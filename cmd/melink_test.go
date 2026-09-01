package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/me"
)

// TestSomebodyAddsAWayIntoTheirOwnAccount, which is the other half of `Unlink`
// and the half §4 left undrawn.
//
// roster checks nothing about the claim -- being the relying party is what
// `connection.proto` says roster is not -- so what this is about is where the
// row lands and what it refuses.
func TestSomebodyAddsAWayIntoTheirOwnAccount(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Contoso, "erin")
	b.identity(t, ctx, who, "google", "google-erin")

	s := me.New(b.Ent, cmd.Everything(b.Ent), me.WithWrites(b.Walled))

	v, err := s.Link(b.as(ctx, who, b.Contoso), app.MeLinkRequest_builder{
		Provider: "entra",
		Subject:  "entra-erin",
	}.Build())
	x.NoError(err)
	x.NotEmpty(v.GetId())

	t.Run("and it is theirs, read off the frame", func(t *testing.T) {
		x := require.New(t)

		// The row hangs off the actor and there is no field that could have
		// said otherwise, which is what keeps this in the same category as
		// `Get`: it cannot be pointed at anybody.
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
		_, err := s.Link(b.as(ctx, who, b.Contoso), app.MeLinkRequest_builder{
			Provider: "google",
			Subject:  "some-other-google",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
	})

	t.Run("and one already somebody else's is refused without saying whose", func(t *testing.T) {
		x := require.New(t)

		other := b.holder(t, ctx, b.Contoso, "somebody-else")
		b.identity(t, ctx, other, "okta", "okta-taken")

		_, err := s.Link(b.as(ctx, who, b.Contoso), app.MeLinkRequest_builder{
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

		_, err := s.Link(b.as(ctx, who, b.Contoso), app.MeLinkRequest_builder{
			Subject: "no-provider",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
	})

	t.Run("and nobody unasked-for may call it", func(t *testing.T) {
		x := require.New(t)

		// No frame at all, which is the deployment's own work -- and this is
		// one of the few writes that refuses it, because the row it writes is
		// defined by who is asking. There is nobody.
		_, err := s.Link(ctx, app.MeLinkRequest_builder{
			Provider: "entra",
			Subject:  "whoever",
		}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err))
	})
}
