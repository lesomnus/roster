package account_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/cli"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// The terminal's sign-in names the app (#567).
//
// A key is for one app, and the terminal's sign-in minted a key of roster's
// and nothing else: `foo auth login`, or an app's page signing in the person in
// front of it, could not be handed a key for that app, and the person
// approving was not shown which app it was for.

// appOfTerminals is Loft, an app of Contoso's, holding a key of roster's of its
// own allowed `Introspect`, and erin holding `/loft.AssetService/Get` at Loft.
func appOfTerminals(t *testing.T, d *deployment) (loft []byte, token string) {
	t.Helper()
	x := require.New(t)
	ctx := context.Background()
	tenant := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	v, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: tenant, Alias: "loft"}.Build())
	x.NoError(err)
	loft = v.GetId()

	bind := func(who []byte, alias string, of []byte, methods ...string) {
		req := rstr.RoleAddRequest_builder{Tenant: tenant, Alias: alias, Methods: methods}
		if of != nil {
			req.App = rstr.HolderRef_builder{Id: of}.Build()
		}
		r, err := d.ungated.Role().Add(ctx, req.Build())
		x.NoError(err)
		_, err = d.ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
			Role: rstr.RoleRef_builder{Id: r.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: who}.Build(),
		}.Build())
		x.NoError(err)
	}
	bind(d.erin, "loft-assets", loft, "/loft.AssetService/Get")
	bind(loft, "loft-introspects", nil, "/payday.TokenService/Introspect")

	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	_, err = d.ungated.ApiKey().Add(ctx, rstr.ApiKeyAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: loft}.Build(), Alias: "loft", Secret: sum,
		Methods: []string{"/payday.TokenService/Introspect"},
	}.Build())
	x.NoError(err)

	return loft, token
}

// TestATerminalIsSignedInForAnApp.
//
// A terminal begun naming `app` is shown to the person approving with the app
// beside its name and methods, and the key minted is for that app: answered
// to that app at `Introspect`, and no credential at roster. Approved by
// somebody who does not hold what was asked at that app, it is refused as a
// terminal asking more than its approver holds is.
func TestATerminalIsSignedInForAnApp(t *testing.T) {
	x := require.New(t)

	d := serve(t, account.Invited(), terminal)
	mints(t, d)
	loft, asLoft := appOfTerminals(t, d)
	term := d.browser(t, "contoso.test")
	her := d.browser(t, "contoso.test")

	code, body := her.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	begin := func(allow string) (device, user string) {
		code, body := term.do(t, http.MethodPost, "/device/begin",
			`{"name":"laptop","app":"loft","allow":["`+allow+`"]}`, nil)
		x.Equal(http.StatusOK, code, body)
		var v struct {
			DeviceCode string `json:"device_code"`
			UserCode   string `json:"user_code"`
		}
		x.NoError(json.Unmarshal([]byte(body), &v))

		return v.DeviceCode, v.UserCode
	}

	device, user := begin("/loft.AssetService/Get")

	// The page is told the app, beside the name and the methods.
	code, body = her.do(t, http.MethodGet, "/device/pending?user_code="+user, "", nil)
	x.Equal(http.StatusOK, code, body)
	var shown struct {
		Name    string   `json:"name"`
		App     string   `json:"app"`
		Methods []string `json:"methods"`
	}
	x.NoError(json.Unmarshal([]byte(body), &shown))
	x.Equal("loft", shown.App, "the person approving was not shown the app: %s", body)

	code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, nil)
	x.Equal(http.StatusOK, code, body)
	code, body = asked(t, term, device)
	x.Equal(http.StatusOK, code, body)
	var got struct{ Token string }
	x.NoError(json.Unmarshal([]byte(body), &got))

	// The key is for Loft.
	k, err := d.ungated.ApiKey().Get(context.Background(), rstr.ApiKeyGetRequest_builder{
		Ref:    rstr.ApiKeyRef_builder{Secret: keys.Sum(got.Token)}.Build(),
		Select: rstr.ApiKeySelect_builder{App: rstr.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(loft, k.GetApp().GetId(), "the key is not for the app the terminal named")

	// Answered to Loft, and no credential at roster.
	conn, err := grpc.NewClient(d.grpc, grpc.WithTransportCredentials(insecure.NewCredentials()))
	x.NoError(err)
	t.Cleanup(func() { conn.Close() })
	bearing := func(token string) context.Context {
		return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
	}
	res, err := pdpb.NewTokenServiceClient(conn).Introspect(bearing(asLoft), pdpb.TokenIntrospectRequest_builder{Token: got.Token}.Build())
	x.NoError(err, "Loft was not answered about a key for Loft")
	x.Equal(d.erin, res.GetId())
	_, err = rstr.NewMeServiceClient(conn).Get(bearing(got.Token), rstr.MeGetRequest_builder{}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err), "a key for Loft was a credential at roster")

	// Asking at Loft what she does not hold there is refused as today.
	_, user = begin("/loft.AssetService/Erase")
	code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, nil)
	x.Equal(http.StatusForbidden, code, body)
	x.Contains(body, "more than you hold")
}

// TestTheCommandSignsATerminalInForAnApp is `roster sign-in --app`: the
// command names the app in what it begins with, and the key it is handed is
// for that app.
func TestTheCommandSignsATerminalInForAnApp(t *testing.T) {
	x := require.New(t)

	d := serve(t, account.Invited(), terminal)
	mints(t, d)
	loft, _ := appOfTerminals(t, d)
	her := d.browser(t, "contoso.test")
	code, body := her.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	codes := make(chan string, 4)
	said := regexp.MustCompile(`and type\s+([A-Z]{4}-[A-Z]{4})`)
	say := func(f string, a ...any) (int, error) {
		if m := said.FindStringSubmatch(fmt.Sprintf(f, a...)); m != nil {
			codes <- m[1]
		}

		return 0, nil
	}
	go func() {
		select {
		case user := <-codes:
			_, _ = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, nil)
		case <-time.After(20 * time.Second):
		}
	}()

	ctx, stop := context.WithTimeout(t.Context(), 45*time.Second)
	defer stop()
	key, err := cli.SignIn(ctx, say, &http.Client{Transport: hostIs{name: "contoso.test"}}, d.app.URL, "laptop", "loft", []string{"/loft.AssetService/Get"})
	x.NoError(err)

	k, err := d.ungated.ApiKey().Get(context.Background(), rstr.ApiKeyGetRequest_builder{
		Ref:    rstr.ApiKeyRef_builder{Secret: keys.Sum(key)}.Build(),
		Select: rstr.ApiKeySelect_builder{App: rstr.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(loft, k.GetApp().GetId(), "the command's key is not for the app it named")
}
