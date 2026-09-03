package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// TestAPersonChangesTheirOwnPassword is the self-service password change as the
// line has it (CLAUDE.md, *no self-only twin of a verb*): no verb of its own.
// A person calls `Credential.Set` with their **own** reference -- the same verb
// an operator calls about somebody else -- and the layer asks them for the
// password they hold before writing the one they chose.
//
// It proves the whole reopening at once: `CredentialService` is served for
// `Set` while its `Get` -- the verifier read D13 closed it for -- stays shut;
// the change is gated by reauth, not by which credential presents it; the new
// password is the one that works afterwards; and a wrong `current` costs what a
// wrong sign-in costs, so it cannot be guessed at any faster.
func TestAPersonChangesTheirOwnPassword(t *testing.T) {
	const set = "/roster.CredentialService/Set"

	x := require.New(t)
	b := keyFor(t, set)
	ctx := t.Context()

	const old, next = "correct horse battery staple", "a whole new set of words entirely"

	own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()

	// Alice has a password, set the operator way: no frame, so nobody's own row
	// and nothing to prove.
	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    own,
		Secret: []byte(old),
	}.Build())
	x.NoError(err)

	// A role that names Set, and her own key holding it. Both are gates: the
	// key's list is checked by `auth`, the holder's role by the policy. What
	// neither says is *whose row* -- that is the layer's, below.
	permits(t, ctx, b, b.Contoso, b.Who, "self", set)
	hers := mintFor(t, ctx, b, b.Who, "laptop", []string{set}, time.Time{})
	cl := app.NewCredentialServiceClient(b.Conn)

	t.Run("her own row, without the current password, is refused", func(t *testing.T) {
		x := require.New(t)

		// The whole point: a credential that merely acts as her -- this key,
		// were it lifted from a build log -- can name her row and still not
		// replace what she signs in with.
		_, err := cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
			Ref:    own,
			Secret: []byte(next),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("the wrong current password is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
			Ref:     own,
			Current: []byte("not it"),
			Secret:  []byte(next),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("the right current password changes it", func(t *testing.T) {
		x := require.New(t)

		_, err := cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
			Ref:     own,
			Current: []byte(old),
			Secret:  []byte(next),
		}.Build())
		x.NoError(err)

		// The new one is what verifies now, and the old one is not.
		v := vouch.New(b.Ungated, b.Ungated)
		res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(), Secret: []byte(next),
		}.Build())
		x.NoError(err)
		x.True(res.GetOk(), "the new password does not work")

		res, err = v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(), Secret: []byte(old),
		}.Build())
		x.NoError(err)
		x.False(res.GetOk(), "the old password still works")
	})

	t.Run("somebody else's row is the operator write, and asks for no current", func(t *testing.T) {
		x := require.New(t)

		// RBAC as it is: a role naming `Set` reaches anybody no wider than the
		// caller, and mate holds nothing. What changes for another's row is
		// only that `current` is not hers to give and is refused if she does.
		mate, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:  "mate",
		}.Build())
		x.NoError(err)
		theirs := app.HolderRef_builder{Id: mate.GetId()}.Build()

		_, err = cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
			Ref:     theirs,
			Current: []byte(next),
			Secret:  []byte("for mate"),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "current was accepted for somebody else's row")

		_, err = cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
			Ref:    theirs,
			Secret: []byte("for mate"),
		}.Build())
		x.NoError(err, "an operator write to somebody no wider than the caller was refused")
	})

	t.Run("the reopened service still never answers a verifier", func(t *testing.T) {
		x := require.New(t)

		// The read D13 closed the whole service for is still closed by method,
		// even to a key that may call Set on the same service.
		_, err := cl.Get(bearing(ctx, hers), app.CredentialGetRequest_builder{
			Ref: app.CredentialRef_builder{
				Kind: app.CredentialRefByKind_builder{
					Holder: own,
					Kind:   ptr("password"),
				}.Build(),
			}.Build(),
		}.Build())
		x.Error(err, "CredentialService.Get answered over the wire")
		x.NotEqual(codes.OK, status.Code(err))
	})

	t.Run("guessing the current password locks the account like guessing at a sign-in", func(t *testing.T) {
		x := require.New(t)

		// `ChangeMine` compared without counting, so a lifted delegation could
		// guess at leisure. Now each wrong `current` is a wrong sign-in, and
		// after enough of them even the right one is refused until the lock
		// lifts.
		for range vouch.MaxFailures {
			_, err := cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
				Ref:     own,
				Current: []byte("still not it"),
				Secret:  []byte("whatever comes next"),
			}.Build())
			x.Equal(codes.PermissionDenied, status.Code(err))
		}

		_, err := cl.Set(bearing(ctx, hers), app.CredentialSetRequest_builder{
			Ref:     own,
			Current: []byte(next),
			Secret:  []byte("whatever comes next"),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "the right password got through a lockout")
		x.Contains(status.Convert(err).Message(), "locked")
	})
}
