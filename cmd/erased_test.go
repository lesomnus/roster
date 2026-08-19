package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestNothingOfAnErasedHolderIsReadableByNamingThem is the other half of what
// `holder.proto` claims, and the half that has nothing to do with signing in.
//
// It says an erased holder "cannot be read", and gives the wall as the reason:
// every read is narrowed by that column. That is true of a read that names the
// **holder**. It was not true of a read that names one of their rows *through*
// them -- `Email` is unique on `(holder, address)`, so the generated reference
// composed `HasHolderWith(HolderPick(...))`, and what the read narrowed was the
// email. Erase somebody and their address stayed readable by anybody who may
// read that tenant's mail, while the person themselves answered NotFound.
//
// Fixed in `protoc-gen-orm-ent` rather than here, which is the rule one layer
// down from payday's; F9 records it and what is left of it. This is the test
// that says roster has the fix, so that moving the pin backwards is something
// that fails here rather than something nobody notices.
func TestNothingOfAnErasedHolderIsReadableByNamingThem(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const address = "someone@acme.example"

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Address: address,
	}.Build())
	x.NoError(err)

	get := func(s app.Server) error {
		_, err := s.Email().Get(ctx, app.EmailGetRequest_builder{
			Ref: app.EmailRef_builder{
				Address: app.EmailRefByAddress_builder{
					Holder:  app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
					Address: z.Ptr(address),
				}.Build(),
			}.Build(),
			Select: app.EmailSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())

		return err
	}

	x.NoError(get(b.Ungated), "the control: while they are here, naming them finds it")

	_, err = b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build())
	x.NoError(err)

	// The unwalled stack, because that is the one with nothing else in front of
	// it: if the reference itself narrows, this is NotFound here, and a scope
	// added on top can only ever narrow it further.
	x.Equal(codes.NotFound, status.Code(get(b.Ungated)),
		"an erased holder's row was read by naming the holder")

	// And the row is still there, which is what soft erasure is for -- so this
	// is a reference that stopped reaching it rather than a delete.
	n, err := b.Ent.Email.Query().Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "the row was destroyed rather than put out of reach")
}
