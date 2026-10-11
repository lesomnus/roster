package account_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/internal/idptest"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestSomebodySignsInHereThroughGitHub is the account app's half of what
// `login/github_test.go` walks: the same `arrives`, so what is pinned here is
// that this app hands it the GitHub it was told about, and that the round trip
// ends at a session for the account GitHub's id names.
func TestSomebodySignsInHereThroughGitHub(t *testing.T) {
	x := require.New(t)
	g := idptest.NewGitHub(t, clientId, "the-github-secret")
	t.Setenv("GITHUB_SECRET", "the-github-secret")
	d := serve(t, account.Enrolling(), func(c *account.Config) { c.GitHub = g.URL })

	_, err := d.ungated.Connection().Add(t.Context(), rstr.ConnectionAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		Name:   "github", Issuer: g.URL, ClientId: clientId, SecretRef: "env:GITHUB_SECRET",
	}.Build())
	x.NoError(err)

	g.User = map[string]any{"id": 583231, "login": "octocat", "name": "The Octocat"}
	g.Emails = []map[string]any{{"email": "octocat@contoso.com", "primary": true, "verified": true}}

	b := d.browser(t, "contoso.test")
	code, body := b.do(t, http.MethodGet, "/login?connection=github", "", nil)
	x.Equal(http.StatusOK, code, body)

	code, body = b.rpc(t, "/roster.MeService/Get", `{}`)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, `"octocat"`, "the GitHub account was not enrolled as the local part of its address")
	x.Contains(body, `"583231"`, "the identity is not GitHub's id for the account")
	x.Equal("the-github-secret", g.Sent().Get("client_secret"))
}
