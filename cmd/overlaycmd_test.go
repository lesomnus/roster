package cmd_test

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// TestTheTerminalReachesEveryOverlayVerb is D58 kept: a method an overlay
// added is a command, or a console can do something a terminal cannot. The
// seven that arrived with the two UIs, each called over the wire as a
// customer's person holding a key that names them -- walled and gated like
// any caller -- with the request as JSON the way the generated verbs take it.
func TestTheTerminalReachesEveryOverlayVerb(t *testing.T) {
	const (
		tenantUpdate     = "/roster.TenantService/Update"
		hostUpdate       = "/roster.HostService/Update"
		domainUpdate     = "/roster.MailDomainService/Update"
		connectionUpdate = "/roster.ConnectionService/Update"
		reaches          = "/roster.HolderService/Reaches"
		search           = "/roster.HolderService/Search"
		verify           = "/roster.EmailService/Verify"
		confirm          = "/roster.EmailService/Confirm"
	)

	b := cliUp(t, tenantUpdate, hostUpdate, domainUpdate, connectionUpdate, reaches, search, verify, confirm)
	ctx := t.Context()
	at := app.TenantRef_builder{Id: b.Tenant.GetId()}.Build()
	id := func(v []byte) string {
		k, err := pdid.From(v)
		require.NoError(t, err)
		return k.String()
	}
	version := func(v interface{ GetDateUpdated() *timestamppb.Timestamp }) string {
		return v.GetDateUpdated().AsTime().Format(time.RFC3339Nano)
	}

	t.Run("tenant update", func(t *testing.T) {
		x := require.New(t)
		tn, err := b.Server.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: at}.Build())
		x.NoError(err)

		out, err := cliRun(t, &b.Hers, "tenant", "update", "@newco",
			fmt.Sprintf(`{"date_updated":%q,"name":"Newco Ltd"}`, version(tn)))
		x.NoError(err, out)

		v, err := b.Server.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: at}.Build())
		x.NoError(err)
		x.Equal("Newco Ltd", v.GetName())
		x.Equal("newco", v.GetAlias())
	})

	t.Run("host update", func(t *testing.T) {
		x := require.New(t)
		h, err := b.Server.Ungated.Host().Add(ctx, app.HostAddRequest_builder{Tenant: at, Name: "newco.example.com"}.Build())
		x.NoError(err)

		out, err := cliRun(t, &b.Hers, "host", "update",
			id(h.GetId()), fmt.Sprintf(`{"date_updated":%q,"desc":"production"}`, version(h)))
		x.NoError(err, out)

		v, err := b.Server.Ungated.Host().Get(ctx, app.HostGetRequest_builder{Ref: app.HostRef_builder{Id: h.GetId()}.Build()}.Build())
		x.NoError(err)
		x.Equal("production", v.GetDesc())
	})

	t.Run("mail-domain update", func(t *testing.T) {
		x := require.New(t)
		d, err := b.Server.Ungated.MailDomain().Add(ctx, app.MailDomainAddRequest_builder{Tenant: at, Name: "newco.example"}.Build())
		x.NoError(err)

		out, err := cliRun(t, &b.Hers, "mail-domain", "update",
			id(d.GetId()), fmt.Sprintf(`{"date_updated":%q,"provider":"entra"}`, version(d)))
		x.NoError(err, out)

		v, err := b.Server.Ungated.MailDomain().Get(ctx, app.MailDomainGetRequest_builder{Ref: app.MailDomainRef_builder{Id: d.GetId()}.Build()}.Build())
		x.NoError(err)
		x.Equal("entra", v.GetProvider())
	})

	t.Run("connection update", func(t *testing.T) {
		x := require.New(t)
		c, err := b.Server.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
			Tenant: at, Name: "entra", Issuer: "https://login.example/v1", ClientId: "old",
		}.Build())
		x.NoError(err)

		out, err := cliRun(t, &b.Hers, "connection", "update",
			id(c.GetId()), fmt.Sprintf(`{"date_updated":%q,"client_id":"new"}`, version(c)))
		x.NoError(err, out)

		v, err := b.Server.Ungated.Connection().Get(ctx, app.ConnectionGetRequest_builder{Ref: app.ConnectionRef_builder{Id: c.GetId()}.Build()}.Build())
		x.NoError(err)
		x.Equal("new", v.GetClientId())
		x.Equal("entra", v.GetName())
	})

	t.Run("holder reaches", func(t *testing.T) {
		x := require.New(t)
		out, err := cliRun(t, &b.Hers, "holder", "reaches", "@newco/alice")
		x.NoError(err, out)
		x.Contains(out, reaches, "what alice reaches did not list the method she called this with")
	})

	t.Run("holder search", func(t *testing.T) {
		x := require.New(t)
		out, err := cliRun(t, &b.Hers, "holder", "search", `{"q":"ali"}`)
		x.NoError(err, out)
		x.Contains(out, "alice")
		x.NotContains(out, "bob")
	})

	t.Run("email verify, then confirm", func(t *testing.T) {
		x := require.New(t)
		e, err := b.Server.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder: app.HolderRef_builder{Id: b.Alice.GetId()}.Build(), Address: "alice@newco.example",
		}.Build())
		x.NoError(err)

		out, err := cliRun(t, &b.Hers, "email", "verify", "-o", "protojson", id(e.GetId()))
		x.NoError(err, out)
		token := regexp.MustCompile(`rl_[A-Za-z0-9_-]+`).FindString(out)
		x.NotEmpty(token, "no link in: %s", out)

		out, err = cliRun(t, &b.Hers, "email", "confirm", fmt.Sprintf(`{"token":%q}`, token))
		x.NoError(err, out)

		v, err := b.Server.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{Ref: app.EmailRef_builder{Id: e.GetId()}.Build()}.Build())
		x.NoError(err)
		x.NotNil(v.GetDateVerified(), "confirmed from a terminal and not stamped")
	})
}
