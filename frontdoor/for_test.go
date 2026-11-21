package frontdoor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	rstr "github.com/lesomnus/roster/rstr"
)

// signedIn is [Door.SignedIn] for the browser holding `c`.
func signedIn(d *Door, c *http.Cookie) (SignIn, bool) {
	r := httptest.NewRequest(http.MethodGet, "http://contoso.example.com/", nil)
	r.Header.Set("Cookie", c.Name+"="+c.Value)

	return d.SignedIn(r.Context(), r)
}

// TestASignInSaysWhatItWasFor is [Config.For], and [Door.SignedIn] reading it
// back.
//
// A session said who signed in and nothing of what for, so an app whose
// sign-in is a step of something else had only the cookie to go by: the Login
// App answered every challenge Hydra raised with the last sign-in the browser
// made, whichever challenge its form had been answered at (#278). What the
// sign-in was for is the app's to say, of the request that finished it, and
// the app's to hold a later request to.
func TestASignInSaysWhatItWasFor(t *testing.T) {
	asked := func(c *Config) {
		c.For = func(r *http.Request) string { return r.URL.Query().Get("for") }
	}
	form := `{"alias":"somebody","password":"correct horse battery staple"}`

	t.Run("the first form's", func(t *testing.T) {
		x := require.New(t)

		d := doorFor(t, &vouch{delegate: func(*rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
			return whole(), nil
		}}, asked)

		w := post(d, "/session?for=one", form, nil)
		x.Equal(http.StatusNoContent, w.Code, w.Body.String())

		s, ok := signedIn(d, w.Result().Cookies()[0])
		x.True(ok)
		x.Equal("one", s.For)
		x.Equal(someone, s.Who)
		x.Equal(somehere, s.Tenant)
	})

	// Half way names nobody, so there is nothing to be for yet; and the second
	// form is the request the sign-in finished on.
	t.Run("the second form's, where there is one", func(t *testing.T) {
		x := require.New(t)

		d := doorFor(t, &vouch{delegate: func(in *rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
			if in.GetContinuation() == "" {
				return halfway(), nil
			}

			return whole(), nil
		}}, asked)

		w := post(d, "/session?for=one", form, nil)
		x.Equal(http.StatusOK, w.Code, w.Body.String())
		half := w.Result().Cookies()[0]
		_, ok := signedIn(d, half)
		x.False(ok, "a browser half way was read as signed in")

		w = post(d, "/session/continue?for=two", `{"kind":"totp","secret":"123456"}`, half)
		x.Equal(http.StatusNoContent, w.Code, w.Body.String())

		s, ok := signedIn(d, w.Result().Cookies()[0])
		x.True(ok)
		x.Equal("two", s.For)
	})

	// An app whose sign-in is for its own pages says nothing, and is as it was.
	t.Run("nothing, where the app said nothing", func(t *testing.T) {
		x := require.New(t)

		d := doorFor(t, &vouch{delegate: func(*rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
			return whole(), nil
		}}, nil)

		s, ok := signedIn(d, firstForm(t, d, http.StatusNoContent))
		x.True(ok)
		x.Empty(s.For)
	})

	// The app that finishes a provider's round trip is already where the
	// sign-in was for, and no request of a form's is there to ask.
	t.Run("nor on a provider's word", func(t *testing.T) {
		x := require.New(t)

		d := doorFor(t, &vouch{accept: func(*rstr.VouchAcceptRequest) (*rstr.VouchDelegateResponse, error) {
			return whole(), nil
		}}, asked)

		w := httptest.NewRecorder()
		x.NoError(d.Accept(t.Context(), w, Claim{Tenant: somehere.String(), Provider: "entra", Subject: "dana-at-entra"}))

		s, ok := signedIn(d, w.Result().Cookies()[0])
		x.True(ok)
		x.Empty(s.For)
	})
}
