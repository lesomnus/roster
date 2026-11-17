package login_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/internal/idptest"
	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestATokensOidIsTheSamePersonInEitherSpelling: a sign-in looks for the
// subject its token carried, and at a connection that names its people by
// `oid` that is a GUID, which a provider may write in capitals as readily as in
// small letters. The row was found only in the spelling it was written in, so
// somebody a directory provisioned by one was a stranger arriving by the
// other -- refused, or under `enrolling` made a second time. They are the
// person who holds that GUID, and no second person is made of them.
func TestATokensOidIsTheSamePersonInEitherSpelling(t *testing.T) {
	x := require.New(t)
	p := idptest.New(t, "login-app")
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Enrolling() })
	erin := d.who["contoso"]

	d.connect(t, p, "entra")
	d.namesBy(t, "entra", "oid")
	_, err := d.s.Ungated.Identity().Add(t.Context(), rstr.IdentityAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: erin.Bytes()}.Build(), Provider: "entra", Subject: erinOid,
	}.Build())
	x.NoError(err)

	p.Subject = erinSub
	for i, said := range []string{erinOid, strings.ToUpper(erinOid)} {
		p.Claims = map[string]any{"oid": said}

		challenge := []string{"c1", "c2"}[i]
		d.hydra.raise(challenge, "contoso-web")
		res := d.through(t, d.browser(t), challenge, "entra")
		x.Equal(http.StatusSeeOther, res.StatusCode, "a token that says %s", said)

		subject, _ := d.hydra.told()
		x.Equal(erin.String(), subject, "a token that says %s was somebody else", said)
		x.Equal(erinOid, d.subjectOf(t, erin))
	}
}
