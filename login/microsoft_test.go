package login_test

import (
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

// Any Microsoft account through one connection: the authority of more than one
// directory, whose document names `{tenantid}` where its issuer's directory
// goes and whose tokens each name their own (`arrives/microsoft.go`).

// Directories, the way Microsoft writes theirs.
const (
	contosoTid  = "72f988bf-86f1-41af-91ab-2d7cd011db47"
	fabrikamTid = "1a2b3c4d-5e6f-4081-92a3-b4c5d6e7f809"
)

// connectAt writes one of contoso's providers at an issuer of the fake's.
func (d *deployment) connectAt(t *testing.T, p *idptest.Idp, name, issuer string) {
	t.Helper()
	x := require.New(t)

	tn, err := d.s.Ungated.Tenant().Get(t.Context(), rstr.TenantGetRequest_builder{
		Ref: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Connection().Add(t.Context(), rstr.ConnectionAddRequest_builder{
		Tenant:   rstr.TenantRef_builder{Id: tn.GetId()}.Build(),
		Name:     name,
		Issuer:   issuer,
		ClientId: p.Audience,
		Scopes:   []string{"email", "profile"},
	}.Build())
	x.NoError(err)
}

// TestAnyMicrosoftAccountArrivesThroughOneConnection: people from two
// directories and a personal account, through `common`, each themselves.
func TestAnyMicrosoftAccountArrivesThroughOneConnection(t *testing.T) {
	for _, word := range []string{"common", "organizations"} {
		t.Run(word, func(t *testing.T) {
			x := require.New(t)
			p := idptest.New(t, "login-app")
			d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Enrolling() })
			d.connectAt(t, p, "microsoft", p.URL+"/"+word+"/v2.0")

			arrive := func(challenge, tid, sub, address string) string {
				t.Helper()
				p.Tid, p.Subject = tid, sub
				p.Claims = map[string]any{"email": address, "xms_edov": true}
				d.hydra.raise(challenge, "contoso-web")
				res := d.through(t, d.browser(t), challenge, "microsoft")
				x.Equal(http.StatusSeeOther, res.StatusCode, "somebody from %s did not arrive", tid)
				id, _ := d.hydra.told()

				return id
			}

			dana := arrive("c1", contosoTid, "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ", "dana@contoso.example")
			erin := arrive("c2", fabrikamTid, "BBBBBBBBBBBBBBBBBBBBBIkzqFVrSaSaFHy782bbtaQ", "erin@fabrikam.example")
			x.NotEqual(dana, erin)
			if word == "common" {
				fred := arrive("c3", idptest.Consumers, "CCCCCCCCCCCCCCCCCCCCCIkzqFVrSaSaFHy782bbtaQ", "fred@outlook.example")
				x.NotEqual(dana, fred)
			}

			// And the same person again is the same person.
			x.Equal(dana, arrive("c4", contosoTid, "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ", "dana@contoso.example"))
		})
	}
}

// TestATokenIsHeldToTheDirectoryItNames: with the issuer a template, what the
// issuer check was is that a token's `iss` is that template with its own
// `tid` in it -- and a connection to one directory keeps the plain check.
func TestATokenIsHeldToTheDirectoryItNames(t *testing.T) {
	refused := func(t *testing.T, d *deployment, name string) {
		t.Helper()
		d.hydra.raise("c1", "contoso-web")
		res := d.through(t, d.browser(t), "c1", name)
		require.Equal(t, http.StatusBadRequest, res.StatusCode)
		_, told := d.hydra.said()
		require.False(t, told, "hydra was told about somebody whose token is from another directory than it says")
	}

	t.Run("an issuer that is not its own directory's", func(t *testing.T) {
		p := idptest.New(t, "login-app")
		d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Enrolling() })
		d.connectAt(t, p, "microsoft", p.URL+"/common/v2.0")

		p.Tid, p.Subject = contosoTid, "AAAA"
		p.Claims = map[string]any{"iss": p.URL + "/" + fabrikamTid + "/v2.0", "email": "dana@contoso.example"}
		refused(t, d, "microsoft")
	})

	t.Run("no directory at all", func(t *testing.T) {
		p := idptest.New(t, "login-app")
		d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Enrolling() })
		d.connectAt(t, p, "microsoft", p.URL+"/common/v2.0")

		p.Subject = "AAAA"
		p.Claims = map[string]any{"iss": p.URL + "/" + contosoTid + "/v2.0", "email": "dana@contoso.example"}
		refused(t, d, "microsoft")
	})

	// The template is the document's and only an authority's: a connection
	// to one directory still wants its own issuer in every token.
	t.Run("a connection to one directory still checks its issuer", func(t *testing.T) {
		p := idptest.New(t, "login-app")
		d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Enrolling() })
		d.connectAt(t, p, "contoso", p.URL+"/"+contosoTid+"/v2.0")

		p.Tid, p.Subject = fabrikamTid, "AAAA"
		p.Claims = map[string]any{"email": "dana@contoso.example"}
		refused(t, d, "contoso")
	})
}

// TestPersonalAccountsAreTheirDirectorysIssuer: `consumers` is not the
// template -- its document names the one directory personal accounts are
// in -- so a connection for them alone is written as that directory's issuer,
// and the alias is refused the way any document naming another issuer is.
func TestPersonalAccountsAreTheirDirectorysIssuer(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, "login-app")
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Enrolling() })
	d.connectAt(t, p, "personal", p.URL+"/"+idptest.Consumers+"/v2.0")
	d.connectAt(t, p, "alias", p.URL+"/consumers/v2.0")

	p.Tid, p.Subject = idptest.Consumers, "CCCC"
	p.Claims = map[string]any{"email": "fred@outlook.example"}
	d.hydra.raise("c1", "contoso-web")
	res := d.through(t, d.browser(t), "c1", "personal")
	x.Equal(http.StatusSeeOther, res.StatusCode)

	d.hydra.raise("c2", "contoso-web")
	res, err := d.browser(t).Get(d.app.URL + "/provider?login_challenge=c2&connection=alias")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusBadGateway, res.StatusCode, "a document naming another issuer than the connection's was believed")
}

// TestMicrosoftsWordMakesAnAddressAnInvitation: Microsoft sends no
// `email_verified`, so `expected` could never find anybody through it. Its
// word is `xms_edov`, in either of the spellings it sends, and without it the
// address is a stranger's -- which is what keeps any directory's administrator
// from putting somebody's address on a user of their own and arriving as them.
func TestMicrosoftsWordMakesAnAddressAnInvitation(t *testing.T) {
	for _, c := range []struct {
		name   string
		claims map[string]any
		want   int
	}{
		{"xms_edov, as true", map[string]any{"xms_edov": true}, http.StatusSeeOther},
		{"xms_edov, as \"1\"", map[string]any{"xms_edov": "1"}, http.StatusSeeOther},
		{"xms_edov, false", map[string]any{"xms_edov": false}, http.StatusForbidden},
		{"nothing said", map[string]any{}, http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := require.New(t)
			p := idptest.New(t, "login-app")
			d := serveAs(t, login.Skip, func(c *login.Config) {
				c.Enrol = arrives.Expected()
				// The fake, as Microsoft's sign-in: `xms_edov` is its word
				// and nobody else's.
				c.Microsoft = p.URL
			})
			d.connectAt(t, p, "microsoft", p.URL+"/common/v2.0")
			dana := d.entered(t, "dana", "dana@contoso.example")

			p.Tid, p.Subject = fabrikamTid, "AAAA"
			p.Claims = map[string]any{"email": "dana@contoso.example"}
			for k, v := range c.claims {
				p.Claims[k] = v
			}
			d.hydra.raise("c1", "contoso-web")
			res := d.through(t, d.browser(t), "c1", "microsoft")
			x.Equal(c.want, res.StatusCode)
			if c.want == http.StatusSeeOther {
				subject, _ := d.hydra.told()
				x.Equal(dana.String(), subject)
			}
		})
	}
}

// TestMicrosoftsWordIsMicrosoftsAlone: `xms_edov` in a token some other issuer
// signed is a claim like any other somebody may have put there -- a user's own
// attribute, mapped into the token -- and an address is verified on the
// provider's word. So the same token from an issuer that is not Microsoft's,
// Microsoft's paths and all, finds nobody by address.
func TestMicrosoftsWordIsMicrosoftsAlone(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, "login-app")
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Expected() })
	d.connectAt(t, p, "microsoft", p.URL+"/common/v2.0")
	d.entered(t, "dana", "dana@contoso.example")

	p.Tid, p.Subject = fabrikamTid, "AAAA"
	p.Claims = map[string]any{"email": "dana@contoso.example", "xms_edov": true}
	d.hydra.raise("c1", "contoso-web")
	res := d.through(t, d.browser(t), "c1", "microsoft")
	x.Equal(http.StatusForbidden, res.StatusCode, "an issuer that is not Microsoft's was believed in Microsoft's words")

	_, told := d.hydra.said()
	x.False(told)
}

// entered is somebody an operator put in by hand with the address they will
// arrive under, and nothing else: no way in, which is what makes the address an
// invitation.
func (d *deployment) entered(t *testing.T, alias, address string) pdid.Id {
	t.Helper()
	x := require.New(t)

	who, err := d.s.Ungated.Holder().Add(t.Context(), rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
		Alias:  alias, Name: "Dana",
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Email().Add(t.Context(), rstr.EmailAddRequest_builder{
		Holder:  rstr.HolderRef_builder{Id: who.GetId()}.Build(),
		Address: address,
	}.Build())
	x.NoError(err)
	id, err := pdid.From(who.GetId())
	x.NoError(err)

	return id
}
