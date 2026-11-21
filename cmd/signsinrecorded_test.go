package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
)

// TestReadingHowSomebodySignsInIsOnTheRecord: `HolderService/SignsIn` of
// somebody is a row of the access record naming who read and whom, at a port
// and at the box.
//
// # What it was
//
// It answers somebody's subjects at their providers -- which `Identity.subject`
// declares personal -- and what their keys and factors are called, and left no
// row anywhere: answered 200 and wrote nothing. The record finds whose data a
// call carried in the messages that declare a personal field (`Holder`,
// `Email`, `Identity`, `Phone`), and this answers in a message of its own,
// `SignInIdentity`, which declares none. Nothing said so in its place
// (`access.Saw`), which is what that is for.
//
// It names the person, and each account at a provider whose subject it
// answered, by the row that holds it -- as `IdentityService/List` names them.
func TestReadingHowSomebodySignsInIsOnTheRecord(t *testing.T) {
	t.Run("by an administrator at server.addr", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		alice := b.holder(t, ctx, b.Contoso, "alice")
		account := b.identity(t, ctx, alice, "entra", "sub-of-alice")
		b.mayAnything(b.ContosoUser, b.Contoso)

		conn := served(t, b.Server)
		v, err := app.NewHolderServiceClient(conn).SignsIn(asOverTheWire(ctx, b.ContosoUser),
			app.HolderSignsInRequest_builder{Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build()}.Build())
		x.NoError(err)
		x.Len(v.GetIdentities(), 1)
		x.Equal("sub-of-alice", v.GetIdentities()[0].GetSubject())

		rs := filed(t, ctx, b.Server, app.HolderService_SignsIn_FullMethodName)
		x.Len(rs, 1, "answered somebody's subject at a provider, and left no row")
		x.Equal("read", rs[0].Kind)
		x.Equal("ok", rs[0].Result)
		x.Equal(b.Contoso.Uuid(), rs[0].TenantId)
		x.Equal(b.ContosoUser.Uuid(), rs[0].ActorId)
		x.ElementsMatch([][]byte{alice.Bytes(), account.GetId()}, rs[0].Subjects)
	})

	t.Run("by an operator at admin.addr, under the customer", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()
		s, c, out := adminDeployment(t, nil)
		conn, as := adminPort(t, s, c, out)
		who := adminCustomer(t, conn, as, "newco")
		newco := s.Ent.Holder.GetX(ctx, mustId(t, who).Uuid()).TenantId
		operator := s.Control.Ent.Holder.Query().OnlyIdX(ctx)
		filed(t, ctx, s, "")

		_, err := app.NewHolderServiceClient(conn).SignsIn(as,
			app.HolderSignsInRequest_builder{Ref: app.HolderRef_builder{Id: who}.Build()}.Build())
		x.NoError(err)

		rs := filed(t, ctx, s, app.HolderService_SignsIn_FullMethodName)
		x.Len(rs, 1, "answered how a customer's person signs in, and left no row")
		x.Equal(newco, rs[0].TenantId)
		x.Equal(operator, rs[0].ActorId)
		x.Equal([][]byte{who}, rs[0].Subjects)
		x.Zero(s.Control.Ent.Access.Query().CountX(ctx), "the customer's person is the data plane's")
	})

	t.Run("at the box", func(t *testing.T) {
		x := require.New(t)
		b := atTheBox(t)

		out, err := entities(t, at(b.c, "admin"), "holder", "signs-in", "-o", "json", "@newco/admin")
		x.NoError(err)
		x.NotEmpty(out)

		rs := accessed(t, b.c, false, app.HolderService_SignsIn_FullMethodName)
		x.Len(rs, 1, "printed how somebody signs in, and left no row")
		x.Equal(b.newco.Uuid(), rs[0].TenantId)
		x.Equal(b.operator.Uuid(), rs[0].ActorId)
		x.Equal([][]byte{b.admin.Bytes()}, rs[0].Subjects)
	})
}

// TestWhatSomebodyIsToldOfThemselvesLeavesNoRow: `MeService/Get` answers its
// caller their own alias, addresses and accounts, and is not on the access
// record -- on purpose.
//
// The record is of who read **somebody else's** data. `MeService` takes no
// subject, so it cannot be pointed at anybody but its caller: somebody reading
// what is theirs, themselves or through an app acting as them -- a front door,
// the Login App filling a token's claims, a directory front checking who
// bound. A row for each page they load would bury the ones a review is for.
// So what an app is told of the person who is using it leaves no row, and the
// manual says so where it says what a row is written for.
//
// The same person read through the directory is a row, their own included: a
// `HolderService/Get` names whom it reads, and the record does not ask whether
// that is the caller.
func TestWhatSomebodyIsToldOfThemselvesLeavesNoRow(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	alice := b.holder(t, ctx, b.Contoso, "alice")
	b.identity(t, ctx, alice, "entra", "sub-of-alice")
	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(), Address: "alice@contoso.example",
	}.Build())
	x.NoError(err)
	b.mayAnything(alice, b.Contoso)
	filed(t, ctx, b.Server, "")

	conn := served(t, b.Server)
	as := asOverTheWire(ctx, alice)

	me, err := app.NewMeServiceClient(conn).Get(as, app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.Equal("alice", me.GetAlias())
	x.Len(me.GetEmails(), 1)
	x.Len(me.GetIdentities(), 1)

	x.NoError(b.Access.Flush(ctx))
	x.Zero(b.Ent.Access.Query().CountX(ctx), "what somebody is told of themselves is not somebody reading somebody else's data")

	_, err = app.NewHolderServiceClient(conn).Get(as, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build()}.Build())
	x.NoError(err)
	rs := filed(t, ctx, b.Server, app.HolderService_Get_FullMethodName)
	x.Len(rs, 1, "read through the directory, it is a row whoever it is of")
	x.Equal(alice.Uuid(), rs[0].ActorId)
	x.Equal([][]byte{alice.Bytes()}, rs[0].Subjects)
}
