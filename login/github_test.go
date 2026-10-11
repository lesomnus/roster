package login_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/internal/idptest"
	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
)

// GitHub, which is OAuth 2.0 and no OpenID Connect, through the same round trip
// as every other provider: the button, the provider, the callback, and a
// `Holder.id` for Hydra at the end of it (`arrives/github.go`).

// gitHubSecret is the client secret the deployment keeps for the GitHub
// connection, under the reference the connection names.
const gitHubSecret = "the-github-secret"

// serveGitHub is contoso's deployment with a GitHub connection, as a
// deployment writes one: the issuer that is GitHub's, and a reference to the
// secret GitHub wants at its token endpoint.
func serveGitHub(t *testing.T, enrol arrives.Enrol) (*idptest.GitHub, *deployment) {
	t.Helper()
	x := require.New(t)

	g := idptest.NewGitHub(t, "login-app", gitHubSecret)
	d := serveAs(t, login.Skip, func(c *login.Config) {
		c.Enrol = enrol
		c.GitHub = g.URL
		c.Secret = func(ref string) (string, error) {
			if ref == "env:GITHUB_SECRET" {
				return gitHubSecret, nil
			}

			return "", errors.New("no such secret")
		}
	})

	tn, err := d.s.Ungated.Tenant().Get(t.Context(), rstr.TenantGetRequest_builder{
		Ref: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Connection().Add(t.Context(), rstr.ConnectionAddRequest_builder{
		Tenant:    rstr.TenantRef_builder{Id: tn.GetId()}.Build(),
		Name:      "github",
		Issuer:    g.URL,
		ClientId:  "login-app",
		SecretRef: "env:GITHUB_SECRET",
	}.Build())
	x.NoError(err)

	g.User = map[string]any{
		"id": 583231, "login": "octocat", "name": "The Octocat",
		"avatar_url": g.URL + "/avatars/u/583231",
	}
	g.Emails = []map[string]any{
		{"email": "octocat@contoso.example", "primary": true, "verified": true},
	}

	return g, d
}

// TestSomebodyArrivesThroughGitHub: the sign-in that has no ID token ends where
// every other one does, and the person is their account's id there.
func TestSomebodyArrivesThroughGitHub(t *testing.T) {
	x := require.New(t)
	g, d := serveGitHub(t, arrives.Enrolling())
	d.profile(t, rstr.TenantProfile_builder{Fill: true}.Build())
	g.Avatar = face(t, 200)

	d.hydra.raise("c1", "contoso-web")
	res := d.through(t, d.browser(t), "c1", "github")
	x.Equal(http.StatusSeeOther, res.StatusCode)
	x.Contains(res.Header.Get("location"), "consent_challenge=c1")

	// What GitHub was asked, and what it was handed: the addresses, and the
	// secret the deployment keeps, in the form.
	x.Equal("user:email", g.Asked().Get("scope"))
	x.Equal("login-app", g.Asked().Get("client_id"))
	x.Equal(gitHubSecret, g.Sent().Get("client_secret"))

	id, _ := d.hydra.told()
	who := pdid.MustParse(id)

	// Named by the primary address and called what GitHub calls them.
	v, err := d.s.Ungated.Holder().Get(t.Context(), rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("octocat", v.GetAlias())
	x.Equal("The Octocat", v.GetName())

	// Linked by the account's id, which is the one thing about it that does
	// not change.
	link, err := d.s.Ungated.Identity().Get(t.Context(), rstr.IdentityGetRequest_builder{
		Ref: rstr.IdentityRef_builder{Subject: rstr.IdentityRefBySubject_builder{
			TenantId: v.GetTenant().GetId(),
			Provider: proto.String("github"),
			Subject:  proto.String("583231"),
		}.Build()}.Build(),
		Select: rstr.IdentitySelect_builder{Holder: rstr.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(who.Bytes(), link.GetHolder().GetId())

	// The address, stamped on GitHub's word.
	addrs, err := d.s.Ungated.Email().List(t.Context(), rstr.EmailListRequest_builder{
		Filters: []*rstr.EmailFilter{rstr.EmailFilter_builder{
			Holder: rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
		}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(addrs.GetItems(), 1)
	addr, err := d.s.Ungated.Email().Get(t.Context(), rstr.EmailGetRequest_builder{
		Ref:    rstr.EmailRef_builder{Id: addrs.GetItems()[0].GetId()}.Build(),
		Select: rstr.EmailSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("octocat@contoso.example", addr.GetAddress())
	x.NotNil(addr.GetDateVerified())

	// And the picture, from GitHub's own host here and so with the token.
	x.Len(v.GetPortrait().GetRenditions(), 3)

	// A username somebody changes is the same person.
	g.User["login"] = "octocat-renamed"
	d.hydra.raise("c2", "contoso-web")
	res = d.through(t, d.browser(t), "c2", "github")
	x.Equal(http.StatusSeeOther, res.StatusCode)
	again, _ := d.hydra.told()
	x.Equal(id, again, "a renamed GitHub account became somebody else")
}

// TestAGitHubAddressIsAnInvitationOnlyWhenGitHubVerifiedIt: `expected` matches
// an address only on the provider's word that it checked it, and GitHub's
// word is the `verified` of the account's primary address.
func TestAGitHubAddressIsAnInvitationOnlyWhenGitHubVerifiedIt(t *testing.T) {
	entered := func(t *testing.T, d *deployment) pdid.Id {
		t.Helper()
		x := require.New(t)

		who, err := d.s.Ungated.Holder().Add(t.Context(), rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
			Alias:  "dana", Name: "Dana",
		}.Build())
		x.NoError(err)
		_, err = d.s.Ungated.Email().Add(t.Context(), rstr.EmailAddRequest_builder{
			Holder:  rstr.HolderRef_builder{Id: who.GetId()}.Build(),
			Address: "dana@contoso.example",
		}.Build())
		x.NoError(err)
		id, err := pdid.From(who.GetId())
		x.NoError(err)

		return id
	}

	t.Run("verified, so it is them", func(t *testing.T) {
		x := require.New(t)
		g, d := serveGitHub(t, arrives.Expected())
		dana := entered(t, d)
		g.Emails = []map[string]any{{"email": "dana@contoso.example", "primary": true, "verified": true}}

		d.hydra.raise("c1", "contoso-web")
		res := d.through(t, d.browser(t), "c1", "github")
		x.Equal(http.StatusSeeOther, res.StatusCode)
		subject, _ := d.hydra.told()
		x.Equal(dana.String(), subject)
	})

	t.Run("unverified, so it is nobody", func(t *testing.T) {
		x := require.New(t)
		g, d := serveGitHub(t, arrives.Expected())
		entered(t, d)
		g.Emails = []map[string]any{{"email": "dana@contoso.example", "primary": true, "verified": false}}

		d.hydra.raise("c1", "contoso-web")
		res := d.through(t, d.browser(t), "c1", "github")
		x.Equal(http.StatusForbidden, res.StatusCode)
		_, told := d.hydra.said()
		x.False(told, "hydra was told about somebody GitHub had not verified")
	})

	// An address the account has and has not made primary is not who it is.
	t.Run("an address that is not the primary one is not read", func(t *testing.T) {
		x := require.New(t)
		g, d := serveGitHub(t, arrives.Expected())
		entered(t, d)
		g.Emails = []map[string]any{
			{"email": "dana@contoso.example", "primary": false, "verified": true},
			{"email": "mallory@elsewhere.example", "primary": true, "verified": true},
		}

		d.hydra.raise("c1", "contoso-web")
		res := d.through(t, d.browser(t), "c1", "github")
		x.Equal(http.StatusForbidden, res.StatusCode)
	})
}
