package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestAnOperatorEditsHowACustomersPeopleArrive is `Host.Update`,
// `MailDomain.Update` and `Connection.Update` (#2): what each row says about
// itself, under the version read -- and never the name, which is the row: a
// host is what a tenant is resolved through, a domain what an address is
// routed by, a connection what `Identity.provider` points at.
func TestAnOperatorEditsHowACustomersPeopleArrive(t *testing.T) {
	x := require.New(t)
	s, c, out := adminDeployment(t, nil)
	conn, as := adminPort(t, s, c, out)

	tn, err := app.NewTenantServiceClient(conn).Add(as, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: tn.GetId()}.Build()

	t.Run("a host's note, and not its name", func(t *testing.T) {
		x := require.New(t)
		hosts := app.NewHostServiceClient(conn)

		h, err := hosts.Add(as, app.HostAddRequest_builder{Tenant: at, Name: "contoso.example.com", Desc: "staging"}.Build())
		x.NoError(err)
		ref := app.HostRef_builder{Id: h.GetId()}.Build()

		v, err := hosts.Update(as, app.HostUpdateRequest_builder{Ref: ref, DateUpdated: h.GetDateUpdated(), Desc: ptr("production")}.Build())
		x.NoError(err)
		x.Equal("production", v.GetDesc())
		x.Equal("contoso.example.com", v.GetName(), "an update touched the name")

		_, err = hosts.Update(as, app.HostUpdateRequest_builder{Ref: ref, DateUpdated: h.GetDateUpdated(), Desc: ptr("stale")}.Build())
		x.NotEqual(codes.OK, status.Code(err), "a stale version was applied")
	})

	t.Run("where a domain routes", func(t *testing.T) {
		x := require.New(t)
		domains := app.NewMailDomainServiceClient(conn)

		d, err := domains.Add(as, app.MailDomainAddRequest_builder{Tenant: at, Name: "contoso.com"}.Build())
		x.NoError(err)
		ref := app.MailDomainRef_builder{Id: d.GetId()}.Build()

		v, err := domains.Update(as, app.MailDomainUpdateRequest_builder{Ref: ref, DateUpdated: d.GetDateUpdated(), Provider: ptr("entra")}.Build())
		x.NoError(err)
		x.Equal("entra", v.GetProvider())
		x.Equal("contoso.com", v.GetName())

		// Given empty, it is routed nowhere again -- the same thing empty means
		// on `Add` -- and a note left out is left alone.
		w, err := domains.Update(as, app.MailDomainUpdateRequest_builder{Ref: ref, DateUpdated: v.GetDateUpdated(), Desc: ptr("known")}.Build())
		x.NoError(err)
		x.Equal("entra", w.GetProvider(), "a provider that was not named was dropped")
		z, err := domains.Update(as, app.MailDomainUpdateRequest_builder{Ref: ref, DateUpdated: w.GetDateUpdated(), Provider: ptr("")}.Build())
		x.NoError(err)
		x.Equal("", z.GetProvider())
		x.Equal("known", z.GetDesc())
	})

	t.Run("a provider's configuration, and not its name", func(t *testing.T) {
		x := require.New(t)
		connections := app.NewConnectionServiceClient(conn)

		p, err := connections.Add(as, app.ConnectionAddRequest_builder{
			Tenant: at, Name: "entra", Issuer: "https://login.example/v1", ClientId: "old", Scopes: []string{"email"},
		}.Build())
		x.NoError(err)
		ref := app.ConnectionRef_builder{Id: p.GetId()}.Build()

		v, err := connections.Update(as, app.ConnectionUpdateRequest_builder{
			Ref: ref, DateUpdated: p.GetDateUpdated(),
			Issuer: ptr("https://login.example/v2"), ClientId: ptr("new"), Scopes: []string{"email", "profile"}, SecretRef: ptr("env:CONTOSO_ENTRA"),
		}.Build())
		x.NoError(err)
		x.Equal("https://login.example/v2", v.GetIssuer())
		x.Equal("new", v.GetClientId())
		x.Equal([]string{"email", "profile"}, v.GetScopes())
		x.Equal("env:CONTOSO_ENTRA", v.GetSecretRef())
		x.Equal("entra", v.GetName(), "an update touched the name every identity points at")

		w, err := connections.Update(as, app.ConnectionUpdateRequest_builder{Ref: ref, DateUpdated: v.GetDateUpdated(), Desc: ptr("staff")}.Build())
		x.NoError(err)
		x.Equal([]string{"email", "profile"}, w.GetScopes(), "scopes were dropped by an update that did not name them")
		x.Equal("new", w.GetClientId())
	})
}
