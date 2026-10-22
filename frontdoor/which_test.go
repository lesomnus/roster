package frontdoor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/auth/authsession"

	rstr "github.com/lesomnus/roster/rstr"
)

// which is `GET /session` as a page asks it: with the cookie, and nothing else.
func which(d *Door, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://contoso.example.com/session", nil)
	if c != nil {
		r.Header.Set("Cookie", c.Name+"="+c.Value)
	}

	w := httptest.NewRecorder()
	d.Which().ServeHTTP(w, r)

	return w
}

// TestABrowserIsToldWhoseStoreItsPageDraws is [Door.Which]: the name a page
// keeps its store under, for the session the browser holds -- and nobody's for
// one that acts for nobody yet.
func TestABrowserIsToldWhoseStoreItsPageDraws(t *testing.T) {
	t.Run("nobody signed in is nobody's", func(t *testing.T) {
		x := require.New(t)

		d := doorFor(t, &vouch{}, nil)
		x.Equal(http.StatusUnauthorized, which(d, nil).Code)
	})

	// Half way has no delegation, so nothing the page could ask would be
	// answered as anybody: the page draws the second form, and a store named
	// now would be named for a session that is not yet one.
	t.Run("half way is nobody's yet", func(t *testing.T) {
		x := require.New(t)

		d := doorFor(t, &vouch{delegate: func(*rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
			return halfway(), nil
		}}, nil)
		c := firstForm(t, d, http.StatusOK)

		x.Equal(http.StatusUnauthorized, which(d, c).Code)
	})

	t.Run("signed in is theirs, by the name the proxy holds a call to", func(t *testing.T) {
		x := require.New(t)

		d := doorFor(t, &vouch{delegate: func(*rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
			return whole(), nil
		}}, nil)
		c := firstForm(t, d, http.StatusNoContent)

		w := which(d, c)
		x.Equal(http.StatusOK, w.Code, w.Body.String())
		x.Equal("no-store", w.Header().Get("Cache-Control"), "a name for one session may be kept for the next")

		var v authsession.Which
		x.NoError(json.Unmarshal(w.Body.Bytes(), &v))
		x.Equal(someone.String(), v.Id)
		x.Equal(somehere.String(), v.TenantId)

		r := httptest.NewRequest(http.MethodGet, "http://contoso.example.com/session", nil)
		r.Header.Set("Cookie", c.Name+"="+c.Value)
		s, err := d.c.Sessions.Read(t.Context(), d.keyOf(r))
		x.NoError(err)
		x.Equal(authsession.IdentityOf(s), v.Identity)
	})
}

// TestAPageLeftHoldingAnotherCallersStoreIsRefusedAtTheApp is the proxy's half
// of [Door.Which]: a call that names the store it was made for is handed on
// only when that is this browser's session's, and the name stays here.
//
// The cookie is every tab's, so whoever signed in last in any tab is who a call
// would be made as; a page still holding the store of somebody before them
// would read this person's rows into it, or send what it kept for them as this
// one. Refused here, it asks again who is signed in.
func TestAPageLeftHoldingAnotherCallersStoreIsRefusedAtTheApp(t *testing.T) {
	x := require.New(t)

	d := doorFor(t, &vouch{delegate: func(*rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
		return whole(), nil
	}}, nil)
	c := firstForm(t, d, http.StatusNoContent)

	var v authsession.Which
	x.NoError(json.Unmarshal(which(d, c).Body.Bytes(), &v))
	mine := v.Identity
	x.NotEmpty(mine)

	// roster, as far as the proxy can tell: whatever reaches it, as it arrived.
	var seen []http.Header
	roster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(roster.Close)
	at, err := url.Parse(roster.URL)
	x.NoError(err)

	p := d.Proxy(at, func(context.Context, string) (Bearer, error) { return Bearer{Token: "rt_app"}, nil })
	call := func(names ...string) int {
		r := httptest.NewRequest(http.MethodPost, "http://contoso.example.com"+rstr.MeService_Get_FullMethodName, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Connect-Protocol-Version", "1")
		r.Header.Set("Cookie", c.Name+"="+c.Value)
		for _, name := range names {
			r.Header.Add(authsession.IdentityHeader, name)
		}

		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)

		return w.Code
	}

	x.Equal(http.StatusOK, call(mine), "a call made for this session's store was refused")
	x.Len(seen, 1)
	x.Empty(seen[0].Values(authsession.IdentityHeader), "the name of a page's store went on to roster")

	x.Equal(http.StatusUnauthorized, call("0123456789abcdef"), "a call made for another store was handed on")
	x.Equal(http.StatusUnauthorized, call(mine, "0123456789abcdef"), "a call naming two stores was handed on for the one that is this session's")
	x.Len(seen, 1, "a refused call reached roster")

	// A call that names no store is not a page's store's, and is handed on as
	// it always was.
	x.Equal(http.StatusOK, call())
	x.Len(seen, 2)
}
