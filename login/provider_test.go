package login_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/internal/idptest"
	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
)

// The other way in, against a directory that agrees with everybody.
//
// `scripts/hydra.sh` walks the password half through a real Hydra with curl and
// cannot walk this one: there is no provider in `compose.yaml` to be redirected
// to. So this is where the round trip is held -- the state cookie, the
// exchange, the `Identity`, and that what Hydra is told is the same `Holder.id`
// a password would have produced.

// connect writes one of contoso's providers, pointing at the fake.
func (d *deployment) connect(t *testing.T, p *idptest.Idp, name string) {
	t.Helper()
	x := require.New(t)

	tn, err := d.s.Ungated.Tenant().Get(t.Context(), rstr.TenantGetRequest_builder{
		Ref: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
	}.Build())
	x.NoError(err)

	// No `secret_ref`: a public client, so nothing here has to resolve one.
	// What a reference costs is `account/account_test.go`'s subject.
	_, err = d.s.Ungated.Connection().Add(t.Context(), rstr.ConnectionAddRequest_builder{
		Tenant:   rstr.TenantRef_builder{Id: tn.GetId()}.Build(),
		Name:     name,
		Issuer:   p.URL,
		ClientId: p.Audience,
		Scopes:   []string{"email", "profile"},
	}.Build())
	x.NoError(err)
}

// through walks the whole round trip: the button, the directory, the callback.
func (d *deployment) through(t *testing.T, b *http.Client, challenge, name string) *http.Response {
	t.Helper()
	x := require.New(t)

	res, err := b.Get(d.app.URL + "/provider?login_challenge=" + url.QueryEscape(challenge) +
		"&connection=" + url.QueryEscape(name))
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusFound, res.StatusCode, "the button did not leave for the provider")

	// The directory, which redirects back at once.
	res, err = b.Get(res.Header.Get("location"))
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusFound, res.StatusCode)

	back, err := b.Get(res.Header.Get("location"))
	x.NoError(err)
	t.Cleanup(func() { back.Body.Close() })

	return back
}

// TestSomebodyArrivesThroughAProvider is the port's whole claim: a directory's
// answer ends where a password ends, at `acceptLoginRequest{Holder.id}`.
func TestSomebodyArrivesThroughAProvider(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, "login-app")
	d := serve(t)
	b := d.browser(t)

	d.connect(t, p, "entra")
	p.Subject = "erin-at-entra"

	// The identity, written by an operator: `enrol: invited` is the default and
	// this person was expected.
	_, err := d.s.Ungated.Identity().Add(t.Context(), rstr.IdentityAddRequest_builder{
		Holder:   rstr.HolderRef_builder{Id: d.who["contoso"].Bytes()}.Build(),
		Provider: "entra",
		Subject:  "erin-at-entra",
	}.Build())
	x.NoError(err)

	d.hydra.raise("c1", "contoso-web")

	res := d.through(t, b, "c1", "entra")
	x.Equal(http.StatusSeeOther, res.StatusCode)
	x.Contains(res.Header.Get("location"), "consent_challenge=c1")

	subject, _ := d.hydra.told()
	x.Equal(d.who["contoso"].String(), subject, "hydra was told the wrong subject")

	// And the claims, which need a session this app minted for the person --
	// the reason the callback signs them in here rather than only telling
	// Hydra. A provider sign-in that skipped it would answer a token with a
	// `sub` and nothing else.
	res, err = b.Get(d.app.URL + "/consent?consent_challenge=c1")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusSeeOther, res.StatusCode)

	_, claims := d.hydra.told()
	x.Equal("erin", claims["preferred_username"])
	x.Equal("Erin of contoso", claims["name"])
}

// TestAStrangerIsRefusedUnlessTheDeploymentEnrols is the policy, both ways.
//
// The directory vouching for somebody is not this deployment saying they have
// an account: `Invited` is the default and it is the one that refuses.
func TestAStrangerIsRefusedUnlessTheDeploymentEnrols(t *testing.T) {
	t.Run("invited refuses", func(t *testing.T) {
		x := require.New(t)
		p := idptest.New(t, "login-app")
		d := serve(t)

		d.connect(t, p, "entra")
		p.Subject = "nobody-here"
		p.Claims = map[string]any{"email": "stranger@contoso.example"}
		d.hydra.raise("c1", "contoso-web")

		res := d.through(t, d.browser(t), "c1", "entra")
		x.Equal(http.StatusForbidden, res.StatusCode)

		_, told := d.hydra.said()
		x.False(told, "hydra was told about somebody with no account")
	})

	t.Run("enrolling makes them", func(t *testing.T) {
		x := require.New(t)
		p := idptest.New(t, "login-app")
		d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Enrolling() })

		d.connect(t, p, "entra")
		p.Subject = "new-at-entra"
		p.Claims = map[string]any{"email": "dana@contoso.example", "name": "Dana"}
		d.hydra.raise("c1", "contoso-web")

		res := d.through(t, d.browser(t), "c1", "entra")
		x.Equal(http.StatusSeeOther, res.StatusCode)

		// Named by the local part of the address, which is `Enrolling`'s whole
		// policy and the one thing a deployment cannot change without writing
		// its own.
		who, err := d.s.Ungated.Holder().Get(t.Context(), rstr.HolderGetRequest_builder{
			Ref: rstr.HolderRef_builder{
				Slug: rstr.HolderRefBySlug_builder{
					Tenant: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
					Alias:  proto.String("dana"),
				}.Build(),
			}.Build(),
			Select: rstr.HolderSelect_builder{Name: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("Dana", who.GetName())

		id, err := pdid.From(who.GetId())
		x.NoError(err)
		subject, _ := d.hydra.told()
		x.Equal(id.String(), subject)
	})
}

// TestACallbackWithoutItsOwnStateIsRefused: the state cookie is what binds one
// browser to one round trip, and a callback is the one hop somebody else can
// aim at this app.
func TestACallbackWithoutItsOwnStateIsRefused(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, "login-app")
	d := serve(t)
	b := d.browser(t)

	d.connect(t, p, "entra")
	p.Subject = "erin-at-entra"
	d.hydra.raise("c1", "contoso-web")

	res, err := b.Get(d.app.URL + "/provider?login_challenge=c1&connection=entra")
	x.NoError(err)
	defer res.Body.Close()
	to, err := url.Parse(res.Header.Get("location"))
	x.NoError(err)
	state := to.Query().Get("state")
	x.NotEmpty(state)

	// The right state, in a browser that did not start it.
	other := d.browser(t)
	res, err = other.Get(d.app.URL + "/callback?code=the-code&state=" + url.QueryEscape(state))
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusBadRequest, res.StatusCode)

	// And the browser that did start it, with somebody else's answer.
	res, err = b.Get(d.app.URL + "/callback?code=the-code&state=not-the-one")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusBadRequest, res.StatusCode)

	_, told := d.hydra.said()
	x.False(told, "hydra was told about a flow nobody started")
}

// TestAProviderFlowReachesOnlyItsOwnOperator: the challenge decides which
// operator's `Connection` rows are read, so fabrikam's client cannot walk into
// contoso's directory.
func TestAProviderFlowReachesOnlyItsOwnOperator(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, "login-app")
	d := serve(t)

	d.connect(t, p, "entra")
	d.hydra.raise("f1", "fabrikam-web")

	res, err := d.browser(t).Get(d.app.URL + "/provider?login_challenge=f1&connection=entra")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusNotFound, res.StatusCode, "fabrikam reached contoso's provider")
}

// TestATenantWithNoPasswordDrawsNoForm: `/flow` says what roster says.
//
// The page cannot be the place this is decided -- `Vouch.Verify` refuses a
// password for such a tenant either way -- so what is checked here is that the
// two agree, and that the form is not drawn under a door that is shut.
func TestATenantWithNoPasswordDrawsNoForm(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, "login-app")
	d := serve(t)

	d.connect(t, p, "entra")
	d.hydra.raise("c1", "contoso-web")

	asks := func() (bool, []any) {
		t.Helper()
		res, err := d.browser(t).Get(d.app.URL + "/flow?login_challenge=c1")
		x.NoError(err)
		defer res.Body.Close()
		var v struct {
			Password  bool  `json:"password"`
			Providers []any `json:"providers"`
		}
		x.NoError(json.NewDecoder(res.Body).Decode(&v))

		return v.Password, v.Providers
	}

	// The control: unset is yes, which is what every tenant written before the
	// field existed reads.
	form, providers := asks()
	x.True(form)
	x.Len(providers, 1)

	tn, err := d.s.Ungated.Tenant().Get(t.Context(), rstr.TenantGetRequest_builder{
		Ref:    rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
		Select: rstr.TenantSelect_builder{DateUpdated: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Tenant().Update(t.Context(), rstr.TenantUpdateRequest_builder{
		Ref:         rstr.TenantRef_builder{Id: tn.GetId()}.Build(),
		DateUpdated: tn.GetDateUpdated(),
		Config:      rstr.TenantConfig_builder{Password: proto.Bool(false)}.Build(),
	}.Build())
	x.NoError(err)

	form, providers = asks()
	x.False(form, "the form is drawn under a door roster has shut")
	x.Len(providers, 1, "the directory is still a way in")

	// And the door: the right password, refused.
	_, code := d.signIn(t, d.browser(t), "c1", "erin", password)
	x.Equal(http.StatusUnauthorized, code)
}
