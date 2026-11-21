package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	entaccess "github.com/lesomnus/roster/internal/ent/access"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/standing"
)

// Under whose tenant a row of the access record is filed (#280).
//
// payday reads a row's tenant off the message a call answered with, and files
// a row whose answer carried none under the caller's. A request's references
// carry no tenant at all, so a refused call was always the caller's. For
// somebody walled to one tenant that is the same tenant. It is not for the two
// callers the record is most for: an operator at `admin.addr`, held by the
// control plane's tenant, which is no tenant of the data plane; and a
// deployment key, held by none.

// filed is the rows of `method` on the data plane's record, once what `s` holds
// is written, and the record is emptied for the next call to be read by itself.
func filed(t *testing.T, ctx context.Context, s *cmd.Server, method string) []*ent.Access {
	t.Helper()
	x := require.New(t)

	x.NoError(s.Access.Flush(ctx))
	rs, err := s.Ent.Access.Query().Where(entaccess.ActionEQ(method)).All(ctx)
	x.NoError(err)
	_, err = s.Ent.Access.Delete().Exec(ctx)
	x.NoError(err)

	return rs
}

// TestAReadIsFiledUnderTheTenantOfWhoeverWasRead: what an operator, or a
// deployment key, read of a customer's person is a row under that customer,
// whether or not the answer said which customer.
//
// # What it was
//
// An operator's `HolderService/Get` of a customer's person was filed under the
// customer when it read the row whole, and under the control plane's tenant
// when it asked for the alias and the name with a `select`, since the answer
// then carries no `tenant`. The same for an address. A deployment key's was
// filed under no tenant. The manual says an operator's read "is filed under
// that customer", and shows the review by customer as a filter on `tenant_id`:
// that review did not show these reads, and a hold on the customer did not
// keep them (`TestAHoldKeepsAReadThatLeftTheTenantOut`).
//
// The tenant is the row's, read by its key where the answer did not say.
func TestAReadIsFiledUnderTheTenantOfWhoeverWasRead(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, c, out := adminDeployment(t, nil)
	conn, as := adminPort(t, s, c, out)
	who := adminCustomer(t, conn, as, "newco")
	other := adminCustomer(t, conn, as, "otherco")

	owner := s.Control.Ent.Tenant.Query().OnlyIdX(ctx)
	operator, err := s.Control.Ent.Holder.Query().OnlyId(ctx)
	x.NoError(err)
	newco := s.Ent.Holder.GetX(ctx, mustId(t, who).Uuid()).TenantId
	otherco := s.Ent.Holder.GetX(ctx, mustId(t, other).Uuid()).TenantId
	x.NotEqual(owner, newco)

	address, err := app.NewEmailServiceClient(conn).Add(as, app.EmailAddRequest_builder{
		Holder: app.HolderRef_builder{Id: who}.Build(), Address: "someone@newco.example",
	}.Build())
	x.NoError(err)
	filed(t, ctx, s, "")

	H := app.NewHolderServiceClient(conn)

	t.Run("an operator's whole read", func(t *testing.T) {
		x := require.New(t)

		v, err := H.Get(as, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: who}.Build()}.Build())
		x.NoError(err)
		x.True(v.HasTenant())

		rs := filed(t, ctx, s, app.HolderService_Get_FullMethodName)
		x.Len(rs, 1)
		x.Equal(newco, rs[0].TenantId)
		x.Equal(operator, rs[0].ActorId)
		x.Equal(owner, rs[0].ActorTenantId)
	})

	t.Run("the same with a select", func(t *testing.T) {
		x := require.New(t)

		v, err := H.Get(as, app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Id: who}.Build(),
			Select: app.HolderSelect_builder{Alias: z.Ptr(true), Name: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("someone", v.GetAlias())
		x.False(v.HasTenant(), "the answer says whose it is, so this asks nothing")

		rs := filed(t, ctx, s, app.HolderService_Get_FullMethodName)
		x.Len(rs, 1)
		x.Equal(newco, rs[0].TenantId, "filed under the operators' own tenant, where no review by customer looks")
		x.Equal([][]byte{who}, rs[0].Subjects)
		x.Equal(operator, rs[0].ActorId)
		x.Equal(owner, rs[0].ActorTenantId)
	})

	t.Run("an address with a select", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewEmailServiceClient(conn).Get(as, app.EmailGetRequest_builder{
			Ref:    app.EmailRef_builder{Id: address.GetId()}.Build(),
			Select: app.EmailSelect_builder{Address: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("someone@newco.example", v.GetAddress())
		x.NotEqual(newco[:], v.GetTenantId(), "the answer says whose it is, so this asks nothing")

		rs := filed(t, ctx, s, app.EmailService_Get_FullMethodName)
		x.Len(rs, 1)
		x.Equal(newco, rs[0].TenantId)
		x.Equal([][]byte{address.GetId()}, rs[0].Subjects)
	})

	t.Run("and a call the operators' port refuses, under whose row it named", func(t *testing.T) {
		x := require.New(t)

		// Two customers' people named in one request that is refused for what
		// else it says: a row under each, since each was asked about.
		_, err := H.List(as, app.HolderListRequest_builder{
			Filters: []*app.HolderFilter{
				app.HolderFilter_builder{Ref: app.HolderRef_builder{Id: who}.Build()}.Build(),
				app.HolderFilter_builder{Ref: app.HolderRef_builder{Id: other}.Build()}.Build(),
			},
			After: "not a cursor",
		}.Build())
		x.Error(err)

		rs := filed(t, ctx, s, app.HolderService_List_FullMethodName)
		x.Len(rs, 2)
		under := map[pdid.Id][][]byte{}
		for _, r := range rs {
			x.Equal(status.Code(err).String(), r.Result)
			x.Equal(operator, r.ActorId)
			under[pdid.Id(r.TenantId)] = r.Subjects
		}
		x.Equal(map[pdid.Id][][]byte{pdid.Id(newco): {who}, pdid.Id(otherco): {other}}, under)
	})
}

// TestAKeysReadIsFiledUnderTheTenantOfWhoeverWasRead: the same for a
// deployment key, whose frame names no tenant, and for what a call that was
// refused named.
//
// # What it was
//
// A deployment key's `HolderService/Get` with a `select` that leaves the
// tenant out was a row under **no** tenant, and so was every call of a key's
// that was refused: nobody's record, and no customer's. roster's own client
// makes such reads -- `standing.Addresses` reads each address with a `select`
// of the address and when it was proved -- so an app holding a deployment key
// left one row under no tenant for each address it read.
//
// A refused call that named a row which exists is filed under that row's
// tenant, since that is whose data was asked for. One that named nothing that
// exists stays the caller's: there is no row to say whose it would have been.
func TestAKeysReadIsFiledUnderTheTenantOfWhoeverWasRead(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	b := keyFor(t, append([]string{app.HolderService_Get_FullMethodName}, standing.Methods...)...)
	as := bearing(ctx, b.Token)

	address, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(), Address: "someone@contoso.example",
	}.Build())
	x.NoError(err)
	filed(t, ctx, b.Server, "")

	H := app.NewHolderServiceClient(b.Conn)

	t.Run("a deployment key with a select", func(t *testing.T) {
		x := require.New(t)

		v, err := H.Get(as, app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
			Select: app.HolderSelect_builder{Alias: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("someone", v.GetAlias())

		rs := filed(t, ctx, b.Server, app.HolderService_Get_FullMethodName)
		x.Len(rs, 1)
		x.Equal(b.Contoso.Uuid(), rs[0].TenantId, "filed under no tenant")
		x.Equal(pdid.Nil.Uuid(), rs[0].ActorTenantId, "a deployment key is held by no tenant of this plane")
	})

	t.Run("what roster's own client reads of somebody's addresses", func(t *testing.T) {
		x := require.New(t)

		a, err := standing.New(standing.Config{
			Conn: b.Conn,
			As:   func(ctx context.Context) (context.Context, error) { return bearing(ctx, b.Token), nil },
		})
		x.NoError(err)
		_, err = a.Addresses(ctx, b.Who)
		x.NoError(err)

		rs := filed(t, ctx, b.Server, app.EmailService_Get_FullMethodName)
		x.Len(rs, 1, "one address, read once")
		x.Equal(b.Contoso.Uuid(), rs[0].TenantId, "filed under no tenant")
		x.Equal([][]byte{address.GetId()}, rs[0].Subjects)
	})

	// Refused for what else it says, by a server behind the record: a cursor
	// that is none.
	refused := func(whose pdid.Id) error {
		_, err := app.NewEmailServiceClient(b.Conn).List(as, app.EmailListRequest_builder{
			Filters: []*app.EmailFilter{app.EmailFilter_builder{
				Holder: app.HolderRef_builder{Id: whose.Bytes()}.Build(),
			}.Build()},
			After: "not a cursor",
		}.Build())

		return err
	}

	t.Run("a refused call that named a row which exists", func(t *testing.T) {
		x := require.New(t)

		err := refused(b.Who)
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

		rs := filed(t, ctx, b.Server, app.EmailService_List_FullMethodName)
		x.Len(rs, 1)
		x.Equal(codes.InvalidArgument.String(), rs[0].Result)
		x.Equal(b.Contoso.Uuid(), rs[0].TenantId, "filed under no tenant")
		x.Equal([][]byte{b.Who.Bytes()}, rs[0].Subjects)
	})

	t.Run("and one that named nothing that exists stays the caller's", func(t *testing.T) {
		x := require.New(t)

		nobody := pdid.New(b.Who.Domain())
		err := refused(nobody)
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

		rs := filed(t, ctx, b.Server, app.EmailService_List_FullMethodName)
		x.Len(rs, 1)
		x.Equal(pdid.Nil.Uuid(), rs[0].TenantId)
		x.Equal([][]byte{nobody.Bytes()}, rs[0].Subjects)
	})
}

// TestARefusedReadAcrossTheWallIsOnTheRecordOfWhoseRowItNamed: somebody of
// one tenant who asks for another tenant's person by identifier is refused,
// and the row of that refusal is the other tenant's, naming who asked.
//
// # What it was
//
// It was filed under the caller's own tenant, since a request's reference
// carries none: the tenant whose person was asked about had no row of it, and
// the one that did was the tenant of whoever asked. The caller is told what
// they were told before -- that there is nobody by that identifier -- and
// learns nothing from a record they do not read.
func TestARefusedReadAcrossTheWallIsOnTheRecordOfWhoseRowItNamed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	bob := b.holder(t, ctx, b.Fabrikam, "bob")
	b.mayAnything(b.ContosoUser, b.Contoso)

	conn := served(t, b.Server)
	as := asOverTheWire(ctx, b.ContosoUser)

	_, err := app.NewHolderServiceClient(conn).Get(as, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: bob.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "%v", err)

	rs := filed(t, ctx, b.Server, app.HolderService_Get_FullMethodName)
	x.Len(rs, 1)
	x.Equal(codes.NotFound.String(), rs[0].Result)
	x.Equal(b.Fabrikam.Uuid(), rs[0].TenantId, "filed under the tenant of whoever asked")
	x.Equal(b.ContosoUser.Uuid(), rs[0].ActorId)
	x.Equal(b.Contoso.Uuid(), rs[0].ActorTenantId)
	x.Equal([][]byte{bob.Bytes()}, rs[0].Subjects)
}

// TestAReadAtTheBoxIsFiledUnderTheTenantOfWhoeverWasRead: the same at a shell
// on the box, where the operator `--as` names is held by the control plane's
// tenant as they are at the admin port.
//
// # What it was
//
// `roster --as admin holder get @newco/admin '{"select":{"alias":true}}'`
// printed the alias and left a row under the control plane's tenant.
func TestAReadAtTheBoxIsFiledUnderTheTenantOfWhoeverWasRead(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	out, err := entities(t, at(b.c, "admin"), "holder", "get", "-o", "json", "@newco/admin", `{"select":{"alias":true}}`)
	x.NoError(err)
	x.Contains(out, `"alias": "admin"`)
	x.NotContains(out, b.newco.String(), "the answer says whose it is, so this asks nothing")

	rs := accessed(t, b.c, false, app.HolderService_Get_FullMethodName)
	x.Len(rs, 1)
	x.Equal(b.newco.Uuid(), rs[0].TenantId, "filed under the operators' own tenant")
	x.Equal(b.operator.Uuid(), rs[0].ActorId)
	x.Equal(b.owner.Uuid(), rs[0].ActorTenantId)
}

// TestAHoldKeepsAReadThatLeftTheTenantOut: a hold on a tenant keeps every row
// of the access record that is about its people, a read that asked for part of
// a row among them.
//
// # What it was
//
// A hold keeps the rows that name its tenant. With `newco` held, a manual
// discard of the access record kept the operator's whole-row read of newco's
// person and destroyed their `select` read of the same person, made a moment
// later: "kept by a legal hold: 1 row(s)", and one row left of two. The row
// that went named the control plane's tenant and no other.
func TestAHoldKeepsAReadThatLeftTheTenantOut(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, c, out := adminDeployment(t, nil)
	conn, as := adminPort(t, s, c, out)
	who := adminCustomer(t, conn, as, "newco")
	filed(t, ctx, s, "")

	H := app.NewHolderServiceClient(conn)
	_, err := H.Get(as, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: who}.Build()}.Build())
	x.NoError(err)
	_, err = H.Get(as, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Id: who}.Build(),
		Select: app.HolderSelect_builder{Alias: z.Ptr(true), Name: z.Ptr(true), Profile: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.NoError(s.Access.Flush(ctx))
	x.Equal(2, s.Ent.Access.Query().Where(entaccess.ActionEQ(app.HolderService_Get_FullMethodName)).CountX(ctx))

	_, err = entities(t, at(c, ""), "hold", "place", "--why", "Contoso v. Fabrikam", "@newco")
	x.NoError(err)
	_, err = entities(t, at(c, ""), "trail", "prune", "--kind", "access", "--older-than", "1ns", "--discard")
	x.NoError(err)

	x.Equal(2, s.Ent.Access.Query().Where(entaccess.ActionEQ(app.HolderService_Get_FullMethodName)).CountX(ctx),
		"a read of a held tenant's person was destroyed with the hold on")
}
