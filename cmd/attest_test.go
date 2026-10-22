package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/keys"
)

// An address moves inside a batch as it does outside one.
//
// A batch is one transaction, and two things about the address writes did not
// survive being put in one. `Email.Attest` wrote first and read on
// `AlreadyExists`, and a write the database refused ends the transaction it was
// in -- PostgreSQL ends it at once, and payday refuses its commit on SQLite
// (payday#128) -- so a batch could not attest an address its holder already
// had, which is every sign-in after the first. And the two writes that move an
// address from one row to another refused outright where the stack had no
// driver to begin a transaction on, which inside a batch it never has: it is
// already in one. These are run on SQLite and, with `PDTEST_POSTGRES`, on
// PostgreSQL.

// batching is a front door in contoso, on the wire, sending batches.
type batching struct {
	*built
	ctx context.Context
	do  func(t *testing.T, ops ...*pdpb.Op) error
}

func batchingAt(t *testing.T) *batching {
	t.Helper()
	b, ctx := build(t)

	door := b.holder(t, ctx, b.Contoso, "front-door")
	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "front-door",
		Methods: []string{"/roster.*/*", pdpb.BatchService_Do_FullMethodName},
	}.Build())
	require.NoError(t, err)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: door.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)

	conn := served(t, b.Server)
	wire := auth.PlainProvider(door.String()).Provide(ctx)

	return &batching{
		built: b,
		ctx:   ctx,
		do: func(t *testing.T, ops ...*pdpb.Op) error {
			t.Helper()
			_, err := pdpb.NewBatchServiceClient(conn).Do(wire, pdpb.BatchRequest_builder{Ops: ops}.Build())

			return err
		},
	}
}

func opOf(t *testing.T, method string, m proto.Message) *pdpb.Op {
	t.Helper()
	v, err := anypb.New(m)
	require.NoError(t, err)

	return pdpb.Op_builder{Method: method, Request: v}.Build()
}

// at is the row holding an address in contoso, read past the wall.
func (b *batching) at(t *testing.T, address string) *app.Email {
	t.Helper()
	v, err := b.Ungated.Email().Get(b.ctx, app.EmailGetRequest_builder{
		Ref: app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(address)}.Build()}.Build(),
		Select: app.EmailSelect_builder{
			All:       z.Ptr(true),
			Holder:    app.HolderSelect_builder{}.Build(),
			VouchedBy: app.IdentitySelect_builder{}.Build(),
		}.Build(),
	}.Build())
	require.NoError(t, err)

	return v
}

func TestAnAttestationIsOneOfABatch(t *testing.T) {
	const (
		theirs = "ceo@contoso.example"
		shared = "office@contoso.example"
	)
	b := batchingAt(t)

	ceo := b.holder(t, b.ctx, b.Contoso, "ceo")
	who := b.identity(t, b.ctx, ceo, "entra", "e1b2c3")
	attest := func(address string, verified bool) *pdpb.Op {
		return opOf(t, app.EmailService_Attest_FullMethodName, app.EmailAttestRequest_builder{
			Holder:    app.HolderRef_builder{Id: ceo.Bytes()}.Build(),
			Address:   z.Ptr(address),
			VouchedBy: app.IdentityRef_builder{Id: who.GetId()}.Build(),
			Verified:  z.Ptr(verified),
		}.Build())
	}

	t.Run("an address nobody holds is written", func(t *testing.T) {
		x := require.New(t)

		x.NoError(b.do(t, attest(theirs, false)))
		v := b.at(t, theirs)
		x.Equal(ceo.Bytes(), v.GetHolder().GetId())
		x.Equal(who.GetId(), v.GetVouchedBy().GetId())
		x.Nil(v.GetDateVerified())
	})

	t.Run("and attested again, it is stamped", func(t *testing.T) {
		x := require.New(t)

		// The second sign-in, after the directory started saying it checked:
		// the address is the holder's already, which is where the write that
		// came first was refused and took the batch with it.
		x.NoError(b.do(t, attest(theirs, true)))
		x.NotNil(b.at(t, theirs).GetDateVerified(), "the directory's word was not written down")

		// And once more, which writes nothing.
		x.NoError(b.do(t, attest(theirs, true)))
	})

	t.Run("and one nobody proved moves to whoever the directory vouches for", func(t *testing.T) {
		x := require.New(t)

		alice := b.holder(t, b.ctx, b.Contoso, "alice")
		_, err := b.Ungated.Email().Add(b.ctx, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
			Address: shared,
		}.Build())
		x.NoError(err)

		x.NoError(b.do(t, attest(shared, true)))
		v := b.at(t, shared)
		x.Equal(ceo.Bytes(), v.GetHolder().GetId(), "the address did not move inside a batch")
		x.NotNil(v.GetDateVerified())
		x.Equal(who.GetId(), v.GetVouchedBy().GetId())
	})
}

// TestAClaimIsSpentInsideABatch is the other move, `Email.Confirm` spending a
// claim: the address leaves the row that held it unproved for the claimant's,
// in three writes that are one.
func TestAClaimIsSpentInsideABatch(t *testing.T) {
	const theirs = "ceo@contoso.example"
	x := require.New(t)
	b := batchingAt(t)

	alice := b.holder(t, b.ctx, b.Contoso, "alice")
	_, err := b.Ungated.Email().Add(b.ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
		Address: theirs,
	}.Build())
	x.NoError(err)

	ceo := b.holder(t, b.ctx, b.Contoso, "ceo")
	door := b.as(b.ctx, b.holder(t, b.ctx, b.Contoso, "front-door"), b.Contoso)
	v, err := b.Walled.Email().Verify(door, app.EmailVerifyRequest_builder{
		Ref:    app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(theirs)}.Build()}.Build(),
		Holder: app.HolderRef_builder{Id: ceo.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	x.NoError(b.do(t, opOf(t, app.EmailService_Confirm_FullMethodName,
		app.EmailConfirmRequest_builder{Token: v.GetToken()}.Build())))

	row := b.at(t, theirs)
	x.Equal(ceo.Bytes(), row.GetHolder().GetId(), "the claim was not spent inside a batch")
	x.NotNil(row.GetDateVerified())
}

// TestAnAddressDoesNotMoveWhereItsWritesCannotBeOne is the other side of the
// two tests above: a stack built with no driver and called outside any
// transaction -- the admin port's is one -- has nothing to make the three
// writes one with, and refuses rather than make them one at a time, which a
// failure between them would leave as an address nobody holds.
func TestAnAddressDoesNotMoveWhereItsWritesCannotBeOne(t *testing.T) {
	const theirs = "ceo@contoso.example"
	x := require.New(t)
	b, ctx := build(t)

	alice := b.holder(t, ctx, b.Contoso, "alice")
	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
		Address: theirs,
	}.Build())
	x.NoError(err)
	ceo := b.holder(t, ctx, b.Contoso, "ceo")
	who := b.identity(t, ctx, ceo, "entra", "e1b2c3")

	bare, err := app.Build(b.Ungated, core.Build(cmd.Rules(b.Ent), core.WithPrefix(keys.PrefixTenant)))
	x.NoError(err)
	_, err = bare.Email().Attest(ctx, app.EmailAttestRequest_builder{
		Holder:    app.HolderRef_builder{Id: ceo.Bytes()}.Build(),
		Address:   z.Ptr(theirs),
		VouchedBy: app.IdentityRef_builder{Id: who.GetId()}.Build(),
		Verified:  z.Ptr(true),
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)

	v, err := b.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{
		Ref:    app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr(theirs)}.Build()}.Build(),
		Select: app.EmailSelect_builder{Holder: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(alice.Bytes(), v.GetHolder().GetId(), "the address moved in writes that were not one")
}
