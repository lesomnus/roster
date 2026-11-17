package login_test

import (
	"net/http"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/internal/idptest"
	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
)

// aliases is what contoso's people are called, in order: who a sign-in made,
// and who it left behind.
func (d *deployment) aliases(t *testing.T) []string {
	t.Helper()

	vs, err := d.s.Ungated.Holder().List(t.Context(), rstr.HolderListRequest_builder{
		Filters: []*rstr.HolderFilter{rstr.HolderFilter_builder{
			Tenant: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
		}.Build()},
		Size: 100,
	}.Build())
	require.NoError(t, err)

	out := []string{}
	for _, v := range vs.GetItems() {
		out = append(out, v.GetAlias())
	}
	sort.Strings(out)

	return out
}

// TestAFirstSignInThatCannotBeLinkedMakesNobody: under `enrolling` a first
// sign-in is two calls, `HolderService/Add` and then `IdentityService/Add`.
// A subject roster keeps none of was refused at the second, after the first:
// the person stayed, with the plain alias taken from their address and no way
// in, and since nothing of theirs was found at the next try, that one made
// another, with a suffix. Three tries through a connection whose subject is an
// address answered `502` three times and left three people, `yun` and two
// called `yun-` and four letters nobody chose; and once the connection was
// mended the person arrived as a fourth, the plain name held by a row nobody
// signs in as.
//
// What the link is refused for on its form, the front door can ask before
// anybody is made -- of the functions roster asks (`front.Unstable`, and
// `front.GUID` where a connection names people by `oid`). The answer is the
// one it was: it is the connection's fault and everybody's who arrives
// through it, so it stays this app's failure and is in its log. What changes
// is that nobody is left behind by it, and the person arrives once, by their
// own name, when the connection is mended.
func TestAFirstSignInThatCannotBeLinkedMakesNobody(t *testing.T) {
	for name, the := range map[string]struct {
		broken, mended func(t *testing.T, d *deployment, p *idptest.Idp)
	}{
		"a subject that is an address": {
			broken: func(_ *testing.T, _ *deployment, p *idptest.Idp) { p.Subject = "yun@contoso.example" },
			mended: func(_ *testing.T, _ *deployment, p *idptest.Idp) { p.Subject = "yun-at-entra" },
		},
		"a subject with a space after it": {
			broken: func(_ *testing.T, _ *deployment, p *idptest.Idp) { p.Subject = "yun-at-entra " },
			mended: func(_ *testing.T, _ *deployment, p *idptest.Idp) { p.Subject = "yun-at-entra" },
		},
		"an `oid` that is no GUID, where the connection names people by it": {
			broken: func(t *testing.T, d *deployment, p *idptest.Idp) {
				d.namesBy(t, "entra", "oid")
				p.Subject = erinSub
				p.Claims["oid"] = "yun-at-entra"
			},
			mended: func(_ *testing.T, _ *deployment, p *idptest.Idp) { p.Claims["oid"] = erinOid },
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			p := idptest.New(t, "login-app")
			d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Enrolling() })
			d.connect(t, p, "entra")

			p.Claims = map[string]any{"email": "yun@contoso.example", "email_verified": true, "name": "Yun"}
			the.broken(t, d, p)

			before := d.aliases(t)
			for i, challenge := range []string{"c1", "c2", "c3"} {
				d.hydra.raise(challenge, "contoso-web")
				res := d.through(t, d.browser(t), challenge, "entra")
				x.Equal(http.StatusBadGateway, res.StatusCode, "try %d: the connection's failure is this app's to answer", i+1)
			}
			x.Equal(before, d.aliases(t), "a sign-in that was refused left somebody behind")
			_, told := d.hydra.said()
			x.False(told, "hydra was told about a sign-in that was refused")

			// Mended, they arrive once, and by their own name.
			the.mended(t, d, p)
			d.hydra.raise("c4", "contoso-web")
			res := d.through(t, d.browser(t), "c4", "entra")
			x.Equal(http.StatusSeeOther, res.StatusCode)

			after := append(append([]string{}, before...), "yun")
			sort.Strings(after)
			x.Equal(after, d.aliases(t))
		})
	}
}
