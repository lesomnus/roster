package login

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWhatALoopbackRedirectIs: `http`, the IP literal `127.0.0.1` or `[::1]`,
// any port and any path -- RFC 8252 §7.3's -- and nothing else.
//
// Not `localhost`, which RFC 8252 §8.3 tells a native app not to use and which
// is a name a tenant can claim; and not `https`, which no command line can
// answer on the loopback interface with a certificate a browser takes.
func TestWhatALoopbackRedirectIs(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://127.0.0.1:53117/callback": true,
		"http://127.0.0.1/callback":       true,
		"http://127.0.0.1":                true,
		"http://[::1]:53117/callback":     true,
		"http://[::1]/":                   true,
		"HTTP://127.0.0.1:8000/x?y=z":     true,

		"https://127.0.0.1:53117/callback":  false,
		"http://localhost:53117/callback":   false,
		"http://127.0.0.2:53117/callback":   false,
		"http://[::ffff:127.0.0.1]:1/cb":    false,
		"http://contoso.app.test/callback":  false,
		"https://contoso.app.test/callback": false,
		"http://127.0.0.1.example/callback": false,
		"com.example.app:/callback":         false,
		"":                                  false,
	} {
		require.Equal(t, want, loopback(raw), raw)
	}
}

// registrationOf is a Hydra that answers one client's registration, and no
// challenge: what `arrivedAt` falls back to.
func registrationOf(t *testing.T, redirects ...string) *App {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "cli", "redirect_uris": redirects})
	}))
	t.Cleanup(srv.Close)

	return &App{admin: admin{base: srv.URL, client: srv.Client()}}
}

// TestALoopbackAddressIsNeverATenantsHost: what `arrivedAt` reads a flow's
// tenant from, with loopback addresses in the request and the registration.
//
// The loopback redirect was answered as the name `127.0.0.1`, which no tenant
// answers at; and a registration with one beside the tenant's host was two
// hosts, refused. A client with nothing but loopback addresses is refused
// saying that they name no tenant and what to register beside them.
func TestALoopbackAddressIsNeverATenantsHost(t *testing.T) {
	const asked = "https://issuer.test/oauth2/auth?client_id=cli&redirect_uri=http%3A%2F%2F127.0.0.1%3A53117%2Fcallback"

	t.Run("beside one tenant's host", func(t *testing.T) {
		a := registrationOf(t, "http://127.0.0.1/callback", "https://contoso.app.test/callback", "http://[::1]/callback")
		name, err := a.arrivedAt(t.Context(), asked, "cli")
		require.NoError(t, err)
		require.Equal(t, "contoso.app.test", name)
	})

	t.Run("alone", func(t *testing.T) {
		a := registrationOf(t, "http://127.0.0.1/callback", "http://[::1]/callback")
		_, err := a.arrivedAt(t.Context(), asked, "cli")
		require.ErrorContains(t, err, "a loopback address names no tenant")
		require.ErrorContains(t, err, "register a redirect at the tenant's own host beside them")
		require.ErrorContains(t, err, "roster login doctor")
	})

	t.Run("beside two tenants' hosts", func(t *testing.T) {
		a := registrationOf(t, "http://127.0.0.1/callback", "https://contoso.app.test/callback", "https://fabrikam.app.test/callback")
		_, err := a.arrivedAt(t.Context(), asked, "cli")
		require.ErrorContains(t, err, "contoso.app.test and fabrikam.app.test")
		require.ErrorContains(t, err, "two answers")
	})
}
