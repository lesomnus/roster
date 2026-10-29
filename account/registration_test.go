package account_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
)

func TestAccountNeedsItsCallbackRegisteredBeforeTheUpgrade(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())
	d.idp.Subject = "3001"
	d.idp.Claims = map[string]any{"email": "erin@contoso.com", "email_verified": true}
	d.idp.RegisterRedirects(d.app.URL + "/callback")
	b := d.browser(t, "contoso.test")

	code, _ := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
	x.Equal(http.StatusBadRequest, code, "the provider accepted an unregistered callback")
	x.Nil(d.idp.Asked())
	x.Zero(d.idp.Exchanges.Load())
	code, _ = b.rpc(t, "/roster.MeService/Get", `{}`)
	x.Equal(http.StatusUnauthorized, code, "a refused login created a session")

	d.idp.RegisterRedirects(d.app.URL+"/callback", d.app.URL+"/callback/contoso/example")
	code, body := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
	x.Equal(http.StatusOK, code, body)
	x.EqualValues(1, d.idp.Exchanges.Load())
	code, body = b.rpc(t, "/roster.MeService/Get", `{}`)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, `"erin"`, "the protected API answered for another person")

	b.host = "fabrikam.test"
	code, _ = b.rpc(t, "/roster.MeService/Get", `{}`)
	x.Equal(http.StatusUnauthorized, code, "contoso's session reached another tenant")
}
