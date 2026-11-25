package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/roster/rstr"
)

// TestWhatTheOperatorsPortsAnswerIsSo: three refusals at the ports a
// deployment's own keys and operators call keep their codes, and say what is
// so.
//
// # What it was
//
//   - `NominationService` `Get`, `List` and `Watch` at `control.addr` answered
//     *this deployment has no control plane, so no deployment keys*, at the
//     control plane's own port. A nomination names a deployment key, and is a
//     row of the plane the key calls into.
//   - `MeService/Get` answered every deployment key *there is no such person
//     any more*, a sentence written for somebody erased between two lines.
//     A deployment's key is nobody of a tenant until it names one.
//   - `TenantService/Erase` was refused in two sentences that disagreed: the
//     generated gate's, *taken down by whoever runs this deployment*, at
//     `server.addr` and `control.addr`, and `server/core`'s, which says what
//     does end a tenant, at `admin.addr`. No server has the method now, so
//     every port answers it as a method nobody serves.
func TestWhatTheOperatorsPortsAnswerIsSo(t *testing.T) {
	b := keyedFor(t, nil, "/roster.*/*")
	ctx := t.Context()

	boundOnControl(t, ctx, b.Server, b.Service.Bytes(), "everything", "/roster.*/*")
	op := addHolder(t, ctx, b.Control, controlTenant(t, ctx, b.Server), "operator")
	boundOnControl(t, ctx, b.Server, op.Bytes(), "operators", "/roster.*/*")
	_, err := b.Control.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Holder: app.HolderRef_builder{Id: op.Bytes()}.Build(), Secret: []byte("an operator's own password"),
	}.Build())
	require.NoError(t, err)
	k := signIn(t, b.Server, "operator", "an operator's own password")
	require.NotNil(t, k, "the operator did not sign in")

	g, err := b.GrpcControl(ctx, b.Config)
	require.NoError(t, err)
	control := pdtest.Serve(t, g)
	g, err = b.GrpcAdmin(ctx, b.Config)
	require.NoError(t, err)
	admin := pdtest.Serve(t, g)

	t.Run("a nomination at the control plane's own port", func(t *testing.T) {
		as := bearing(ctx, b.Token)
		ref := app.NominationRef_builder{Id: b.Service.Bytes()}.Build()

		for name, call := range map[string]func() error{
			"Get": func() error {
				_, err := app.NewNominationServiceClient(control).Get(as, app.NominationGetRequest_builder{Ref: ref}.Build())
				return err
			},
			"List": func() error {
				_, err := app.NewNominationServiceClient(control).List(as, app.NominationListRequest_builder{}.Build())
				return err
			},
			"Watch": func() error {
				st, err := app.NewNominationServiceClient(control).Watch(as, app.NominationWatchRequest_builder{
					Filters: []*app.NominationFilter{app.NominationFilter_builder{Ref: ref}.Build()},
				}.Build())
				if err != nil {
					return err
				}
				_, err = st.Recv()
				return err
			},
		} {
			t.Run(name, func(t *testing.T) {
				err := call()
				require.Equal(t, codes.Unimplemented, status.Code(err), "%v", err)
				require.NotContains(t, status.Convert(err).Message(), "no control plane")
				require.Contains(t, status.Convert(err).Message(), "data plane")
			})
		}
	})

	t.Run("who a deployment key is", func(t *testing.T) {
		for port, conn := range map[string]grpc.ClientConnInterface{"server.addr": b.Conn, "control.addr": control} {
			t.Run(port, func(t *testing.T) {
				_, err := app.NewMeServiceClient(conn).Get(bearing(ctx, b.Token), app.MeGetRequest_builder{}.Build())
				require.Equal(t, codes.NotFound, status.Code(err), "%v", err)
				require.NotContains(t, status.Convert(err).Message(), "any more")
				require.Contains(t, status.Convert(err).Message(), "deployment's key")
			})
		}
	})

	t.Run("a tenant erased", func(t *testing.T) {
		ref := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
		for port, p := range map[string]struct {
			conn grpc.ClientConnInterface
			as   context.Context
		}{
			"server.addr":  {b.Conn, bearing(ctx, b.Token)},
			"control.addr": {control, bearing(ctx, b.Token)},
			"admin.addr":   {admin, metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", k.Name+"="+k.Value))},
		} {
			t.Run(port, func(t *testing.T) {
				// No server has the method now (#531): one answer, gRPC's own.
				err := p.conn.Invoke(p.as, "/roster.TenantService/Erase", ref, &emptypb.Empty{})
				require.Equal(t, codes.Unimplemented, status.Code(err), "%v", err)
				require.Contains(t, status.Convert(err).Message(), "unknown method Erase")
			})
		}
	})
}
