package cmd_test

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/z"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestAVerifierNamesNoRowOverTheWire: a key or a delegation is named at a
// port by its identifier or its name, and never by its verifier.
//
// # What it was
//
// `ApiKeyRef` and `DelegationRef` have a `secret` arm, which `server/keys`
// finds a row by in process: the hash of the token somebody presented. At a
// port it answered too. `ApiKeyService/Get`, `List` and `Erase` and
// `DelegationService/Get` by a verifier found the row, so whoever holds the
// methods and one hash had a question to ask of every row there is -- *is this
// a key, and whose* -- that nothing on the wire was meant to answer. Nothing in
// this repository sends it over a port. Refused before anything is read, the
// way a label only a file writes is refused (`cmd.Undeclared`).
func TestAVerifierNamesNoRowOverTheWire(t *testing.T) {
	for port, p := range identifierPorts(t) {
		t.Run(port, func(t *testing.T) {
			x := require.New(t)

			holder, err := app.NewHolderServiceClient(p.conn).Add(p.as, app.HolderAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: p.tenant}.Build(), Alias: "keyed",
			}.Build())
			x.NoError(err)
			minted, err := app.NewApiKeyServiceClient(p.conn).Issue(p.as, app.ApiKeyIssueRequest_builder{
				Holder:  app.HolderRef_builder{Id: holder.GetId()}.Build(),
				Alias:   "laptop",
				Methods: []string{app.MeService_Get_FullMethodName},
			}.Build())
			x.NoError(err)

			// The control: by its identifier, it is there.
			_, err = app.NewApiKeyServiceClient(p.conn).Get(p.as, app.ApiKeyGetRequest_builder{
				Ref: app.ApiKeyRef_builder{Id: minted.GetKey().GetId()}.Build(),
			}.Build())
			x.NoError(err)

			byKey := app.ApiKeyRef_builder{Secret: keys.Sum(minted.GetToken())}.Build()

			for name, call := range map[string]func() error{
				"ApiKeyService/Get": func() error {
					_, err := app.NewApiKeyServiceClient(p.conn).Get(p.as, app.ApiKeyGetRequest_builder{
						Ref: byKey, Select: app.ApiKeySelect_builder{Alias: z.Ptr(true)}.Build(),
					}.Build())
					return err
				},
				"ApiKeyService/List": func() error {
					_, err := app.NewApiKeyServiceClient(p.conn).List(p.as, app.ApiKeyListRequest_builder{
						Filters: []*app.ApiKeyFilter{app.ApiKeyFilter_builder{Ref: byKey}.Build()},
					}.Build())
					return err
				},
				"ApiKeyService/Erase": func() error {
					_, err := app.NewApiKeyServiceClient(p.conn).Erase(p.as, byKey)
					return err
				},
				"DelegationService/Get": func() error {
					_, err := app.NewDelegationServiceClient(p.conn).Get(p.as, app.DelegationGetRequest_builder{
						Ref: app.DelegationRef_builder{Secret: keys.Sum(keys.PrefixDelegation + rand.Text())}.Build(),
					}.Build())
					return err
				},
			} {
				t.Run(name, func(t *testing.T) {
					err := call()
					require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
				})
			}

			// And the key is still there: the erase by its verifier was refused,
			// and not answered as one of nothing.
			_, err = app.NewApiKeyServiceClient(p.conn).Get(p.as, app.ApiKeyGetRequest_builder{
				Ref: app.ApiKeyRef_builder{Id: minted.GetKey().GetId()}.Build(),
			}.Build())
			x.NoError(err)
		})
	}
}

// TestNoServiceIsRegisteredWithNoMethod: every service a port lists answers
// something.
//
// # What it was
//
// `Register` had a line for `OutboxService`, which has no method today, and
// so the name was listed on `server.addr`, `control.addr` and `admin.addr`
// with nothing under it. Its entity says that what keeps one tenant's writes
// from another's is that nothing serves it at all; with the line there, a
// release of payday that gave the outbox a read would be served on three
// ports by moving a pin. A service is served when somebody writes the line
// for what it answers.
func TestNoServiceIsRegisteredWithNoMethod(t *testing.T) {
	ctx := t.Context()

	k := keyFor(t)
	s, c, _ := adminDeployment(t, nil)

	ports := map[string]func() (*grpc.Server, error){
		"server.addr":  func() (*grpc.Server, error) { return k.Grpc(ctx, cmd.Config{}) },
		"control.addr": func() (*grpc.Server, error) { return k.GrpcControl(ctx, cmd.Config{}) },
		"admin.addr":   func() (*grpc.Server, error) { return s.GrpcAdmin(ctx, c) },
	}
	for port, served := range ports {
		t.Run(port, func(t *testing.T) {
			g, err := served()
			require.NoError(t, err)
			for name, info := range g.GetServiceInfo() {
				require.NotEmpty(t, info.Methods, "%s is listed on %s and answers nothing", name, port)
			}
		})
	}
}
