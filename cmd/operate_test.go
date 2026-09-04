package cmd_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// operated is the service as `cmd.Grpc` wires it: over the **walled** stack, so
// the rule about who may write whose credential runs where it lives now -- in
// `server/core`, which `Reset` reaches through `Credential.Set`. The vouch
// service no longer carries the rule itself; it is a fact about the stack it
// writes through.
func (b *built) operated() *vouch.Server {
	return vouch.New(b.Ungated, b.Walled)
}

// mayCall gives somebody a binding across their tenant and answers with a
// context that calls as them.
func (b *built) mayCall(t *testing.T, ctx context.Context, who pdid.Id, alias string, methods ...string) context.Context {
	t.Helper()
	x := require.New(t)

	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   alias,
		Methods: methods,
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	return frame.Into(ctx, frame.New(who, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))
}

// TestNobodyWritesTheCredentialOfSomebodyWiderThanThey is item 11, and the
// order it went in is the point of it.
//
// Resetting a password is a way to **become** somebody. So an operator who may
// reset anybody in their tenant effectively holds every permission in it -- two
// operations, and it is exactly the shape `escalate.go` exists to close,
// arriving through a door nobody had put a lock on because the door did not
// exist yet. The list of twelve insisted the lock went in first, and this is
// the test that says it did.
func TestNobodyWritesTheCredentialOfSomebodyWiderThanThey(t *testing.T) {
	b, ctx := build(t)

	const erase = "/roster.HolderService/Erase"
	const list = "/roster.HolderService/List"

	// An administrator who may erase anybody, and an operator who may not.
	boss := b.holder(t, ctx, b.Contoso, "boss")
	b.mayCall(t, ctx, boss, "admin", erase, list)

	ops := b.holder(t, ctx, b.Contoso, "ops")
	asOps := b.mayCall(t, ctx, ops, "operator", list)

	// And somebody ordinary, with nothing.
	joe := b.holder(t, ctx, b.Contoso, "joe")

	set := func(c context.Context, who pdid.Id) error {
		_, err := b.Walled.Credential().Set(c, app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Secret: []byte("a new one"),
		}.Build())

		return err
	}

	t.Run("somebody who holds nothing may be written", func(t *testing.T) {
		x := require.New(t)

		x.NoError(set(asOps, joe), "an operator could not reset somebody with no permissions")
	})

	t.Run("and somebody narrower may be written", func(t *testing.T) {
		x := require.New(t)

		// A second operator holding the same one method.
		other := b.holder(t, ctx, b.Contoso, "other-ops")
		b.mayCall(t, ctx, other, "operator-2", list)

		x.NoError(set(asOps, other))
	})

	t.Run("and somebody wider may not", func(t *testing.T) {
		x := require.New(t)

		err := set(asOps, boss)
		x.Equal(codes.PermissionDenied, status.Code(err),
			"an operator became an administrator in two operations")
		x.Contains(status.Convert(err).Message(), erase,
			"the refusal did not say which permission was the problem")
	})

	t.Run("and the administrator may write the operator", func(t *testing.T) {
		x := require.New(t)

		asBoss := frame.Into(ctx, frame.New(boss, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))
		x.NoError(set(asBoss, ops))
	})

	// Changing your own is not becoming somebody else, and without this nobody
	// could change their own password unless they held everything they held --
	// which is true and is a strange way to write it. What your own row asks
	// instead is the password you hold: the same verb, one rule more, so that a
	// credential merely acting as you cannot replace what you sign in with.
	t.Run("and anybody may write their own, by proving the one they hold", func(t *testing.T) {
		x := require.New(t)

		// A first password is set *for* somebody -- here the operator way, with
		// no frame -- never by them with nothing to prove.
		asBossOwn := frame.Into(ctx, frame.New(boss, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))
		x.Equal(codes.PermissionDenied, status.Code(set(asBossOwn, boss)),
			"somebody set their own first password with nothing to prove")
		_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: boss.Bytes()}.Build(),
			Secret: []byte("a new one"),
		}.Build())
		x.NoError(err)

		_, err = b.Walled.Credential().Set(asBossOwn, app.CredentialSetRequest_builder{
			Ref:     app.HolderRef_builder{Id: boss.Bytes()}.Build(),
			Current: []byte("a new one"),
			Secret:  []byte("a newer one"),
		}.Build())
		x.NoError(err, "the administrator could not change their own password knowing the current one")
	})

	// The deployment's own work through an unwalled server -- `init`, the
	// sandbox, a migration. There is nobody to refuse: `Ungated` carries the
	// escalation rule like any stack, and `mayReach` reads a frameless call as
	// the deployment itself and passes it, which is the door `init` sets the
	// first administrator's password through.
	t.Run("and a call with no frame is the deployment itself", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: boss.Bytes()}.Build(),
			Secret: []byte("a new one"),
		}.Build())
		x.NoError(err, "init could not set the first administrator's password")
	})
}

// TestALocalOperatorHandsSomebodyAPassword is item 10, which is what an air gap
// has instead of a mail server.
//
// D13 closed `CredentialService` entirely, so nothing on the wire could set a
// password and `init` plus a shell was the only way. That is right for the read
// and it took the write with it -- and a deployment with no mail cannot live
// with it, because the person who delivers a recovery code is a person.
func TestALocalOperatorHandsSomebodyAPassword(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	joe := b.holder(t, ctx, b.Contoso, "joe")
	ops := b.holder(t, ctx, b.Contoso, "ops")
	asOps := b.mayCall(t, ctx, ops, "operator", "/roster.MeService/Get")

	v := b.operated()

	res, err := v.Reset(asOps, app.VouchResetRequest_builder{
		Who: app.VouchWho_builder{Id: joe.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.NotEmpty(res.GetSecret())

	t.Run("and it is the password now", func(t *testing.T) {
		x := require.New(t)

		got, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: joe.Bytes()}.Build(),
			Secret: []byte(res.GetSecret()),
		}.Build())
		x.NoError(err)
		x.True(got.GetOk())
	})

	t.Run("and it is not stored", func(t *testing.T) {
		x := require.New(t)

		row, err := b.Ent.Credential.Query().Only(ctx)
		x.NoError(err)
		x.NotContains(string(row.Secret), res.GetSecret(), "the plaintext was stored")
	})

	// Nobody chooses it, which is `IssueService`'s argument about a key: a
	// secret the caller chose is a secret the caller knows.
	t.Run("and the operator did not choose it", func(t *testing.T) {
		x := require.New(t)

		again, err := v.Reset(asOps, app.VouchResetRequest_builder{
			Who: app.VouchWho_builder{Id: joe.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		x.NotEqual(res.GetSecret(), again.GetSecret())
	})

	t.Run("and there is nothing to hand somebody for a second factor", func(t *testing.T) {
		x := require.New(t)

		_, err := v.Reset(asOps, app.VouchResetRequest_builder{
			Who:  app.VouchWho_builder{Id: joe.Bytes()}.Build(),
			Kind: "totp",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
	})
}

// TestALocalOperatorOpensAnAccountSomebodyElseClosed is the answer to the
// limitation D14 recorded and could not close from where it was.
//
// *An account can still be held closed by somebody else* -- ten wrong guesses
// every fifteen minutes, for as long as somebody cares to. Locking by name
// costs that, and the ways out are all somewhere else. A person on site is one
// of them.
func TestALocalOperatorOpensAnAccountSomebodyElseClosed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	joe := b.holder(t, ctx, b.Contoso, "joe")
	b.sets(t, ctx, joe, "correct horse battery staple")

	ops := b.holder(t, ctx, b.Contoso, "ops")
	asOps := b.mayCall(t, ctx, ops, "operator", "/roster.MeService/Get")

	v := b.operated()

	// Somebody else, holding it closed.
	for range vouch.MaxFailures {
		_, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: joe.Bytes()}.Build(),
			Secret: []byte("wrong"),
		}.Build())
		x.NoError(err)
	}

	shut, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: joe.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)
	x.False(shut.GetOk(), "the right password worked while the account was closed")
	x.NotNil(shut.GetLockedUntil())

	out, err := b.Walled.Credential().Unlock(asOps, app.CredentialUnlockRequest_builder{
		Ref: app.HolderRef_builder{Id: joe.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.NotNil(out.GetWasLockedUntil(), "an operator cannot tell 'I opened it' from 'it was not closed'")

	open, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: joe.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)
	x.True(open.GetOk(), "the account is still closed")

	// And the secret is untouched, which is what makes this a different act
	// from a reset: somebody locked out by an attacker gets their old password
	// back rather than a new one.
	t.Run("and an account that was open says so", func(t *testing.T) {
		x := require.New(t)

		again, err := b.Walled.Credential().Unlock(asOps, app.CredentialUnlockRequest_builder{
			Ref: app.HolderRef_builder{Id: joe.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		x.Nil(again.GetWasLockedUntil())
	})
}

// TestASecretSomebodyHasLostIsRefused is item 5 through the service, which is
// where the property that makes it roster's shows.
//
// Length and complexity are policy and stay with whoever collects the password.
// *This one is in a corpus of leaks* can only be answered where the plaintext
// is, and roster is the only thing that sees it.
func TestASecretSomebodyHasLostIsRefused(t *testing.T) {
	x := require.New(t)

	// The corpus is a fact about the deployment now, not about one server built
	// beside it: `Credential.Set` reads it through the same layer every write
	// goes through. So it is named in the config and written before the build
	// that loads and sorts it.
	sum := sha1.Sum([]byte("hunter2hunter2"))
	at := filepath.Join(t.TempDir(), "leaked.txt")
	x.NoError(os.WriteFile(at,
		[]byte(strings.ToUpper(hex.EncodeToString(sum[:]))+":12\n"), 0o600))

	b, ctx := build(t, func(c *cmd.Config) { c.Vouch.Breached = at })

	set := func(secret string) error {
		_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Secret: []byte(secret),
		}.Build())

		return err
	}

	// `FailedPrecondition` rather than `InvalidArgument`: there is nothing
	// wrong with the request, the world changed under the value in it.
	x.Equal(codes.FailedPrecondition, status.Code(set("hunter2hunter2")))
	x.NoError(set("correct horse battery staple"))

	// And a generated one is checked too, because nothing about being generated
	// makes it absent from a list -- it is the same path: `Reset` hands its
	// write to `Credential.Set`, which is where the corpus check now lives, so
	// the corpus travels through `b.Ungated`'s `core` stack and not a vouch
	// server built beside it.
	t.Run("and a reset goes through the same check", func(t *testing.T) {
		x := require.New(t)

		_, err := vouch.New(b.Ungated, b.Ungated).Reset(ctx,
			app.VouchResetRequest_builder{
				Who: app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
			}.Build())
		x.NoError(err, "thirty-two random bytes were in a corpus of leaks")
	})

	// A deployment that named no corpus refuses nothing, which is every
	// deployment that has not said otherwise -- a whole other build, since the
	// corpus is the one above's and not something a single call turns off.
	t.Run("and a deployment with no corpus checks nothing", func(t *testing.T) {
		x := require.New(t)

		nb, nctx := build(t)
		_, err := nb.Ungated.Credential().Set(nctx, app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: nb.ContosoUser.Bytes()}.Build(),
			Secret: []byte("hunter2hunter2"),
		}.Build())
		x.NoError(err)
	})
}
