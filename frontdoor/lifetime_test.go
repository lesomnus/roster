package frontdoor

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	rstr "github.com/lesomnus/roster/rstr"
)

// TestASignInLastsAsLongAsTheAppSaid is [Config.Lifetime] on every way in, and
// the session each one mints following it.
//
// The Login App's store was set to Hydra's eight hours and its sessions lasted
// fifteen minutes. Nothing asked roster for a lifetime, so every delegation got
// roster's default, and every session took the delegation's expiry -- which is
// the one `authsession` keeps (#121). So each way in is asked what it requested
// and the cookie what it was handed: a way in that left the expiry out is the
// same bug on one road, and three roads that were right would not show it.
func TestASignInLastsAsLongAsTheAppSaid(t *testing.T) {
	const lasts = 8 * time.Hour

	// roster, as far as this goes: what it was asked for, or an hour of its own.
	minted := func(asked *timestamppb.Timestamp) *rstr.VouchDelegateResponse {
		v := whole()
		if asked != nil {
			v.SetExpires(asked)
		}

		return v
	}

	// Each signs a browser in through one way, and answers what roster was
	// asked for and the cookie the browser was handed.
	ways := []struct {
		name string
		in   func(t *testing.T, f *vouch, d *Door) (*timestamppb.Timestamp, *http.Cookie)
	}{
		{"the first form", func(t *testing.T, f *vouch, d *Door) (*timestamppb.Timestamp, *http.Cookie) {
			var asked *timestamppb.Timestamp
			f.delegate = func(in *rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
				asked = in.GetExpires()
				return minted(asked), nil
			}

			c := firstForm(t, d, http.StatusNoContent)

			return asked, c
		}},
		{"the second form", func(t *testing.T, f *vouch, d *Door) (*timestamppb.Timestamp, *http.Cookie) {
			var asked *timestamppb.Timestamp
			f.delegate = func(in *rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
				if in.GetContinuation() == "" {
					return halfway(), nil
				}
				asked = in.GetExpires()
				return minted(asked), nil
			}

			half := firstForm(t, d, http.StatusOK)
			w := post(d, "/session/continue", `{"kind":"totp","secret":"123456"}`, half)
			require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
			cs := w.Result().Cookies()
			require.Len(t, cs, 1)

			return asked, cs[0]
		}},
		{"a provider's claim", func(t *testing.T, f *vouch, d *Door) (*timestamppb.Timestamp, *http.Cookie) {
			var asked *timestamppb.Timestamp
			f.accept = func(in *rstr.VouchAcceptRequest) (*rstr.VouchDelegateResponse, error) {
				asked = in.GetExpires()
				return minted(asked), nil
			}

			w := httptest.NewRecorder()
			require.NoError(t, d.Accept(t.Context(), w, Claim{Tenant: somehere.String(), Provider: "entra", Subject: "dana-at-entra"}))
			cs := w.Result().Cookies()
			require.Len(t, cs, 1)

			return asked, cs[0]
		}},
		{"a link", func(t *testing.T, f *vouch, d *Door) (*timestamppb.Timestamp, *http.Cookie) {
			var asked *timestamppb.Timestamp
			f.redeem = func(in *rstr.VouchRedeemRequest) (*rstr.VouchDelegateResponse, error) {
				asked = in.GetExpires()
				return minted(asked), nil
			}

			w := httptest.NewRecorder()
			require.NoError(t, d.Redeem(t.Context(), w, "rl_link"))
			cs := w.Result().Cookies()
			require.Len(t, cs, 1)

			return asked, cs[0]
		}},
	}

	for _, way := range ways {
		t.Run(way.name, func(t *testing.T) {
			x := require.New(t)

			f := &vouch{}
			asked, c := way.in(t, f, doorFor(t, f, func(c *Config) { c.Lifetime = lasts }))
			x.NotNil(asked, "roster was asked for nothing, so it minted its default and the session ended with it")
			x.WithinDuration(time.Now().Add(lasts), asked.AsTime(), time.Minute)
			x.WithinDuration(time.Now().Add(lasts), c.Expires, time.Minute, "the session does not last what its delegation does")

			// And an app that said nothing leaves it to roster, which is the
			// account app: its session is somebody at its own page.
			f = &vouch{}
			asked, _ = way.in(t, f, doorFor(t, f, nil))
			x.Nil(asked, "an app that said nothing asked for a lifetime anyway")
		})
	}
}
