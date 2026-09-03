package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestSomebodyAddsAWayIntoTheirOwnAccount, which is the other half of `Unlink`
// and the half §4 left undrawn -- as the line has it now: `Identity.Add` with
// the person's **own** reference, the same verb an operator calls about
// somebody else, and no `Me.Link` beside it.
//
// roster checks nothing about the claim -- being the relying party is what
// `connection.proto` says roster is not -- so what this is about is where the
// row lands, what the layer refuses, and that yourself is a holder you may
// always write a way into.
func TestSomebodyAddsAWayIntoTheirOwnAccount(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Contoso, "erin")
	b.identity(t, ctx, who, "google", "google-erin")

	own := app.HolderRef_builder{Id: who.Bytes()}.Build()
	as := b.as(ctx, who, b.Contoso)

	v, err := b.Walled.Identity().Add(as, app.IdentityAddRequest_builder{
		Holder:   own,
		Provider: "entra",
		Subject:  "entra-erin",
	}.Build())
	x.NoError(err)
	x.NotEmpty(v.GetId())

	t.Run("and it is theirs, because that is the row they named", func(t *testing.T) {
		x := require.New(t)

		u, err := b.Ent.Identity.Get(ctx, mustId(t, v.GetId()).Uuid())
		x.NoError(err)

		holder, err := u.QueryHolder().Only(ctx)
		x.NoError(err)
		x.Equal(who.Uuid(), holder.Id)
	})

	t.Run("and a second at one provider is refused", func(t *testing.T) {
		x := require.New(t)

		// `server/core` in as many words: *a second one is a link that found
		// the wrong row, and linking it would join two people into one.*
		_, err := b.Walled.Identity().Add(as, app.IdentityAddRequest_builder{
			Holder:   own,
			Provider: "google",
			Subject:  "some-other-google",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
	})

	t.Run("and one already somebody else's is refused without saying whose", func(t *testing.T) {
		x := require.New(t)

		other := b.holder(t, ctx, b.Contoso, "somebody-else")
		b.identity(t, ctx, other, "okta", "okta-taken")

		_, err := b.Walled.Identity().Add(as, app.IdentityAddRequest_builder{
			Holder:   own,
			Provider: "okta",
			Subject:  "okta-taken",
		}.Build())
		x.Equal(codes.AlreadyExists, status.Code(err))

		// Nothing about whose it is. Saying so would make this a lookup from a
		// provider subject to a person, which no caller here may do.
		msg := status.Convert(err).Message()
		x.NotContains(msg, "somebody-else")
		x.NotContains(msg, other.String())
	})

	t.Run("and it takes a claim, not an empty one", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Identity().Add(as, app.IdentityAddRequest_builder{
			Holder:   own,
			Provider: "entra",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
	})

	t.Run("and somebody wider than they are is not theirs to write", func(t *testing.T) {
		x := require.New(t)

		// The same verb, pointed at somebody else: `mayWriteAWayIn` is what
		// answers, and it is the whole of what "only your own" costs roster --
		// a reference the wall narrows and a rule about who holds more. Somebody
		// holding **nothing** does the pointing: erin above was handed
		// everything by `b.as`, which would make her wider than anybody.
		joe := b.holder(t, ctx, b.Contoso, "joe")
		boss := b.holder(t, ctx, b.Contoso, "boss")
		b.mayCall(t, ctx, boss, "admin", "/roster.HolderService/Erase")

		_, err := b.Walled.Identity().Add(b.asNobody(ctx, joe, b.Contoso), app.IdentityAddRequest_builder{
			Holder:   app.HolderRef_builder{Id: boss.Bytes()}.Build(),
			Provider: "entra",
			Subject:  "entra-boss-but-erins",
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"somebody wrote a way into an account wider than their own")
	})
}
