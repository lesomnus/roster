package account_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/roster/account"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
)

// A key asked for at a terminal carries the app's word about it, and may end.
//
// `/device/begin` took a name, an app and the methods: no scope, so a key for
// an app could not say which of the app's rows it reaches, and no end, so a
// key minted this way never ended. And a field it did not know was dropped,
// so a body that misspelled one was a key wider than was asked for.

// scopedTerminal is a terminal of Loft's begun with `body`, answering its two
// codes.
func scopedTerminal(t *testing.T, term *browser, body string) (device, user string) {
	t.Helper()
	x := require.New(t)

	code, said := term.do(t, http.MethodPost, "/device/begin", body, nil)
	x.Equal(http.StatusOK, code, said)
	var v struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	x.NoError(json.Unmarshal([]byte(said), &v))

	return v.DeviceCode, v.UserCode
}

// TestATerminalsKeyCarriesTheAppsScopeAndItsEnd.
//
// Begun with `scope` and `date_expired`, the person approving is told both,
// as they will be kept, and the key approved carries both: `Introspect`
// answers Loft the scope as it was sent and the end. Begun with neither, it
// is told the key carries no scope and does not end, as before.
func TestATerminalsKeyCarriesTheAppsScopeAndItsEnd(t *testing.T) {
	x := require.New(t)

	d := serve(t, account.Invited(), terminal)
	mints(t, d)
	_, asLoft := appOfTerminals(t, d)
	term := d.browser(t, "contoso.test")
	her := d.browser(t, "contoso.test")
	code, body := her.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	ends := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	device, user := scopedTerminal(t, term,
		`{"name":"cams","app":"loft","allow":["/loft.AssetService/Get"],"scope":"assets:01","date_expired":"`+ends.Format(time.RFC3339)+`"}`)

	code, body = her.do(t, http.MethodGet, "/device/pending?user_code="+user, "", nil)
	x.Equal(http.StatusOK, code, body)
	var shown struct {
		Scope       *string `json:"scope"`
		DateExpired string  `json:"date_expired"`
	}
	x.NoError(json.Unmarshal([]byte(body), &shown))
	x.NotNil(shown.Scope, "the person approving was not told the scope: %s", body)
	x.Equal("assets:01", *shown.Scope)
	x.Equal(ends.Format(time.RFC3339), shown.DateExpired, "the person approving was not told when it ends: %s", body)

	code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, nil)
	x.Equal(http.StatusOK, code, body)
	code, body = asked(t, term, device)
	x.Equal(http.StatusOK, code, body)
	var got struct{ Token string }
	x.NoError(json.Unmarshal([]byte(body), &got))

	conn, err := grpc.NewClient(d.grpc, grpc.WithTransportCredentials(insecure.NewCredentials()))
	x.NoError(err)
	t.Cleanup(func() { conn.Close() })
	res, err := pdpb.NewTokenServiceClient(conn).Introspect(
		metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+asLoft),
		pdpb.TokenIntrospectRequest_builder{Token: got.Token}.Build())
	x.NoError(err)
	x.Equal("assets:01", res.GetScope(), "the key does not carry the scope it was approved with")
	x.True(ends.Equal(res.GetDateExpired().AsTime()), "the key does not end when it was approved to: %v", res.GetDateExpired())

	// Neither: no scope, and no end.
	_, user = scopedTerminal(t, term, `{"name":"plain","app":"loft","allow":["/loft.AssetService/Get"]}`)
	code, body = her.do(t, http.MethodGet, "/device/pending?user_code="+user, "", nil)
	x.Equal(http.StatusOK, code, body)
	var plain map[string]any
	x.NoError(json.Unmarshal([]byte(body), &plain))
	x.Equal("", plain["scope"], "%s", body)
	x.NotContains(plain, "date_expired", "%s", body)
	code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`"}`, nil)
	x.Equal(http.StatusOK, code, body)
	k, err := d.ungated.ApiKey().Get(context.Background(), rstr.ApiKeyGetRequest_builder{
		Ref: rstr.ApiKeyRef_builder{Slug: rstr.ApiKeyRefBySlug_builder{
			Alias: z.Ptr("plain"), Holder: rstr.HolderRef_builder{Id: d.erin}.Build(),
		}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	x.Empty(k.GetScope())
	x.False(k.HasDateExpired(), "a key asked for with no end ends")
}

// TestATerminalIsRefusedWhatIssueRefuses.
//
// What the beginning takes it holds as `ApiKeyService/Issue` does, before
// anybody is asked to approve it: an end that has passed, or that is not a
// time; a scope on a key of roster's own, or past 256 bytes; and a scope or
// an end on a delegation, which carries neither. Each `400`, naming the field.
func TestATerminalIsRefusedWhatIssueRefuses(t *testing.T) {
	d := serve(t, account.Invited(), terminal, lived(core.DelegationLife))
	mints(t, d)
	term := d.browser(t, "contoso.test")

	for name, c := range map[string]struct{ body, says string }{
		"an end that has passed": {
			`{"app":"loft","allow":["/loft.AssetService/Get"],"date_expired":"2020-01-01T00:00:00Z"}`,
			"date_expired: a key cannot have already expired",
		},
		"an end that is not a time": {
			`{"app":"loft","allow":["/loft.AssetService/Get"],"date_expired":"next week"}`,
			"date_expired: ",
		},
		"a scope on a key of roster's": {
			`{"allow":["/roster.MeService/Get"],"scope":"assets:01"}`,
			"scope: this key is roster's own and for no app, so no app is there to read a scope",
		},
		"a scope past its length": {
			`{"app":"loft","allow":["/loft.AssetService/Get"],"scope":"` + strings.Repeat("a", core.MaxKeyScope+1) + `"}`,
			"scope: at most 256 bytes, and this is 257",
		},
		"a scope on a delegation": {
			`{"app":"loft","delegate":true,"allow":["/roster.GroupMembershipService/Add"],"scope":"assets:01"}`,
			"scope: a delegation carries none",
		},
		"an end on a delegation": {
			`{"app":"loft","delegate":true,"allow":["/roster.GroupMembershipService/Add"],"date_expired":"2099-01-01T00:00:00Z"}`,
			"date_expired: a delegation lasts delegation.life",
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			code, body := term.do(t, http.MethodPost, "/device/begin", c.body, nil)
			x.Equal(http.StatusBadRequest, code, body)
			x.Contains(body, c.says)
		})
	}
}

// TestATerminalNamingAFieldThatIsNoneIsRefused.
//
// A field the beginning did not know was dropped: `scopes` for `scope` began
// a terminal whose key would carry no scope, answered `200`; right is `400`,
// naming the field. And at the approval, where `denied` for `deny` was a yes
// that minted the key: `400`, naming it, and nothing minted.
func TestATerminalNamingAFieldThatIsNoneIsRefused(t *testing.T) {
	x := require.New(t)

	d := serve(t, account.Invited(), terminal)
	mints(t, d)
	appOfTerminals(t, d)
	term := d.browser(t, "contoso.test")
	her := d.browser(t, "contoso.test")
	code, body := her.do(t, http.MethodPost, "/session", `{"alias":"erin","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code, body)

	code, body = term.do(t, http.MethodPost, "/device/begin",
		`{"name":"cams","app":"loft","allow":["/loft.AssetService/Get"],"scopes":"assets:01"}`, nil)
	x.Equal(http.StatusBadRequest, code, body)
	x.Contains(body, "scopes: this request has no such field")

	device, user := scopedTerminal(t, term, `{"name":"cams","app":"loft","allow":["/loft.AssetService/Get"]}`)
	code, body = her.do(t, http.MethodPost, "/device/approve", `{"user_code":"`+user+`","denied":true}`, nil)
	x.Equal(http.StatusBadRequest, code, body)
	x.Contains(body, "denied: this request has no such field")
	x.Equal("authorization_pending", polls(t, term, device), "a refused answer minted a key")
}
