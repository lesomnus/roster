package account_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/internal/idptest"
	rstr "github.com/lesomnus/roster/rstr"
)

// Model the observed Entra response: ordinary login succeeds, but max_age=0
// returns a signed token whose expiry is already past. The real Account App
// callback and protected API must also work for its explicit again=1 link.
// This synthetic provider regression does not replace live, human Entra login.
func TestMicrosoftReauthenticationDoesNotRequestAnExpiredToken(t *testing.T) {
	m := idptest.New(t, clientId)
	d := serve(t, account.Invited(), func(c *account.Config) { c.Microsoft = m.URL })
	ctx := t.Context()
	_, err := d.ungated.Tenant().Patch(ctx, rstr.TenantPatchRequest_builder{
		Ref:    rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		Config: rstr.TenantConfig_builder{Leave: true}.Build(), DateUpdatedForce: proto.Bool(true),
	}.Build())
	require.NoError(t, err)
	_, err = d.ungated.Connection().Add(ctx, rstr.ConnectionAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		Name:   "microsoft", Issuer: m.URL, ClientId: clientId,
		Scopes: []string{"email", "profile"}, SecretRef: "env:EXAMPLE_SECRET",
	}.Build())
	require.NoError(t, err)
	_, err = d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: d.erin}.Build(), Provider: "microsoft", Subject: "erin-at-microsoft",
	}.Build())
	require.NoError(t, err)
	m.Subject = "erin-at-microsoft"
	m.RegisterRedirects(d.app.URL + "/callback/contoso/microsoft")
	when := time.Now()
	expired := false
	m.Answer = func(asked url.Values) map[string]any {
		claims := map[string]any{"exp": time.Now().Add(time.Hour).Unix()}
		if expired || asked.Get("max_age") == "0" {
			claims["exp"] = time.Now().Add(-time.Minute).Unix()
		}
		if asked.Has("claims") && !when.IsZero() {
			claims["auth_time"] = when.Unix()
		}
		return claims
	}
	login := func(t *testing.T, again bool) (*browser, int) {
		t.Helper()
		path := "/login?connection=microsoft"
		if again {
			path += "&again=1"
		}
		b := d.browser(t, "contoso.test")
		code, _ := b.do(t, http.MethodGet, path, "", nil)
		return b, code
	}
	for _, again := range []bool{false, true} {
		b, code := login(t, again)
		require.Equal(t, http.StatusOK, code)
		code, body := b.rpc(t, "/roster.MeService/Get", `{}`)
		require.Equal(t, http.StatusOK, code, body)
		require.Contains(t, body, `"erin"`)
		asked := m.Asked()
		require.False(t, asked.Has("max_age"))
		if again {
			require.Equal(t, "login", asked.Get("prompt"))
			require.JSONEq(t, `{"id_token":{"auth_time":{"essential":true}}}`, asked.Get("claims"))
		} else {
			require.False(t, asked.Has("prompt"))
			require.False(t, asked.Has("claims"))
		}
	}

	t.Run("an expired token still creates no session", func(t *testing.T) {
		expired = true
		t.Cleanup(func() { expired = false })
		b, code := login(t, true)
		require.Equal(t, http.StatusBadRequest, code)
		code, _ = b.rpc(t, "/roster.MeService/Get", `{}`)
		require.Equal(t, http.StatusUnauthorized, code)
	})
	for _, c := range []struct {
		name string
		at   time.Time
		want int
	}{
		{"no auth time is not a recent authentication", time.Time{}, http.StatusBadRequest},
		{"an old authentication is not recent", time.Now().Add(-time.Hour), http.StatusBadRequest},
		{"the provider actually authenticated them recently", time.Now(), http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			when = c.at
			b, code := login(t, true)
			require.Equal(t, http.StatusOK, code)
			code, body := b.rpc(t, "/roster.MeService/Leave", `{"confirm":"erin"}`)
			require.Equal(t, c.want, code, body)
		})
	}
}
