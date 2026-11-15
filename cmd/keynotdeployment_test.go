package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestAKeyOfTheDeploymentsIsNotTheDeployment: a deployment key calls what its
// list names, across every tenant -- and is held, in calling it, to what
// anybody at a port is held to.
//
// # What it was
//
// Two rules are not asked of the deployment's own work: a tenant is left with
// somebody administering it (`administered.go`), and a row a file declares is
// read-only (`declared.go`). Each says whose work that is -- a command at the
// box, the operators' port, what applies the file -- and neither says a key.
// What they tested for was a frame over every tenant, *which nothing reaching a
// listener can have*: and a deployment key reaching a listener has exactly
// that.
//
// So a key made to suspend a customer's people (`Holder.Disable`, and nothing
// else) suspended the deployment's only operator, whose console then answered
// nobody; and one that could write a kind of row wrote the ones a file owns,
// to be put back at the next start by a restart nobody would connect to it
// (#274).
func TestAKeyOfTheDeploymentsIsNotTheDeployment(t *testing.T) {
	ctx := t.Context()

	t.Run("it does not leave the deployment with no operator", func(t *testing.T) {
		x := require.New(t)

		s, _ := inited(t)

		ts, err := s.Control.Ungated.Tenant().List(ctx, app.TenantListRequest_builder{Size: 1}.Build())
		x.NoError(err)
		owner := app.TenantRef_builder{Id: ts.GetItems()[0].GetId()}.Build()

		svc, err := s.Control.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: owner, Alias: "suspends"}.Build())
		x.NoError(err)
		token, sum, err := keys.Mint(keys.PrefixDeployment)
		x.NoError(err)
		_, err = s.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: svc.GetId()}.Build(),
			Alias:   "for-customers-people",
			Secret:  sum,
			Methods: []string{app.HolderService_Disable_FullMethodName},
		}.Build())
		x.NoError(err)

		// Bound the method on the control plane, so that the key may suspend
		// an operator at all there
		// (`TestAKeyAtTheControlListenerIsWhoseItIs`): what is asked here is
		// whether it may suspend the last one.
		boundOnControl(t, ctx, s, svc.GetId(), "suspends-operators", app.HolderService_Disable_FullMethodName)

		admin := app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: strPtr("admin"), Tenant: owner}.Build()}.Build()
		_, err = app.NewHolderServiceClient(servedControl(t, s)).Disable(bearing(ctx, token),
			app.HolderDisableRequest_builder{Ref: admin}.Build())
		x.Error(err, "a key suspended the deployment's only operator")
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)

		still, err := s.Control.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: admin}.Build())
		x.NoError(err)
		x.Nil(still.GetDateDisabled())
	})

	t.Run("nor write a row a file declares", func(t *testing.T) {
		x := require.New(t)

		b := keyFor(t, app.HolderService_Disable_FullMethodName)

		declared, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:  "front-door",
			Labels: map[string]string{cmd.Declared: "config: login"},
		}.Build())
		x.NoError(err)

		_, err = app.NewHolderServiceClient(b.Conn).Disable(bearing(ctx, b.Token),
			app.HolderDisableRequest_builder{Ref: app.HolderRef_builder{Id: declared.GetId()}.Build()}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
		x.ErrorContains(err, "declared")

		// And a row no file declares is the key's to write, as its list says.
		_, err = app.NewHolderServiceClient(b.Conn).Disable(bearing(ctx, b.Token),
			app.HolderDisableRequest_builder{Ref: app.HolderRef_builder{Id: b.Who.Bytes()}.Build()}.Build())
		x.NoError(err)
	})
}

// TestAKeyAtTheControlListenerIsWhoseItIs: a deployment key is a caller of the
// data plane, where its list is the whole of what it may call. At the control
// listener -- where the rows are who runs this deployment -- it is the holder
// it hangs off, and calls what that holder was bound there, of what its list
// names.
//
// # What it was
//
// A key's list names methods and no plane, and the same services answer at
// both listeners: `HolderService` is a customer's people at one and the
// deployment's operators at the other. The gate asked nothing of a key at
// either. So a key made for an app that manages customers -- `Holder.List`,
// `Holder.Disable` -- listed the operators at the control listener and
// suspended one (#274): every key the deployment had ever minted was, there, a
// key to the deployment.
//
// Who may do what to the operators is a role and a binding on the control
// plane, as it is for an operator. A key's holder is somebody there; so it is
// asked of them. An app's holder is bound nothing there, and its key does
// nothing there.
func TestAKeyAtTheControlListenerIsWhoseItIs(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	list := app.HolderService_List_FullMethodName
	b := keyFor(t, list)

	holders := func(conn *grpc.ClientConn) error {
		_, err := app.NewHolderServiceClient(conn).List(bearing(ctx, b.Token), app.HolderListRequest_builder{}.Build())

		return err
	}

	// Where it is for: a customer's people, by its list and nothing else.
	x.NoError(holders(b.Conn))

	control := servedControl(t, b.Server)
	err := holders(control)
	x.Equal(codes.PermissionDenied, status.Code(err), "a key made for customers' people listed the deployment's operators: %v", err)

	// Until whoever runs the deployment says its holder may, there, as they
	// would of anybody.
	boundOnControl(t, ctx, b.Server, b.Service.Bytes(), "reads-operators", list)

	x.NoError(holders(control), "a key whose holder is bound the method on the control plane")

	// And still of what its list names: the binding widens nothing.
	_, err = app.NewRoleServiceClient(control).List(bearing(ctx, b.Token), app.RoleListRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
}

// boundOnControl has a holder of the control plane hold a role naming these
// methods there, which is what a key hanging off them calls at the control
// listener ([cmd.PolicyOfKeys]).
//
// Through `Ungated`, as `roster control bind` writes it at the box: who runs
// the deployment says it, and no key says it of itself.
func boundOnControl(t *testing.T, ctx context.Context, s *cmd.Server, holder []byte, alias string, methods ...string) {
	t.Helper()
	x := require.New(t)

	owner := app.TenantRef_builder{Id: controlTenant(t, ctx, s).Bytes()}.Build()

	role, err := s.Control.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: owner, Alias: alias, Methods: methods,
	}.Build())
	x.NoError(err)

	_, err = s.Control.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: holder}.Build(),
	}.Build())
	x.NoError(err)
}
