package account_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/roster/account"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/keys"
)

// TestAnAppAsksToActForSomebodyAndTheyApproveIt (#567).
//
// An app writes for a person through a delegation that person approved at
// roster, and nothing let an app ask for one: a terminal's sign-in minted a
// key, and `delegate` was read as nothing. Seen as: begun with `"delegate":
// true` and no app, it was answered a code; approved, the poll answered a key.
//
// With `delegate`, what is asked for is a delegation issued to `app`, which
// is then required and refused at once without one; the person approving is
// told it is that app acting as them; their yes mints it with
// `DelegationService/Issue`, as them, held to what they may call; and the
// poll answers the token once, which the app presents beside its own key and
// is answered as her. With `account.terminal` off it is told so at once, as a
// terminal is; and a front door handed no life (`delegation.life`, which the
// command always hands it) offers no delegation, told so at once, and still
// offers a key.
func TestAnAppAsksToActForSomebodyAndTheyApproveIt(t *testing.T) {
	x := require.New(t)

	d := serve(t, account.Invited(), terminal, lived(core.DelegationLife))
	mints(t, d)
	ctx := context.Background()
	tenant := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	// Loft, an app of Contoso's with a key of roster's of its own and nothing
	// bound to it; and erin, who may list roles.
	v, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: tenant, Alias: "loft"}.Build())
	x.NoError(err)
	loft := v.GetId()
	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	_, err = d.ungated.ApiKey().Add(ctx, rstr.ApiKeyAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: loft}.Build(), Alias: "loft", Secret: sum, Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)
	r, err := d.ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: tenant, Alias: "lists-roles", Methods: []string{rstr.RoleService_List_FullMethodName},
	}.Build())
	x.NoError(err)
	_, err = d.ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role: rstr.RoleRef_builder{Id: r.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: d.erin}.Build(),
	}.Build())
	x.NoError(err)

	app := d.browser(t, "contoso.test")
	her := d.browser(t, "contoso.test")
	code, body := her.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	begin := func(req string) (int, string, string, string) {
		code, body := app.do(t, http.MethodPost, "/device/begin", req, nil)
		var v struct {
			DeviceCode string `json:"device_code"`
			UserCode   string `json:"user_code"`
		}
		_ = json.Unmarshal([]byte(body), &v)

		return code, body, v.DeviceCode, v.UserCode
	}

	t.Run("begun without an app, refused at once", func(t *testing.T) {
		code, body, _, _ := begin(`{"name":"loft","delegate":true,"allow":["/roster.RoleService/List"]}`)
		require.Equal(t, http.StatusBadRequest, code, body)
		require.Contains(t, body, "app:")
	})

	t.Run("approved, the poll answers a delegation the app acts as her with", func(t *testing.T) {
		x := require.New(t)

		code, body, device, user := begin(`{"name":"loft","app":"loft","delegate":true,"allow":["/roster.RoleService/List"]}`)
		x.Equal(http.StatusOK, code, body)

		// The page is told it is the app acting as her, and not a key.
		code, body = her.do(t, http.MethodGet, "/device/pending?user_code="+user, "", nil)
		x.Equal(http.StatusOK, code, body)
		var shown struct {
			App      string   `json:"app"`
			Methods  []string `json:"methods"`
			Delegate bool     `json:"delegate"`
			Life     int64    `json:"life"`
		}
		x.NoError(json.Unmarshal([]byte(body), &shown))
		x.True(shown.Delegate, "the person approving was not told it is the app acting as them: %s", body)
		x.Equal("loft", shown.App)
		x.Equal(int64(core.DelegationLife.Seconds()), shown.Life, "the person approving was not told how long: %s", body)

		before := time.Now()
		code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, nil)
		x.Equal(http.StatusOK, code, body)
		after := time.Now()
		code, body = asked(t, app, device)
		x.Equal(http.StatusOK, code, body)
		var got struct{ Token string }
		x.NoError(json.Unmarshal([]byte(body), &got))
		x.True(strings.HasPrefix(got.Token, keys.PrefixDelegation), "the poll answered something other than a delegation")

		// Erin's, issued to Loft.
		row, err := d.ungated.Delegation().Get(ctx, rstr.DelegationGetRequest_builder{
			Ref: rstr.DelegationRef_builder{Secret: keys.Sum(got.Token)}.Build(),
			Select: rstr.DelegationSelect_builder{
				IssuedTo: new(true), Methods: new(true), DateExpired: new(true), Holder: rstr.HolderSelect_builder{}.Build(),
			}.Build(),
		}.Build())
		x.NoError(err)

		// It ends as long after the yes as she was told.
		life := time.Duration(shown.Life) * time.Second
		end := row.GetDateExpired().AsTime()
		x.False(end.Before(before.Add(life).Truncate(time.Millisecond)), "ends at %s", end)
		x.False(end.After(after.Add(life)), "ends at %s", end)
		x.Equal(d.erin, row.GetHolder().GetId())
		x.Equal(loft, row.GetIssuedTo())
		x.Equal([]string{rstr.RoleService_List_FullMethodName}, row.GetMethods())

		// And Loft, presenting it beside its own key, is answered as her.
		conn, err := grpc.NewClient(d.grpc, grpc.WithTransportCredentials(insecure.NewCredentials()))
		x.NoError(err)
		t.Cleanup(func() { conn.Close() })
		as := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token, keys.HeaderActing, got.Token)
		_, err = rstr.NewRoleServiceClient(conn).List(as, rstr.RoleListRequest_builder{}.Build())
		x.NoError(err, "Loft was not answered as erin")

		// Once: a second poll finds nothing.
		code, body = asked(t, app, device)
		x.Equal(http.StatusBadRequest, code, body)
	})

	t.Run("asking more than she may call is refused, in a delegation's words", func(t *testing.T) {
		x := require.New(t)

		code, body, _, user := begin(`{"name":"loft","app":"loft","delegate":true,"allow":["/roster.HolderService/Erase"]}`)
		x.Equal(http.StatusOK, code, body)
		code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, nil)
		x.Equal(http.StatusForbidden, code, body)
		x.Contains(body, "act as you")
	})

	t.Run("a front door told a longer life than roster's is refused at the yes, and nothing is minted", func(t *testing.T) {
		x := require.New(t)

		long := serve(t, account.Invited(), terminal, lived(2*core.DelegationLife))
		mints(t, long)
		lft, err := long.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: long.contoso.Bytes()}.Build(), Alias: "loft",
		}.Build())
		x.NoError(err)
		term, erin := long.browser(t, "contoso.test"), long.browser(t, "contoso.test")
		code, body := erin.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
			func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
		x.Equal(http.StatusNoContent, code, body)

		code, body = term.do(t, http.MethodPost, "/device/begin", `{"name":"loft","app":"loft","delegate":true,"allow":["/roster.MeService/Get"]}`, nil)
		x.Equal(http.StatusOK, code, body)
		var v struct {
			DeviceCode string `json:"device_code"`
			UserCode   string `json:"user_code"`
		}
		x.NoError(json.Unmarshal([]byte(body), &v))

		code, body = erin.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+v.UserCode+`"}`, nil)
		x.Equal(http.StatusConflict, code, body)
		x.Contains(body, "longer than roster allows")

		ds, err := long.ungated.Delegation().List(ctx, rstr.DelegationListRequest_builder{
			Filters: []*rstr.DelegationFilter{rstr.DelegationFilter_builder{Holder: rstr.HolderRef_builder{Id: long.erin}.Build()}.Build()},
		}.Build())
		x.NoError(err)
		for _, d := range ds.GetItems() {
			x.NotEqual(lft.GetId(), d.GetIssuedTo(), "a delegation was minted for Loft")
		}
		code, body = asked(t, term, v.DeviceCode)
		x.Equal(http.StatusBadRequest, code, body)
		x.Contains(body, "authorization_pending", "a delegation was handed over")
	})

	t.Run("a front door told no life offers no delegation, and says so at once", func(t *testing.T) {
		x := require.New(t)

		none := serve(t, account.Invited(), terminal)
		b := none.browser(t, "contoso.test")
		code, body := b.do(t, http.MethodPost, "/device/begin",
			`{"name":"loft","app":"loft","delegate":true,"allow":["/roster.RoleService/List"]}`, nil)
		x.Equal(http.StatusNotImplemented, code, body)
		x.Contains(body, "delegation.life")

		// A key is still offered.
		code, body = b.do(t, http.MethodPost, "/device/begin", `{"name":"laptop","allow":["/roster.MeService/Get"]}`, nil)
		x.Equal(http.StatusOK, code, body)
	})

	t.Run("with account.terminal off, told so at once", func(t *testing.T) {
		off := serve(t, account.Invited(), lived(core.DelegationLife))
		code, body := off.browser(t, "contoso.test").do(t, http.MethodPost, "/device/begin",
			`{"name":"loft","app":"loft","delegate":true,"allow":["/roster.RoleService/List"]}`, nil)
		require.Equal(t, http.StatusNotImplemented, code, body)
	})
}

// lived is a front door told how long a delegation lives, as the command tells
// it what the deployment's file says.
func lived(v time.Duration) func(*account.Config) {
	return func(c *account.Config) { c.DelegationLife = v }
}
