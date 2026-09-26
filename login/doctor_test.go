package login_test

import (
	"context"
	"encoding/json"
	"errors"
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
	m.HandleFunc("GET /admin/clients", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		for _, v := range have {
			out = append(out, v)
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
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
	found, err := login.Doctor(context.Background(), at, "", nil, at_(t, "app.test", "contoso"))
	x.NoError(err)
	x.Empty(found)
}

// TestDoctorPassesAPublicClient, a page that signs people in with PKCE.
//
// It has no secret to send in any header, so `none` is its registration rather
// than a mistake -- and the first cut of the check refused it as broken, which
// failed a deployment's sync for the one shape a browser app can take.
func TestDoctorPassesAPublicClient(t *testing.T) {
	x := require.New(t)

	page := good()
	page["token_endpoint_auth_method"] = login.PublicAuthMethod
	page["grant_types"] = []string{"authorization_code"}
	page["scope"] = "openid profile email"

	at := clients(t, map[string]map[string]any{"app": page})
	found, err := login.Doctor(context.Background(), at, "", nil, at_(t, "app.test", "contoso"))
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
			found, err := login.Doctor(context.Background(), at, "", nil, at_(t, "app.test", "contoso"))
			x.NoError(err)
			x.Len(found, 1)
			x.Equal(tc.how, found[0].Severity)
			x.Contains(found[0].What, tc.says)
			x.NotEmpty(found[0].Costs, "a finding that cannot say what it costs is a preference")
		})
	}
}

// TestDoctorKnowsWhichDirectionCosts: the two ways a client and this app's
// configuration can disagree, and only one of them refuses anybody.
//
// TestDoctorKnowsWhichDirectionCosts, in the shape #36 left it.
//
// It used to be a pair about `login.clients`: a client named here that Hydra has
// never heard of is harmless (no flow can be raised for it), and a client Hydra
// holds that nothing here claims is broken (every flow for it reaches a page
// saying the login is not working). The list is gone, so the second half is
// asked of roster's rows instead -- does this client's redirect resolve to a
// tenant, and to exactly one.
func TestDoctorKnowsWhichDirectionCosts(t *testing.T) {
	t.Run("a redirect no tenant answers at", func(t *testing.T) {
		x := require.New(t)

		at := clients(t, map[string]map[string]any{"app": good()})

		// A resolver that claims a different name, so `app.test` is nobody's.
		found, err := login.Doctor(context.Background(), at, "", nil, at_(t, "elsewhere.test", "contoso"))
		x.NoError(err)
		x.Len(found, 1)
		x.Equal(login.Broken, found[0].Severity)
		x.Equal("app", found[0].About)
		x.Contains(found[0].What, "no tenant answers at any of its redirect_uris")
	})

	t.Run("and redirects that answer with two tenants", func(t *testing.T) {
		x := require.New(t)

		both := good()
		both["redirect_uris"] = []string{"https://app.test/callback", "https://other.test/callback"}

		at := clients(t, map[string]map[string]any{"app": both})
		found, err := login.Doctor(context.Background(), at, "", nil, func(_ context.Context, host string) (string, error) {
			switch host {
			case "app.test":
				return "contoso", nil
			case "other.test":
				return "fabrikam", nil
			}

			return "", errNobody
		})
		x.NoError(err)
		x.Len(found, 1)
		x.Equal(login.Broken, found[0].Severity)
		x.Equal("app", found[0].About)

		// A **determinism** finding: which tenant a flow is about would depend
		// on which registered redirect the browser asked for.
		x.Contains(found[0].What, "contoso and fabrikam")
	})

	t.Run("and a run that could not ask says so", func(t *testing.T) {
		x := require.New(t)

		at := clients(t, map[string]map[string]any{"app": good()})
		found, err := login.Doctor(context.Background(), at, "", nil, nil)
		x.NoError(err)
		x.Len(found, 1)
		x.Equal(login.Fragile, found[0].Severity, "a check that was skipped is not a check that passed")
		x.Contains(found[0].What, "not asked")
	})
}

// at_ is a resolver that claims one name for one tenant, which is what `good()`
// registers a redirect on.
func at_(t *testing.T, name, alias string) login.Whose {
	t.Helper()

	return func(_ context.Context, host string) (string, error) {
		if host == name {
			return alias, nil
		}

		return "", errNobody
	}
}

// errNobody is what a resolver answers for a name nothing claims, which is what
// `FrontService.WhoseHost` answers with `NotFound`.
var errNobody = errors.New("no tenant answers at that name")

// TestDoctorPutsWhatIsBrokenFirst, because a deployment reads the first line.
func TestDoctorPutsWhatIsBrokenFirst(t *testing.T) {
	x := require.New(t)

	v := good()
	delete(v, "post_logout_redirect_uris")
	v["token_endpoint_auth_method"] = "client_secret_post"

	at := clients(t, map[string]map[string]any{"app": v})
	found, err := login.Doctor(context.Background(), at, "", nil, at_(t, "app.test", "contoso"))
	x.NoError(err)
	x.Len(found, 2)
	x.Equal(login.Broken, found[0].Severity)
	x.Equal(login.Fragile, found[1].Severity)
}

// hydra is one whose settings can be varied, for the half of `Doctor` that is
// about what Hydra was **told**. Those settings are not on any API, so what is
// faked here is what Hydra *does*: where it sends a browser.
type issuer struct {
	endSession bool
	methods    []string
	afterOut   string // where an end-session with no session lands
	toForm     string // where an authorize lands
}

func serving(t *testing.T, v issuer) string {
	t.Helper()

	m := http.NewServeMux()
	m.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"issuer":                                "https://issuer.test",
			"token_endpoint_auth_methods_supported": v.methods,
		}
		if v.endSession {
			doc["end_session_endpoint"] = "http://" + r.Host + "/oauth2/sessions/logout"
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})
	m.HandleFunc("GET /oauth2/sessions/logout", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, v.afterOut, http.StatusFound)
	})
	m.HandleFunc("GET /oauth2/auth", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, v.toForm, http.StatusFound)
	})

	s := httptest.NewServer(m)
	t.Cleanup(s.Close)

	return s.URL
}

func well() issuer {
	return issuer{
		endSession: true,
		methods:    []string{"client_secret_post", login.AuthMethod, "none"},
		afterOut:   "https://login.test/signed-out",
		toForm:     "https://login.test/login?login_challenge=x",
	}
}

// TestDoctorAsksHydraToDoTheThingsItsSettingsDecide: what Hydra was configured
// with is on the other side of a network, in a file and some environment
// variables, and on no API at all. What is answerable is what it **does**.
func TestDoctorAsksHydraToDoTheThingsItsSettingsDecide(t *testing.T) {
	for _, tc := range []struct {
		name string
		with func(*issuer)
		how  login.Severity
		says string
	}{
		{
			// The one that made a successful sign-out read as a broken
			// deployment: Hydra's own fallback page tells whoever clicked the
			// button to contact an administrator.
			name: "urls.post_logout_redirect unset",
			with: func(v *issuer) { v.afterOut = "https://issuer.test/oauth2/fallbacks/logout/callback" },
			how:  login.Broken,
			says: "urls.post_logout_redirect",
		},
		{
			name: "urls.login unset",
			with: func(v *issuer) { v.toForm = "https://issuer.test/oauth2/fallbacks/error?error=x" },
			how:  login.Broken,
			says: "urls.login",
		},
		{
			name: "no end_session_endpoint",
			with: func(v *issuer) { v.endSession = false },
			how:  login.Broken,
			says: "end_session_endpoint",
		},
		{
			// The registration and the issuer have to agree, and a client can
			// be registered for a method the issuer does not offer.
			name: "the issuer does not offer the method every app here sends",
			with: func(v *issuer) { v.methods = []string{"client_secret_post"} },
			how:  login.Broken,
			says: login.AuthMethod,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			v := well()
			tc.with(&v)

			at := clients(t, map[string]map[string]any{"app": good()})
			found, err := login.Doctor(context.Background(), at, serving(t, v), nil, at_(t, "app.test", "contoso"))
			x.NoError(err)
			x.Len(found, 1)
			x.Equal(tc.how, found[0].Severity)
			x.Contains(found[0].What, tc.says)
			x.Empty(found[0].About, "a finding about the deployment names no client")
		})
	}

	t.Run("all of it as it should be", func(t *testing.T) {
		x := require.New(t)

		at := clients(t, map[string]map[string]any{"app": good()})
		found, err := login.Doctor(context.Background(), at, serving(t, well()), nil, at_(t, "app.test", "contoso"))
		x.NoError(err)
		x.Empty(found)
	})
}

// TestDoctorSaysWhenItCouldNotLook, which is the difference between a check and
// a check that passes for the wrong reason.
//
// The public endpoints are derived from the admin address unless a deployment
// says otherwise, and a derivation can be wrong. Silence there would be a green
// run that asked nothing.
func TestDoctorSaysWhenItCouldNotLook(t *testing.T) {
	x := require.New(t)

	at := clients(t, map[string]map[string]any{"app": good()})
	found, err := login.Doctor(context.Background(), at, "http://127.0.0.1:1", nil, at_(t, "app.test", "contoso"))
	x.NoError(err)
	x.Len(found, 1)
	x.Equal(login.Fragile, found[0].Severity)
	x.Contains(found[0].What, "did not answer")
	x.Contains(found[0].Costs, "--public")
}
