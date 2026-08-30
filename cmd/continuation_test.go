package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// halfway is somebody signed in as far as the first factor.
//
// A whole deployment for it, because a continuation belongs to the caller that
// opened it and the issuer is the key row a request arrives as -- so an attempt
// begun anywhere but through the wire is an attempt this cannot then spend.
func halfway(t *testing.T) (*keyedBuilt, []byte, string) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	b := keyFor(t, delegate)
	seed := enrolled(t, ctx, b.Ungated.Credential(), b.keyed(t), b.Who)

	first, err := app.NewVouchServiceClient(b.Conn).Delegate(bearing(ctx, b.Token),
		app.VouchDelegateRequest_builder{
			Who:     app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret:  []byte("correct horse battery staple"),
			Methods: []string{delegate},
		}.Build())
	x.NoError(err)

	handle := first.GetVerified().GetContinuation()
	x.NotEmpty(handle, "the control: the password left an attempt open")
	x.Empty(first.GetToken(), "the control: one factor minted nothing")

	return b, seed, handle
}

// TestAnAttemptIsNotFinishedForSomebodySuspendedHalfWayThrough is the window a
// second factor opens and nothing else in the app has.
//
// Every other refusal of somebody who may no longer sign in is decided while
// they are presenting a secret: `vouch.verify` reads `date_disabled` off the
// credential's holder, and `cmd.Resolver` reads it again for a credential that
// was already issued. A continuation is neither of those. It is a *proof
// already made*, written down minutes ago, and the second form quotes it back
// rather than proving anything about the person again -- so who they are was
// asked once, at the first form, and the answer the second form works from is
// that stale one.
//
// `ContinueFor` bounds the staleness at five minutes, and five minutes is
// exactly the shape an administrator acts in: a laptop is taken while somebody
// is stood at the code prompt, the account is suspended, and the suspension has
// to land on the attempt that is open rather than only on the next one.
// Without the check in `continuation` the bound is the only thing there is.
//
// A token minted inside that window would not go on to work -- `keys.findKey`
// refuses a suspended holder, so the delegation is dead on presentation. What
// it would be is a sign-in that succeeded: an app told yes, a session opened on
// the strength of it, a delegation row written, and an `Audit` entry saying
// somebody signed in minutes after they were suspended. Which of the two
// answers a person gets is the question, and the layer that already knows is
// this one.
//
// So this presents the **correct** code after the suspension. The correct one
// on purpose: every other way of failing a continuation -- a wrong code, a
// spent handle, another app's -- already answers no, so a test that got the
// code wrong would pass against a build with this refusal deleted.
//
// # Suspension and not erasure
//
// The `if` this pins names both, and only the suspended half of it can be
// observed from out here. An erasure narrows every reference composed through
// the holder, so one line further on `credentialNamed` answers NotFound and the
// step refuses anyway; deleting the check changes nothing an erased person can
// do, and `TestAnErasedHolderCannotAuthenticate` is where that narrowing is
// pinned. A suspension is the case with nothing standing behind it: it changes
// one column, leaves every row reachable, and nothing generated has ever read
// it. If this refusal is not made here it is not made at all.
func TestAnAttemptIsNotFinishedForSomebodySuspendedHalfWayThrough(t *testing.T) {
	x := require.New(t)
	b, seed, handle := halfway(t)
	ctx := t.Context()

	disables(t, ctx, b, b.Who)

	res, err := app.NewVouchServiceClient(b.Conn).Delegate(bearing(ctx, b.Token),
		app.VouchDelegateRequest_builder{
			Continuation: handle,
			Kind:         vouch.KindTotp,
			Secret:       []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
			Methods:      []string{delegate},
		}.Build())
	x.NoError(err)

	x.False(res.GetVerified().GetOk(),
		"a suspension arrived between the two forms and the second one finished anyway")
	x.Empty(res.GetToken(), "a suspended holder was minted a credential")

	// Nothing written at all, rather than a credential a caller reading only
	// `ok` would have gone on using. `Delegate` writes exactly one row on a
	// yes, so counting is what says the answer above is a refusal and not a
	// response that merely forgot to carry its token back.
	n, err := b.Ent.Delegation.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n, "a row was written for a sign-in that was refused")

	// And the refusal is the plain one. A continuation nobody may spend answers
	// alike whether it was never real, has expired, was spent, or belongs to
	// somebody who has just been suspended -- told apart, the second form
	// becomes a way of asking after the standing of an account by quoting a
	// handle at it.
	x.Nil(res.GetVerified().GetLockedUntil())
	x.Empty(res.GetVerified().GetContinuation(),
		"a refused attempt handed back a handle to try again with")
}
