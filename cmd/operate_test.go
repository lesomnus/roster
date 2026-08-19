package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/vouch"
)

// operated is the service as `cmd.Grpc` wires it: with the rule about who may
// write whose credential, which is the half of this that is not the surface.
func (b *built) operated() *vouch.Server {
	return vouch.New(b.Ungated, b.Walled, vouch.WithReach(core.Reaching(cmd.Rules(b.Ent))))
}

// mayCall gives somebody a binding across their tenant and answers with a
// context that calls as them.
func (b *built) mayCall(t *testing.T, ctx context.Context, who pdid.Id, alias string, methods ...string) context.Context {
	t.Helper()
	x := require.New(t)

	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:   alias,
		Methods: methods,
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	return frame.Into(ctx, frame.New(who, b.Acme, frame.Whole()).WithScope(frame.Only(b.Acme)))
}

// TestNobodyWritesTheCredentialOfSomebodyWiderThanThey is item 11, and the
// order it went in is the point of it.
//
// Resetting a password is a way to **become** somebody. So an operator who may
// reset anybody in their tenant effectively holds every permission in it -- two
// operations, and it is exactly the shape `escalate.go` exists to close,
// arriving through a door nobody had put a lock on because the door did not
// exist yet. PLAN.md's list insisted the lock went in first, and this is the
// test that says it did.
func TestNobodyWritesTheCredentialOfSomebodyWiderThanThey(t *testing.T) {
	b, ctx := build(t)

	const erase = "/roster.HolderService/Erase"
	const list = "/roster.HolderService/List"

	// An administrator who may erase anybody, and an operator who may not.
	boss := b.holder(t, ctx, b.Acme, "boss")
	b.mayCall(t, ctx, boss, "admin", erase, list)

	ops := b.holder(t, ctx, b.Acme, "ops")
	asOps := b.mayCall(t, ctx, ops, "operator", list)

	// And somebody ordinary, with nothing.
	joe := b.holder(t, ctx, b.Acme, "joe")

	v := b.operated()
	set := func(c context.Context, who pdid.Id) error {
		_, err := v.Set(c, app.VouchSetRequest_builder{
			Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
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
		other := b.holder(t, ctx, b.Acme, "other-ops")
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

		asBoss := frame.Into(ctx, frame.New(boss, b.Acme, frame.Whole()).WithScope(frame.Only(b.Acme)))
		x.NoError(set(asBoss, ops))
	})

	// Changing your own is not becoming somebody else, and without this nobody
	// could change their own password unless they held everything they held --
	// which is true and is a strange way to write it.
	t.Run("and anybody may write their own", func(t *testing.T) {
		x := require.New(t)

		asBossOwn := frame.Into(ctx, frame.New(boss, b.Acme, frame.Whole()).WithScope(frame.Only(b.Acme)))
		x.NoError(set(asBossOwn, boss))
	})

	// The deployment's own work through an unwalled server -- `init`, the
	// sandbox, a migration. There is nobody to refuse, and the stack is the one
	// those actually use: `vouch.New(Ungated, Ungated)`, where the read the
	// walled half would make has no frame to narrow by either.
	t.Run("and a call with no frame is the deployment itself", func(t *testing.T) {
		x := require.New(t)

		own := vouch.New(b.Ungated, b.Ungated, vouch.WithReach(core.Reaching(cmd.Rules(b.Ent))))

		_, err := own.Set(ctx, app.VouchSetRequest_builder{
			Who:    app.VouchWho_builder{Id: boss.Bytes()}.Build(),
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

	joe := b.holder(t, ctx, b.Acme, "joe")
	ops := b.holder(t, ctx, b.Acme, "ops")
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

	joe := b.holder(t, ctx, b.Acme, "joe")
	b.sets(t, ctx, joe, "correct horse battery staple")

	ops := b.holder(t, ctx, b.Acme, "ops")
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

	out, err := v.Unlock(asOps, app.VouchUnlockRequest_builder{
		Who: app.VouchWho_builder{Id: joe.Bytes()}.Build(),
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

		again, err := v.Unlock(asOps, app.VouchUnlockRequest_builder{
			Who: app.VouchWho_builder{Id: joe.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		x.Nil(again.GetWasLockedUntil())
	})
}
