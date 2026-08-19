package cmd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/vouch"
)

// disables somebody, through the stack that has the layer on it.
func disables(t *testing.T, ctx context.Context, b *keyedBuilt, who pdid.Id) {
	t.Helper()

	_, err := b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{
		Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)
}

func invalidates(t *testing.T, ctx context.Context, b *keyedBuilt, who pdid.Id) {
	t.Helper()

	_, err := b.Ungated.Holder().Invalidate(ctx, app.HolderInvalidateRequest_builder{
		Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)
}

// TestADisabledHolderIsNotToSignInAndNotSignedIn is the half of a new column
// that is not the column.
//
// Nothing generated reads a timestamp: not the wall, not the gate, not the
// erasure machinery. So `date_disabled` means whatever the refusals say it
// means and nothing until they are written -- and the app compiles and serves
// the whole time, which is what makes this the part worth a test rather than
// the schema.
//
// It has to reach two places that do not cover each other. `vouch` is where
// somebody signs in and has no frame; `cmd.Resolver` is where a credential
// already issued arrives and never sees a password. A person suspended at noon
// whose token was minted at eleven is the case the second one is for.
func TestADisabledHolderIsNotToSignInAndNotSignedIn(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	const listHolders = "/roster.HolderService/List"
	mayList(t, ctx, b, b.Who, listHolders)

	v := vouch.New(b.Ungated, b.Ungated)
	_, err := v.Set(ctx, app.VouchSetRequest_builder{
		Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	verifies := func() *app.VouchVerifyResponse {
		res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
		require.NoError(t, err)

		return res
	}

	// Minted while they are still allowed, which is the whole point: this is
	// the credential that has to stop working without being touched.
	held := delegates(t, ctx, b, b.Who, []string{listHolders}, 0)
	key := mintFor(t, ctx, b, b.Who, "hers", []string{listHolders}, time.Time{})

	c := app.NewHolderServiceClient(b.Conn)

	// A delegation goes beside the app's key and a tenant key goes alone, which
	// is the difference between saying who a call is about and saying who is
	// making it.
	list := func(token string) error {
		ctx := bearing(ctx, token)
		if strings.HasPrefix(token, keys.PrefixDelegation) {
			ctx = acting(t.Context(), b.Token, token)
		}

		_, err := c.List(ctx, app.HolderListRequest_builder{}.Build())

		return err
	}

	x.True(verifies().GetOk(), "the control: they can sign in to begin with")
	x.NoError(list(held), "and what they hold works")
	x.NoError(list(key))

	disables(t, ctx, b, b.Who)

	t.Run("they cannot sign in", func(t *testing.T) {
		x := require.New(t)

		res := verifies()
		x.False(res.GetOk())
		x.Nil(res.GetHolder())

		// And the refusal says nothing more than no. A lockout is the one
		// refusal this service distinguishes, because it admits the account
		// exists; a suspension must not become a second one.
		x.Nil(res.GetLockedUntil())
	})

	t.Run("and what they already held stops working", func(t *testing.T) {
		x := require.New(t)

		x.Equal(codes.Unauthenticated, status.Code(list(held)), "a delegation outlived the suspension")
		x.Equal(codes.Unauthenticated, status.Code(list(key)), "a tenant key outlived the suspension")
	})

	t.Run("and enabling puts all of it back", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Holder().Enable(ctx, app.HolderEnableRequest_builder{
			Ref: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		x.True(verifies().GetOk())
		x.NoError(list(held), "a suspension is not an erasure, so nothing was destroyed")
		x.NoError(list(key))
	})
}

// TestInvalidatingVoidsWhatWasIssuedBefore is "sign out everywhere" as one
// write, and the two things it deliberately treats differently.
//
// A registry of every app's live sessions would be a copy of state whose truth
// is elsewhere: ghosts when an app dies, disagreement with that app's own
// store, and other people's browser metadata in roster. One monotonic timestamp
// does the job, and each side says only what it holds the truth of -- roster
// answers *invalid since when*, an app answers *what is still alive*.
func TestInvalidatingVoidsWhatWasIssuedBefore(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	const listHolders = "/roster.HolderService/List"
	mayList(t, ctx, b, b.Who, listHolders)

	c := app.NewHolderServiceClient(b.Conn)

	// A delegation goes beside the app's key and a tenant key goes alone, which
	// is the difference between saying who a call is about and saying who is
	// making it.
	list := func(token string) error {
		ctx := bearing(ctx, token)
		if strings.HasPrefix(token, keys.PrefixDelegation) {
			ctx = acting(t.Context(), b.Token, token)
		}

		_, err := c.List(ctx, app.HolderListRequest_builder{}.Build())

		return err
	}

	before := delegates(t, ctx, b, b.Who, []string{listHolders}, 0)
	key := mintFor(t, ctx, b, b.Who, "hers", []string{listHolders}, time.Time{})
	x.NoError(list(before))
	x.NoError(list(key))

	// The stamp is the server's and is `time.Now()`, so a row written in the
	// same millisecond would tie. Sleeping is what makes "before" mean before
	// on a clock this coarse, rather than the test depending on which of two
	// writes the scheduler ran first.
	time.Sleep(2 * time.Millisecond)
	invalidates(t, ctx, b, b.Who)
	time.Sleep(2 * time.Millisecond)

	t.Run("what was issued before is void", func(t *testing.T) {
		x := require.New(t)

		x.Equal(codes.Unauthenticated, status.Code(list(before)))
	})

	t.Run("and what is issued after is not", func(t *testing.T) {
		x := require.New(t)

		after := delegates(t, ctx, b, b.Who, []string{listHolders}, 0)
		x.NoError(list(after), "signing back in has to work, or this is an erasure with another name")
	})

	// The decision worth pinning, because the other reading is defensible and
	// this one is what was chosen: a key is named, listed and revoked one at a
	// time, so killing somebody's scripts silently under "sign out everywhere"
	// would be an outage with nothing anywhere saying why. Revoking a key is a
	// second act and it has a second name.
	t.Run("and an api key is not touched", func(t *testing.T) {
		x := require.New(t)

		x.NoError(list(key), "an api key was revoked by something that did not name it")
	})

	// Nothing takes a time, so nothing can move it backwards. There is no undo
	// by construction, which is the property that makes a duplicate a no-op and
	// a missed message a matter of latency rather than correctness.
	t.Run("and doing it twice changes nothing", func(t *testing.T) {
		x := require.New(t)

		invalidates(t, ctx, b, b.Who)

		after := delegates(t, ctx, b, b.Who, []string{listHolders}, 0)
		x.NoError(list(after))
	})
}

// TestSuspendingIsItsOwnPermission is why these are three methods and not one
// field on `Update`.
//
// A role is a list of methods, so what a deployment can grant is exactly what
// it can name. Written as a field on the write a person makes about themselves,
// suspending somebody would be something anybody who could edit a profile could
// do -- and there would be no way to ask for it separately.
func TestSuspendingIsItsOwnPermission(t *testing.T) {
	b := keyFor(t, "/roster.HolderService/Update", "/roster.HolderService/Disable")
	ctx := t.Context()

	c := app.NewHolderServiceClient(b.Conn)
	ref := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()

	t.Run("what the key was given it may do", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Disable(bearing(ctx, b.Token), app.HolderDisableRequest_builder{Ref: ref}.Build())
		x.NoError(err)
	})

	t.Run("and what it was not it may not", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Enable(bearing(ctx, b.Token), app.HolderEnableRequest_builder{Ref: ref}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"a grant to suspend somebody carried a grant to reinstate them")

		_, err = c.Invalidate(bearing(ctx, b.Token), app.HolderInvalidateRequest_builder{Ref: ref}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
	})
}
