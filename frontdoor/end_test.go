package frontdoor

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// signOut is `DELETE /session` for the browser holding `c`, asked in `ctx`.
func signOut(ctx context.Context, d *Door, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodDelete, "http://contoso.example.com/session", nil).WithContext(ctx)
	r.Header.Set("Cookie", c.Name+"="+c.Value)

	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, r)

	return w
}

// narrowed is what a test's [Config.In] leaves in a context: the tenant it was
// asked to narrow to.
type narrowed struct{}

// TestASessionIsEndedInItsOwnTenant is [Config.In]: a delegation is revoked in
// the tenant its session is of, whatever the request that ended it was about.
//
// The revocation went out in the context [Door.End] was handed. An app whose
// credential is narrowed call by call hands over a narrowed one only from a
// request that is about a tenant, and a sign-out is not always that: the Login
// App's arrives from Hydra naming a subject and nothing else. There the call
// went out as the app's bare key and was refused, so the cookie was cleared
// and the delegation lived on (#278). The session says its tenant, and the app
// says what a call in a tenant looks like.
func TestASessionIsEndedInItsOwnTenant(t *testing.T) {
	x := require.New(t)

	f := &vouch{delegate: func(*rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) {
		return whole(), nil
	}}
	d := doorFor(t, f, func(c *Config) {
		c.In = func(ctx context.Context, tenant pdid.Id) context.Context {
			return context.WithValue(ctx, narrowed{}, tenant)
		}
	})
	c := firstForm(t, d, http.StatusNoContent)

	// Asked from a request about another tenant altogether: what the caller
	// was narrowed to is not what decides.
	elsewhere := context.WithValue(t.Context(), narrowed{}, pdid.New(3))
	x.Equal(http.StatusNoContent, signOut(elsewhere, d, c).Code)
	x.Equal(1, f.revokes)
	x.Equal(somehere, f.revokedIn.Value(narrowed{}), "the delegation was not revoked in its session's own tenant")

	// And an app that said nothing is asked nothing: the context is the
	// request's, which for the account app is already its host's tenant.
	f = &vouch{delegate: f.delegate}
	d = doorFor(t, f, nil)
	c = firstForm(t, d, http.StatusNoContent)

	x.Equal(http.StatusNoContent, signOut(elsewhere, d, c).Code)
	x.Equal(1, f.revokes)
	x.NotEqual(somehere, f.revokedIn.Value(narrowed{}))
}

// TestARevocationThatFailedIsSaid: signing out works whatever roster answers,
// and what roster answered is not thrown away.
//
// The error was dropped, on the reasoning that a roster which cannot be reached
// must not keep somebody signed in. That holds. But dropped, a revocation
// roster refused every single time read exactly like one that worked, and
// nothing anywhere said a delegation had been left alive.
func TestARevocationThatFailedIsSaid(t *testing.T) {
	x := require.New(t)

	said := &bytes.Buffer{}
	was := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(said, nil)))
	t.Cleanup(func() { slog.SetDefault(was) })

	f := &vouch{
		delegate: func(*rstr.VouchDelegateRequest) (*rstr.VouchDelegateResponse, error) { return whole(), nil },
		refuses:  status.Error(codes.PermissionDenied, "this key may not call that"),
	}
	d := doorFor(t, f, nil)
	c := firstForm(t, d, http.StatusNoContent)

	w := signOut(t.Context(), d, c)
	x.Equal(http.StatusNoContent, w.Code, "a revocation roster refused kept somebody signed in")
	x.Equal(1, f.revokes)

	x.Contains(said.String(), "its delegation was not revoked")
	x.Contains(said.String(), "this key may not call that")
	x.NotContains(said.String(), "rd_whole", "the delegation itself was written to the log")
}
