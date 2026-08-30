package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/z"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// What a kind is allowed to cost, and which ones may be written down.
//
// `server/vouch/kind.go` is the subject: a kind is checked its own way and
// **burns its own way**, and the reason it exists at all is that the moment a
// second kind arrived, every refusal stopped costing the same. These are the
// two places that was still true after it was written -- one where the refusal
// is a status code rather than a clock, and one where a kind nobody can check
// was written down and could never be taken back.

// TestAKindNothingChecksIsRefusedBeforeAnybodyIsLookedFor.
//
// `Verify` resolved the address **first** and asked what checks the kind
// second, so the two refusals arrived in the wrong order: an address that is
// here got `InvalidArgument` off `verifierOf` and an address that is not got
// the `no()` every stranger gets. That is the question D14 closed -- *is there
// an account here* -- answered exactly rather than statistically, and answered
// by anybody who can reach the sign-in form, since no frame is needed to ask.
//
// `step` has always read the other way round, and this is the same shape now:
// what this deployment can check is a fact about the deployment, so it is
// settled before anybody is looked for.
func TestAKindNothingChecksIsRefusedBeforeAnybodyIsLookedFor(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: "someone@contoso.example",
	}.Build())
	x.NoError(err)

	b.sets(t, ctx, b.ContosoUser, "correct horse battery staple")

	ask := func(v *vouch.Server, kind, address string) error {
		t.Helper()

		_, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Tenant: "contoso", Address: address}.Build(),
			Kind:   kind,
			Secret: []byte("hunter2"),
		}.Build())

		return err
	}

	// Somebody who is here and somebody who is not, asked for a kind this has
	// never heard of. One answer, or the difference is the answer.
	t.Run("a kind nothing checks", func(t *testing.T) {
		x := require.New(t)
		v := b.vouched()

		here := ask(v, "recovery", "someone@contoso.example")
		gone := ask(v, "recovery", "nobody@contoso.example")

		x.Equal(codes.InvalidArgument, status.Code(here))
		x.Equal(status.Code(here), status.Code(gone),
			"an unknown kind told a stranger apart from somebody real")
	})

	// And the same for a kind that is real and that **this** deployment cannot
	// check: no keyring, so no second factor. `kind.go` chose `Unimplemented`
	// for that on purpose, and a fact about the deployment must not be
	// answerable only for people who exist.
	t.Run("and a kind this deployment cannot check", func(t *testing.T) {
		x := require.New(t)
		v := b.vouched()

		here := ask(v, vouch.KindTotp, "someone@contoso.example")
		gone := ask(v, vouch.KindTotp, "nobody@contoso.example")

		x.Equal(codes.Unimplemented, status.Code(here))
		x.Equal(status.Code(here), status.Code(gone),
			"a deployment's own limit told a stranger apart from somebody real")
	})

	// The other half of the same finding, and the one `kind.go` was written
	// for: the burn on the unknown-address path was the **package** argon2 one
	// whatever kind was asked for. So on a deployment that does hold second
	// factors, `totp` against an address nobody has cost forty milliseconds and
	// `totp` against somebody with no second factor cost microseconds -- the
	// inversion `kind.go` exists to close, reintroduced one branch earlier.
	//
	// Measured as **one kind against the other**, rather than against an argon2
	// burn timed on its own.
	//
	// Both calls take the same path and do the same two reads -- the tenant and
	// the address -- so whatever those cost cancels, and what is left is the
	// burn. Against a number written here, or against a bare argon2, the reads
	// would be inside the budget: on a remote Postgres two round trips are wide
	// enough to fail a test about a comparison that never ran.
	t.Run("and an address nobody has burns what the kind would have burned", func(t *testing.T) {
		x := require.New(t)
		v := b.keyed2fa(t)

		fastest := func(n int, kind string) time.Duration {
			out := time.Duration(1<<62 - 1)
			for range n {
				at := time.Now()
				_, _ = v.Verify(ctx, app.VouchVerifyRequest_builder{
					Who:    app.VouchWho_builder{Tenant: "contoso", Address: "nobody@contoso.example"}.Build(),
					Kind:   kind,
					Secret: []byte("000000"),
				}.Build())
				if d := time.Since(at); d < out {
					out = d
				}
			}

			return out
		}

		password := fastest(3, vouch.KindPassword)
		totp := fastest(3, vouch.KindTotp)

		x.Less(totp, password/2,
			"an address nobody has burned argon2 for a kind that compares in microseconds")
	})
}

// TestSetWritesOnlyAKindThisCanCheck.
//
// `Set` wrote the `kind` column exactly as it arrived. Nothing else in this
// service does: `Verify` and `Continue` refuse a kind `verifierOf` does not
// know, `Reset` refuses anything but a password and `Enrol` anything but a
// second factor.
//
// What that cost is not a stray row. `factors` offers every confirmed
// credential a person has, so a phantom kind is offered by every framed sign-in
// from then on; `answer` sets `ok` only when there is nothing left to prove, so
// it never does; and `Continue` refuses the very kind it was just offered. The
// person cannot finish a sign-in again -- and nothing can take the row back,
// because `CredentialService` is unregistered and closed to the batch, `Reset`
// refuses the kind and `Enrol` refuses it too. One typo in an admin console is
// an account that needs a shell on the database.
func TestSetWritesOnlyAKindThisCanCheck(t *testing.T) {
	b, ctx := build(t)

	set := func(kind string) error {
		t.Helper()

		_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Kind:   kind,
			Secret: []byte("correct horse battery staple"),
		}.Build())

		return err
	}

	t.Run("a kind nothing checks is refused", func(t *testing.T) {
		x := require.New(t)

		err := set("recovery")
		x.Equal(codes.InvalidArgument, status.Code(err))

		n, err := b.Ent.Credential.Query().Count(ctx)
		x.NoError(err)
		x.Zero(n, "a row nothing on any plane can remove was written")
	})

	// A second factor is real and is still not this call's: `Set` argon2-hashes
	// what it is handed and a seed must be read back, so a `totp` row written
	// here is a factor that can never answer. `vouch.proto` says so under
	// `Enrol` and nothing enforced it.
	//
	// One call, not two: this is `Credential.Set` now, which never consults a
	// keyring -- the settable-kind check is a fact about the kind, so a
	// deployment that holds a key to wrap a seed with is refused for the same
	// reason as one that does not, and there is no second path to demonstrate.
	t.Run("and a second factor is Enrol's", func(t *testing.T) {
		x := require.New(t)

		x.Equal(codes.InvalidArgument, status.Code(set(vouch.KindTotp)))

		n, err := b.Ent.Credential.Query().Count(ctx)
		x.NoError(err)
		x.Zero(n, "an argon2 hash was written where a wrapped seed belongs")
	})

	// And the consequence, which is what makes this worth refusing rather than
	// tolerating: somebody who has one of these can still finish signing in.
	t.Run("and a sign-in still finishes", func(t *testing.T) {
		x := require.New(t)

		b.sets(t, ctx, b.ContosoUser, "correct horse battery staple")

		// Framed, because a continuation is minted for whoever asked and it is
		// the continuation path that a phantom factor blocks.
		res, err := b.vouched().Verify(b.as(ctx, b.ContosoUser, b.Contoso), app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
		x.NoError(err)
		x.True(res.GetOk(), "a kind nobody can prove was left to prove")
		x.Empty(res.GetAvailable())
	})
}

// TestASecretIsResetForTheAddressThatNamesSomebody.
//
// `VouchWho.address` is *what most sign-in forms actually collect*, and every
// sibling that still takes a `VouchWho` resolves it: `Verify`, `Unlock`,
// `Enrol`, `Link`, and -- the operator's recovery flow -- `Reset`. Setting a
// secret is `Credential.Set` now and names a holder by reference (option 2), so
// the address form stays where it belongs: resetting by email is how somebody
// who cannot get in is given a way back, and it is the write where the form in
// front of the operator collects an address rather than a key.
//
// `Reset` resolves `refOf`'s nil answer -- its way of saying *this one is a
// lookup* -- through a `byAddress` lookup, and it asked a second time to know
// whose epoch to move. The bug this pins is that the two disagreed: `refOf`
// answers nil for an address, so a reset by email changed the password and left
// every stolen session alive, on the one form an operator actually uses.
func TestASecretIsResetForTheAddressThatNamesSomebody(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Address: "someone@contoso.example",
	}.Build())
	x.NoError(err)

	who := func(address string) *app.VouchWho {
		return app.VouchWho_builder{Tenant: "contoso", Address: address}.Build()
	}

	v := b.vouched()

	// A reset by address is a **whole** reset, which is the half that was nearly
	// lost making the other half work.
	//
	// D26 keeps the epoch off a self-service change on purpose -- somebody
	// changing their own password must not sign themselves out of everything
	// with nothing having said so -- and puts it on `Reset`, because that is
	// somebody else giving them a new one, which is where recovery from a
	// takeover happens. So the sessions the takeover opened have to go with it.
	before, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Select: app.HolderSelect_builder{DateInvalidated: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Nil(before.GetDateInvalidated())

	res, err := v.Reset(ctx, app.VouchResetRequest_builder{Who: who("someone@contoso.example")}.Build())
	x.NoError(err)
	x.NotEmpty(res.GetSecret())

	// And it is that person's secret, rather than a row hanging off nothing.
	x.True(b.verifies(t, ctx, b.ContosoUser, res.GetSecret()).GetOk())

	after, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Select: app.HolderSelect_builder{DateInvalidated: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.NotNil(after.GetDateInvalidated(),
		"a reset by email left every session it was recovering from alive")

	// And through the stack `cmd.Grpc` actually wires, which is not the one
	// above: `b.vouched()` is the unwalled server with no escalation rule on
	// it, so a fix tested only there is a fix tested with both of the things
	// that could refuse it taken away. `b.operated()` is the walled server
	// with `core.Reaching`, which is what a deployment serves.
	t.Run("and through the stack a deployment serves", func(t *testing.T) {
		x := require.New(t)

		v := b.operated()
		as := b.as(ctx, b.ContosoUser, b.Contoso)

		res, err := v.Reset(as, app.VouchResetRequest_builder{
			Who: who("someone@contoso.example"),
		}.Build())
		x.NoError(err)
		x.NotEmpty(res.GetSecret())
		x.True(b.verifies(t, ctx, b.ContosoUser, res.GetSecret()).GetOk())
	})

	// An address nobody has is `NotFound`, which is what every other call that
	// looks one up answers. This is behind the wall and the caller is an
	// operator who already knows the account exists, so there is nothing here
	// to keep from them -- what D14 protects is the sign-in path.
	t.Run("and an address nobody has is not found", func(t *testing.T) {
		x := require.New(t)

		_, err := v.Reset(ctx, app.VouchResetRequest_builder{
			Who: who("nobody@contoso.example"),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})
}
