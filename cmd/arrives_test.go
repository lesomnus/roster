package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestAnOperatorSaysHowACustomersPeopleArrive is the *arrives through* panel
// of the console (`ts/src/arrives.tsx`), made as the writes it makes: on the
// admin port, with the operator's session, about one customer.
//
// Three entities and no new RPC. A `Host` is a name that means this tenant, a
// `Connection` is a provider the tenant's people authenticate at, and a
// `MailDomain` routes an address's domain to one of those providers. What is
// pinned is that the operator's session reaches all three writes and the
// reads that list them by tenant, that the layer's one rule over a host name
// (stored as it will be compared, refused otherwise with the value it should
// have been) reaches this port, and that the secret a connection would carry
// is not a field roster has -- `secret_ref` is a string it stores and does not
// read.
func TestAnOperatorSaysHowACustomersPeopleArrive(t *testing.T) {
	x := require.New(t)
	s, c, out := adminDeployment(t, nil)
	conn, as := adminPort(t, s, c, out)

	tn, err := app.NewTenantServiceClient(conn).Add(as, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: tn.GetId()}.Build()

	hosts := app.NewHostServiceClient(conn)
	connections := app.NewConnectionServiceClient(conn)
	domains := app.NewMailDomainServiceClient(conn)

	t.Run("a name that reaches the customer", func(t *testing.T) {
		x := require.New(t)

		_, err := hosts.Add(as, app.HostAddRequest_builder{
			Tenant: at, Name: "contoso.example.com", Desc: "the front door",
		}.Build())
		x.NoError(err)

		// Stored as it will be compared: a name that is not already is refused,
		// and the refusal says what it should have been, so the operator can
		// see what they typed and what the row would have to be.
		_, err = hosts.Add(as, app.HostAddRequest_builder{Tenant: at, Name: "Contoso.Example.com:8443"}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
		x.Contains(status.Convert(err).Message(), "contoso.example.com")

		vs, err := hosts.List(as, app.HostListRequest_builder{
			Filters: []*app.HostFilter{app.HostFilter_builder{Tenant: at}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)
		x.Equal("contoso.example.com", vs.GetItems()[0].GetName())
	})

	t.Run("a provider, with the secret's whereabouts and not the secret", func(t *testing.T) {
		x := require.New(t)

		v, err := connections.Add(as, app.ConnectionAddRequest_builder{
			Tenant:    at,
			Name:      "entra",
			Issuer:    "https://login.microsoftonline.com/9f3e/v2.0",
			ClientId:  "app-id-123",
			Scopes:    []string{"email"},
			SecretRef: "env:CONTOSO_ENTRA_SECRET",
		}.Build())
		x.NoError(err)
		x.Equal("env:CONTOSO_ENTRA_SECRET", v.GetSecretRef())

		// One name per tenant, because `Identity.provider` and
		// `MailDomain.provider` name it and two rows called "entra" would be
		// two directories under one word.
		_, err = connections.Add(as, app.ConnectionAddRequest_builder{
			Tenant: at, Name: "entra", Issuer: "https://elsewhere", ClientId: "x",
		}.Build())
		x.Equal(codes.AlreadyExists, status.Code(err))

		vs, err := connections.List(as, app.ConnectionListRequest_builder{
			Filters: []*app.ConnectionFilter{app.ConnectionFilter_builder{Tenant: at}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)
	})

	t.Run("a domain routed to that provider", func(t *testing.T) {
		x := require.New(t)

		_, err := domains.Add(as, app.MailDomainAddRequest_builder{
			Tenant: at, Name: "contoso.com", Provider: "entra",
		}.Build())
		x.NoError(err)

		// And one the deployment knows about and routes nowhere, which is a
		// different fact from not having the row (`host.proto`).
		_, err = domains.Add(as, app.MailDomainAddRequest_builder{Tenant: at, Name: "contoso.example"}.Build())
		x.NoError(err)

		vs, err := domains.List(as, app.MailDomainListRequest_builder{
			Filters: []*app.MailDomainFilter{app.MailDomainFilter_builder{Tenant: at}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 2)
	})

	t.Run("and each can be taken away again", func(t *testing.T) {
		x := require.New(t)

		vs, err := hosts.List(as, app.HostListRequest_builder{
			Filters: []*app.HostFilter{app.HostFilter_builder{Tenant: at}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)

		_, err = hosts.Erase(as, app.HostRef_builder{Id: vs.GetItems()[0].GetId()}.Build())
		x.NoError(err)

		vs, err = hosts.List(as, app.HostListRequest_builder{
			Filters: []*app.HostFilter{app.HostFilter_builder{Tenant: at}.Build()},
		}.Build())
		x.NoError(err)
		x.Empty(vs.GetItems(), "the name is still listed after it was removed")
	})
}
