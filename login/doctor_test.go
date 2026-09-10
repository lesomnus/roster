package login_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lesomnus/roster/login"
	"github.com/stretchr/testify/require"
)

// A Hydra with nothing in it but clients, which is all `Doctor` reads.
func clients(t *testing.T, have map[string]map[string]any) string {
	t.Helper()

	m := http.NewServeMux()
	m.HandleFunc("GET /admin/clients/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := have[r.PathValue("id")]
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)

			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	})

	s := httptest.NewServer(m)
	t.Cleanup(s.Close)

	return s.URL
}

// good is a client registered the way this stack needs, which is the shape
// every case below breaks one field of.
func good() map[string]any {
	return map[string]any{
		"client_id":                  "app",
		"client_name":                "app",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"scope":                      "openid offline profile email",
		"redirect_uris":              []string{"https://app.test/callback"},
		"post_logout_redirect_uris":  []string{"https://app.test"},
		"token_endpoint_auth_method": login.AuthMethod,
	}
}

// TestDoctorPassesAClientRegisteredTheWayThisStackNeeds, which is the case that
// has to be silent or none of the others are worth anything.
func TestDoctorPassesAClientRegisteredTheWayThisStackNeeds(t *testing.T) {
	x := require.New(t)

	at := clients(t, map[string]map[string]any{"app": good()})
	found, err := login.Doctor(context.Background(), at, nil, map[string][]string{"contoso": {"app"}})
	x.NoError(err)
	x.Empty(found)
}

// TestDoctorFindsWhatCostAnHourEach: one case per defect a person found in a
// browser. Each of these was a deployment nobody could sign in to, or a
// sign-out that ended somewhere it should not, and each was invisible to every
// other gate this repository has.
func TestDoctorFindsWhatCostAnHourEach(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(map[string]any)
		how    login.Severity
		says   string
	}{
		{
			// The one that stopped every sign-in: registered for the body,
			// while every relying party here sends the header.
			name:   "the wrong token_endpoint_auth_method",
			break_: func(v map[string]any) { v["token_endpoint_auth_method"] = "client_secret_post" },
			how:    login.Broken,
			says:   "client_secret_post",
		},
		{
			name:   "no token_endpoint_auth_method at all",
			break_: func(v map[string]any) { delete(v, "token_endpoint_auth_method") },
			how:    login.Broken,
			says:   "names no token_endpoint_auth_method",
		},
		{
			// The one that ended a sign-out on the issuer's error page.
			name:   "no post_logout_redirect_uris",
			break_: func(v map[string]any) { delete(v, "post_logout_redirect_uris") },
			how:    login.Fragile,
			says:   "post_logout_redirect_uris",
		},
		{
			name:   "no redirect_uris",
			break_: func(v map[string]any) { delete(v, "redirect_uris") },
			how:    login.Broken,
			says:   "redirect_uris",
		},
		{
			name:   "no openid in the scope",
			break_: func(v map[string]any) { v["scope"] = "profile email" },
			how:    login.Broken,
			says:   "openid",
		},
		{
			name:   "no authorization_code",
			break_: func(v map[string]any) { v["grant_types"] = []string{"refresh_token"} },
			how:    login.Broken,
			says:   "authorization_code",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			v := good()
			tc.break_(v)

			at := clients(t, map[string]map[string]any{"app": v})
			found, err := login.Doctor(context.Background(), at, nil, map[string][]string{"contoso": {"app"}})
			x.NoError(err)
			x.Len(found, 1)
			x.Equal(tc.how, found[0].Severity)
			x.Contains(found[0].What, tc.says)
			x.NotEmpty(found[0].Costs, "a finding that cannot say what it costs is a preference")
		})
	}
}

// TestDoctorFindsAClientNamedHereAndNowhereElse: the half `login.clients`
// cannot check for itself.
//
// A client id in this app's configuration that Hydra has never heard of is an
// operator whose people reach a page saying the login is not working, and the
// only sign of it is in a log nobody is reading at the time.
func TestDoctorFindsAClientNamedHereAndNowhereElse(t *testing.T) {
	x := require.New(t)

	at := clients(t, map[string]map[string]any{"app": good()})
	found, err := login.Doctor(context.Background(), at, nil, map[string][]string{"contoso": {"app", "ghost"}})
	x.NoError(err)
	x.Len(found, 1)
	x.Equal(login.Broken, found[0].Severity)
	x.Equal("ghost", found[0].About)
	x.Contains(found[0].What, "hydra has no such client")
}

// TestDoctorPutsWhatIsBrokenFirst, because a deployment reads the first line.
func TestDoctorPutsWhatIsBrokenFirst(t *testing.T) {
	x := require.New(t)

	v := good()
	delete(v, "post_logout_redirect_uris")
	v["token_endpoint_auth_method"] = "none"

	at := clients(t, map[string]map[string]any{"app": v})
	found, err := login.Doctor(context.Background(), at, nil, map[string][]string{"contoso": {"app"}})
	x.NoError(err)
	x.Len(found, 2)
	x.Equal(login.Broken, found[0].Severity)
	x.Equal(login.Fragile, found[1].Severity)
}
