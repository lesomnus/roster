package login_test

import (
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/internal/idptest"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestAFlowThatNamesAConnectionGoesStraightThere: a product whose sign-in
// screen is its own has already asked which provider, and says so in the
// authorization request (`connection=`); the first screen is then a redirect
// to that provider and not a page asking the same question again.
func TestAFlowThatNamesAConnectionGoesStraightThere(t *testing.T) {
	x := require.New(t)
	entra := idptest.New(t, "login-app")
	google := idptest.New(t, "login-app")
	d := serve(t)
	d.connect(t, entra, "entra")
	d.connect(t, google, "google")

	_, err := d.s.Ungated.Identity().Add(t.Context(), rstr.IdentityAddRequest_builder{
		Holder:   rstr.HolderRef_builder{Id: d.who["contoso"].Bytes()}.Build(),
		Provider: "google",
		Subject:  "erin-at-google",
	}.Build())
	x.NoError(err)
	google.Subject = "erin-at-google"

	first := func(t *testing.T, b *http.Client, challenge string) *http.Response {
		t.Helper()
		res, err := b.Get(d.app.URL + "/login?login_challenge=" + challenge)
		require.NoError(t, err)
		t.Cleanup(func() { res.Body.Close() })

		return res
	}

	t.Run("to the one it names, and on to the end", func(t *testing.T) {
		x := require.New(t)
		b := d.browser(t)
		d.hydra.raise("c1", "contoso-web")
		d.hydra.asking["c1"] = url.Values{"connection": {"google"}}

		res := first(t, b, "c1")
		x.Equal(http.StatusSeeOther, res.StatusCode)
		to, err := url.Parse(res.Header.Get("location"))
		x.NoError(err)
		x.Equal("/provider", to.Path)
		x.Equal("google", to.Query().Get("connection"))
		x.Equal("c1", to.Query().Get("login_challenge"))

		// Which is the button's own route from here: the directory, the
		// callback, and Hydra told who it is.
		res, err = b.Get(d.app.URL + to.String())
		x.NoError(err)
		defer res.Body.Close()
		x.Equal(http.StatusFound, res.StatusCode)
		x.Contains(res.Header.Get("location"), google.URL, "the browser went to another provider than the one named")

		res, err = b.Get(res.Header.Get("location"))
		x.NoError(err)
		defer res.Body.Close()
		back, err := b.Get(res.Header.Get("location"))
		x.NoError(err)
		defer back.Body.Close()
		x.Equal(http.StatusSeeOther, back.StatusCode)

		subject, _ := d.hydra.told()
		x.Equal(d.who["contoso"].String(), subject)
	})

	// A hint, not an order: a name the tenant has no connection by is the
	// page, which lists the ones it has.
	t.Run("a name the tenant has no connection by draws the page", func(t *testing.T) {
		x := require.New(t)
		d.hydra.raise("c2", "contoso-web")
		d.hydra.asking["c2"] = url.Values{"connection": {"okta"}}

		res := first(t, d.browser(t), "c2")
		x.Equal(http.StatusOK, res.StatusCode)
		body, err := io.ReadAll(res.Body)
		x.NoError(err)
		x.Equal("the form", string(body), "the page was not drawn")
	})

	t.Run("and so does naming none", func(t *testing.T) {
		x := require.New(t)
		d.hydra.raise("c3", "contoso-web")

		res := first(t, d.browser(t), "c3")
		x.Equal(http.StatusOK, res.StatusCode)
		body, err := io.ReadAll(res.Body)
		x.NoError(err)
		x.Equal("the form", string(body), "the page was not drawn")
	})
}
