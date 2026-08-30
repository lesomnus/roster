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

// TestAPersonChangesTheirOwnPassword is the first of the layer refactor: the
// password write moving onto the entity it was always about, as a subject-less
// overlay a person calls about themselves.
//
// It proves the whole reopening at once: `CredentialService` is served for
// `ChangeMine` while its `Get` -- the verifier read D13 closed it for -- stays
// shut; the change is gated by reauth, not by which credential presents it; and
// the new password is the one that works afterwards.
func TestAPersonChangesTheirOwnPassword(t *testing.T) {
	const changeMine = "/roster.CredentialService/ChangeMine"

	x := require.New(t)
	b := keyFor(t, changeMine)
	ctx := t.Context()

	const old, next = "correct horse battery staple", "a whole new set of words entirely"

	// Alice has a password, set the operator way.
	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte(old),
	}.Build())
	x.NoError(err)

	// A role that names ChangeMine, and her own key holding it. Both are gates:
	// the key's list is checked by `auth`, the holder's role by the policy.
	permits(t, ctx, b, b.Contoso, b.Who, "self", changeMine)
	hers := mintFor(t, ctx, b, b.Who, "laptop", []string{changeMine}, time.Time{})
	cl := app.NewCredentialServiceClient(b.Conn)

	t.Run("the wrong current password is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := cl.ChangeMine(bearing(ctx, hers), app.CredentialChangeMineRequest_builder{
			Current: []byte("not it"),
			Secret:  []byte(next),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("the right current password changes it", func(t *testing.T) {
		x := require.New(t)

		_, err := cl.ChangeMine(bearing(ctx, hers), app.CredentialChangeMineRequest_builder{
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

	t.Run("the reopened service still never answers a verifier", func(t *testing.T) {
		x := require.New(t)

		// The read D13 closed the whole service for is still closed by method,
		// even to a key that may call ChangeMine on the same service.
		_, err := cl.Get(bearing(ctx, hers), app.CredentialGetRequest_builder{
			Ref: app.CredentialRef_builder{
				Kind: app.CredentialRefByKind_builder{
					Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
					Kind:   ptr("password"),
				}.Build(),
			}.Build(),
		}.Build())
		x.Error(err, "CredentialService.Get answered over the wire")
		x.NotEqual(codes.OK, status.Code(err))
	})
}
