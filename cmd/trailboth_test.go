package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"uuid"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// lineOf is the trail's line for the write of `method` that made `id`.
func lineOf(t *testing.T, ctx context.Context, b *keyedBuilt, method string, id []byte) *ent.Audit {
	t.Helper()

	v, err := b.Ent.Audit.Query().
		Where(entaudit.ObjectIdEQ(mustId(t, id).Uuid()), entaudit.ActionEQ(method)).
		Only(ctx)
	require.NoError(t, err)

	return v
}

// TestACallThroughADelegationNamesBothOnTheTrail.
//
// A front door acting for somebody through the delegation it was handed left
// a line naming the person and nobody else: `caller_id`, `caller_tenant_id`
// and `presence`, which payday's trail has kept for a represented call since
// lesomnus/payday#260, were NULL, so nothing said which app it was or by which
// delegation. Answered: three NULLs; right is the holder that presented it,
// its tenant, and the delegation (`roster-as:<id>`).
//
// Who may do what does not change by it. The caller is recorded and not asked
// (`frame.Frame.Delegated`, lesomnus/payday#285): a front door holds nothing
// for its people on purpose, and a person's own list through a delegation
// issued to one that holds only another method is answered, as it was. A
// write made with no delegation leaves the three empty.
func TestACallThroughADelegationNamesBothOnTheTrail(t *testing.T) {
	b := keyFor(t)
	ctx := t.Context()
	x := require.New(t)

	// The front door is the holder contoso nominated for the harness's
	// deployment key, and is bound one method of its own.
	answersAt(t, b.Server, b.Contoso, "contoso.example")
	front := addHolder(t, ctx, b.Server, b.Contoso, "front")
	nominates(t, b.Server, b.Contoso, b.Service, front)
	permits(t, ctx, b, b.Contoso, front, "fronts", app.VouchService_Verify_FullMethodName)

	erin := addHolder(t, ctx, b.Server, b.Contoso, "erin")
	permits(t, ctx, b, b.Contoso, erin, "roles", app.RoleService_List_FullMethodName, app.RoleService_Add_FullMethodName)

	token, d, err := keys.Delegate(ctx, b.Ungated, keys.Delegated{
		Holder:  erin,
		Issuer:  front.Bytes(),
		Methods: []string{app.RoleService_List_FullMethodName, app.RoleService_Add_FullMethodName},
	})
	x.NoError(err)

	through := metadata.AppendToOutgoingContext(arrivedAt(ctx, b.Token, "contoso.example"), keys.HeaderActing, token)
	roles := app.NewRoleServiceClient(b.Conn)

	t.Run("what the person may call is answered, though the front door holds none of it", func(t *testing.T) {
		_, err := roles.List(through, app.RoleListRequest_builder{}.Build())
		require.NoError(t, err)
	})

	t.Run("a write names the person, the front door and the delegation", func(t *testing.T) {
		x := require.New(t)

		r, err := roles.Add(through, app.RoleAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "made-through", Methods: []string{app.RoleService_List_FullMethodName},
		}.Build())
		x.NoError(err)

		line := lineOf(t, ctx, b, app.RoleService_Add_FullMethodName, r.GetId())
		x.Equal(erin.Uuid(), line.ActorId, "the actor is not the person")
		x.NotNil(line.CallerId, "the trail names no caller")
		x.Equal(front.Uuid(), *line.CallerId, "the caller is not the front door's holder")
		x.NotNil(line.CallerTenantId)
		x.Equal(b.Contoso.Uuid(), *line.CallerTenantId)
		x.NotNil(line.Presence)
		x.Equal("roster-as:"+mustId(t, d.GetId()).String(), *line.Presence, "the reference is not the delegation")
	})

	t.Run("and a write with no delegation leaves the three empty", func(t *testing.T) {
		x := require.New(t)

		r, err := roles.Add(bearing(ctx, mintFor(t, ctx, b, erin, "erin", []string{"/roster.*/*"}, time.Time{})), app.RoleAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "made-directly", Methods: []string{app.RoleService_List_FullMethodName},
		}.Build())
		x.NoError(err)

		line := lineOf(t, ctx, b, app.RoleService_Add_FullMethodName, r.GetId())
		x.Equal(erin.Uuid(), line.ActorId)
		x.Nil(line.CallerId)
		x.Nil(line.CallerTenantId)
		x.Nil(line.Presence)
	})

	t.Run("a deployment key presenting one as itself is named by the key", func(t *testing.T) {
		x := require.New(t)

		mine, d := wroteAs(t, ctx, b, erin)
		r, err := roles.Add(acting(ctx, b.Token, mine), app.RoleAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "made-by-a-key", Methods: []string{app.RoleService_List_FullMethodName},
		}.Build())
		x.NoError(err)

		line := lineOf(t, ctx, b, app.RoleService_Add_FullMethodName, r.GetId())
		x.Equal(erin.Uuid(), line.ActorId)
		x.NotNil(line.CallerId)
		x.Equal(uuid.UUID(mustId(t, issuerOf(t, ctx, b))), *line.CallerId, "the caller is not the key")
		x.Equal("roster-as:"+d.String(), *line.Presence)
		// A key as itself is of no tenant, and payday writes its tenant as the nil
		// identifier rather than NULL.
		x.Equal(uuid.Nil(), *line.CallerTenantId)
	})
}

// wroteAs is a delegation of `who` issued to the harness's deployment key as
// itself, and its identifier.
func wroteAs(t *testing.T, ctx context.Context, b *keyedBuilt, who pdid.Id) (string, pdid.Id) {
	t.Helper()

	token, d, err := keys.Delegate(ctx, b.Ungated, keys.Delegated{
		Holder:  who,
		Issuer:  issuerOf(t, ctx, b),
		Methods: []string{app.RoleService_Add_FullMethodName},
	})
	require.NoError(t, err)

	return token, mustId(t, d.GetId())
}
