package login_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/internal/idptest"
)

// A provider registration made before the callback migration must refuse the
// candidate even though its own nonce/PKCE/callback tests pass. Updating the
// external registration first admits the same binary and the expected holder.
func TestLoginNeedsItsCallbackRegisteredBeforeTheUpgrade(t *testing.T) {
	x := require.New(t)
	d := serve(t)
	p := idptest.New(t, "login-app")
	d.connect(t, p, "entra")
	d.identity(t, "entra", "erin-at-entra")
	p.Subject = "erin-at-entra"
	p.RegisterRedirects(d.app.URL + "/callback")
	d.hydra.raise("c1", "contoso-web")
	b := d.browser(t)

	res, err := b.Get(d.app.URL + "/provider?login_challenge=c1&connection=entra")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusFound, res.StatusCode)
	res, err = b.Get(res.Header.Get("Location"))
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusBadRequest, res.StatusCode)
	x.Empty(res.Header.Get("Location"))
	x.Nil(p.Asked())
	x.Zero(p.Exchanges.Load())
	_, accepted := d.hydra.said()
	x.False(accepted, "Hydra accepted a sign-in the provider refused")

	// Literal preregistered paths, independently of the app's Location header.
	// Keep the old URI too: rolling back should not need an Entra write.
	p.RegisterRedirects(d.app.URL+"/callback", d.app.URL+"/callback/contoso/entra")
	d.hydra.raise("c2", "contoso-web")
	res = d.through(t, b, "c2", "entra")
	x.Equal(http.StatusSeeOther, res.StatusCode)
	subject, _ := d.hydra.told()
	x.Equal(d.who["contoso"].String(), subject)
	x.EqualValues(1, p.Exchanges.Load())

	res, err = b.Get(d.app.URL + "/consent?consent_challenge=c2")
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusSeeOther, res.StatusCode)
	_, claims := d.hydra.told()
	x.Equal("erin", claims["preferred_username"])
}
