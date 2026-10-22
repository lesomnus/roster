package cmd_test

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	entdelegation "github.com/lesomnus/roster/internal/ent/delegation"
	entlink "github.com/lesomnus/roster/internal/ent/link"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/vouch"
)

const (
	leaveLink    = "/roster.VouchService/LeaveLink"
	leaveByLink  = "/roster.VouchService/Leave"
	verifyEmail  = "/roster.EmailService/Verify"
	confirmEmail = "/roster.EmailService/Confirm"
)

// leavingBy is a deployment whose key mints and spends links that confirm
// somebody leaving -- and is allowed the other doors a link is spent at, so
// that their refusals are refusals and not a key that could not call them --
// where contoso lets its people leave and `someone` has an address they
// proved.
type leavingBy struct {
	*keyedBuilt
	c  app.VouchServiceClient
	as context.Context
}

func leavingFor(t *testing.T, with func(*cmd.Config)) *leavingBy {
	t.Helper()
	b := keyedFor(t, with, leaveLink, leaveByLink, link, redeem, verifyEmail, confirmEmail, listHolders,
		// And what hearing the sync stream takes (`syncing`).
		watchSync, invalidate)
	ctx := t.Context()
	offersLeave(t, ctx, b.Server, b.Contoso, true)
	proves(t, ctx, b.Server, b.Who, "someone@contoso.example")

	return &leavingBy{keyedBuilt: b, c: app.NewVouchServiceClient(b.Conn), as: bearing(ctx, b.Token)}
}

// mint is a link that confirms `who` leaving, mailed to `to`.
func (l *leavingBy) mint(t *testing.T, who pdid.Id, to string) (*app.VouchLeaveLinkResponse, error) {
	t.Helper()

	return l.c.LeaveLink(l.as, app.VouchLeaveLinkRequest_builder{
		Who:     app.VouchWho_builder{Id: who.Bytes()}.Build(),
		Address: to,
	}.Build())
}

func (l *leavingBy) minted(t *testing.T, who pdid.Id, to string) string {
	t.Helper()
	v, err := l.mint(t, who, to)
	require.NoError(t, err)

	return v.GetToken()
}

// leave spends one, with the alias typed.
func (l *leavingBy) leave(t *testing.T, token, confirm string) (*app.MeLeaveResponse, error) {
	t.Helper()

	return l.c.Leave(l.as, app.VouchLeaveRequest_builder{Token: token, Confirm: confirm}.Build())
}

// unspent says a link is still there to be spent.
func (l *leavingBy) unspent(t *testing.T, token string) bool {
	t.Helper()
	n, err := l.Ent.Link.Query().Where(entlink.Secret(sum256(token)), entlink.DateErasedIsNil()).Count(t.Context())
	require.NoError(t, err)

	return n == 1
}

// TestALinkThatConfirmsLeavingSignsNobodyIn is the link's whole worth: one
// account deleted, by whoever reads the mailbox it was sent to, and no way in.
// Every other door a link is spent at refuses it -- `Vouch.Redeem`, the user
// console, `Email.Confirm` -- and spends nothing doing so; and the door that
// spends it mints nothing for anybody.
func TestALinkThatConfirmsLeavingSignsNobodyIn(t *testing.T) {
	x := require.New(t)
	l := leavingFor(t, func(c *cmd.Config) { c.SignIn.Enabled = true })
	ctx := t.Context()
	answersAt(t, l.Server, l.Contoso, "contoso.example")

	made, err := l.mint(t, l.Who, "someone@contoso.example")
	x.NoError(err)
	x.True(strings.HasPrefix(made.GetToken(), vouch.PrefixLink))
	x.Equal("someone@contoso.example", made.GetAddress(), "the address roster holds, which is what is mailed")
	x.WithinDuration(time.Now().Add(vouch.LeaveFor), made.GetExpires().AsTime(), time.Minute)
	token := made.GetToken()

	t.Run("not at the door a recovery link is spent at", func(t *testing.T) {
		x := require.New(t)

		res, err := l.c.Redeem(l.as, app.VouchRedeemRequest_builder{Token: token, Methods: []string{listHolders}}.Build())
		x.NoError(err)
		x.False(res.GetVerified().GetOk(), "a link that confirms leaving signed somebody in")
		x.Empty(res.GetToken())
	})

	t.Run("nor at the user console", func(t *testing.T) {
		x := require.New(t)

		at := metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example"))
		_, err := app.NewAuthServiceClient(l.Conn).SignIn(at, app.AuthSignInRequest_builder{Link: token}.Build())
		x.Error(err, "a link that confirms leaving minted a session")
	})

	t.Run("nor at the door that confirms an address", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewEmailServiceClient(l.Conn).Confirm(l.as, app.EmailConfirmRequest_builder{Token: token}.Build())
		x.Equal(codes.NotFound, status.Code(err), "%v", err)
	})

	// None of them spent it, or it would not delete anything now.
	x.True(l.unspent(t, token), "a door that refused the link spent it")

	t.Run("and its own door deletes the account and mints nothing", func(t *testing.T) {
		x := require.New(t)

		before, err := l.Ent.Delegation.Query().Where(entdelegation.HolderIdEQ(l.Who.Uuid())).Count(ctx)
		x.NoError(err)

		v, err := l.leave(t, token, "someone")
		x.NoError(err)
		x.WithinDuration(time.Now(), v.GetDateErased().AsTime(), time.Minute)

		after, err := l.Ent.Delegation.Query().Where(entdelegation.HolderIdEQ(l.Who.Uuid())).Count(ctx)
		x.NoError(err)
		x.Equal(before, after, "spending it minted a credential")

		erased, invalidated := erasedAt(t, ctx, l.Server, l.Who)
		x.NotNil(erased)
		x.NotNil(invalidated, "erased without being signed out everywhere first")
	})
}

// TestALinkThatConfirmsLeavingIsGoodOnce: spent is gone, and only what was
// asked last spends it -- a mistyped alias costs nothing, and the link that
// deleted an account deletes nothing again.
func TestALinkThatConfirmsLeavingIsGoodOnce(t *testing.T) {
	x := require.New(t)
	l := leavingFor(t, nil)
	ctx := t.Context()
	token := l.minted(t, l.Who, "someone@contoso.example")

	for _, wrong := range []string{"", "Someone", "somebody"} {
		_, err := l.leave(t, token, wrong)
		x.Equal(codes.InvalidArgument, status.Code(err), "confirm %q", wrong)
	}
	x.True(l.unspent(t, token), "a mistyped alias spent the link")
	erased, invalidated := erasedAt(t, ctx, l.Server, l.Who)
	x.Nil(erased)
	x.Nil(invalidated, "a mistyped alias signed them out")

	_, err := l.leave(t, token, "someone")
	x.NoError(err)

	// Brought back by an operator, and the same link is still nothing.
	x.NoError(forget.Restore(ctx, l.Ent, l.Who, audit.Contents{}))
	_, err = l.leave(t, token, "someone")
	x.Equal(codes.NotFound, status.Code(err), "a link deleted an account twice")
	erased, _ = erasedAt(t, ctx, l.Server, l.Who)
	x.Nil(erased)
}

// TestALinkThatConfirmsLeavingAsksWhatLeavingAsks: every rule
// `MeService.Leave` applies but the age of a sign-in, both where the link is
// minted -- so that nothing is mailed that could not be spent -- and where it
// is spent, since anything may have changed in between. A refusal at the
// spend leaves the link where it was.
func TestALinkThatConfirmsLeavingAsksWhatLeavingAsks(t *testing.T) {
	l := leavingFor(t, nil)
	ctx := t.Context()
	at := app.TenantRef_builder{Id: l.Contoso.Bytes()}.Build()

	t.Run("a tenant that does not offer leaving", func(t *testing.T) {
		x := require.New(t)
		token := l.minted(t, l.Who, "someone@contoso.example")

		offersLeave(t, ctx, l.Server, l.Contoso, false)
		_, err := l.mint(t, l.Who, "someone@contoso.example")
		x.Equal(codes.PermissionDenied, status.Code(err), "a link was minted where nobody may leave")

		_, err = l.leave(t, token, "someone")
		x.Equal(codes.PermissionDenied, status.Code(err), "a link minted before deleted an account after")
		x.True(l.unspent(t, token))

		offersLeave(t, ctx, l.Server, l.Contoso, true)
	})

	t.Run("somebody the tenant's directory provisions", func(t *testing.T) {
		x := require.New(t)
		_, err := l.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
			Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/contoso/v2.0", ClientId: "the-app",
			SubjectClaim: z.Ptr("oid"), Provisions: z.Ptr(true),
		}.Build())
		x.NoError(err)

		mia := addHolder(t, ctx, l.Server, l.Contoso, "mia")
		proves(t, ctx, l.Server, mia, "mia@contoso.example")
		token := l.minted(t, mia, "mia@contoso.example")

		_, err = l.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
			Holder: app.HolderRef_builder{Id: mia.Bytes()}.Build(), Provider: "entra", Subject: erinOid,
		}.Build())
		x.NoError(err)

		_, err = l.mint(t, mia, "mia@contoso.example")
		x.Equal(codes.PermissionDenied, status.Code(err), "a link was minted for somebody the directory keeps")
		x.Contains(status.Convert(err).Message(), "directory")

		_, err = l.leave(t, token, "mia")
		x.Equal(codes.PermissionDenied, status.Code(err), "somebody the directory keeps left by a link minted before it did")
		x.True(l.unspent(t, token))
		erased, invalidated := erasedAt(t, ctx, l.Server, mia)
		x.Nil(erased)
		x.Nil(invalidated)
	})

	t.Run("a holder a file declares", func(t *testing.T) {
		x := require.New(t)
		v, err := l.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: at, Alias: "front", Labels: map[string]string{cmd.Declared: "config: account"},
		}.Build())
		x.NoError(err)
		front := mustId(t, v.GetId())
		proves(t, ctx, l.Server, front, "front@contoso.example")

		_, err = l.mint(t, front, "front@contoso.example")
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.Contains(status.Convert(err).Message(), "declared")
	})

	t.Run("an address that is not theirs and proved", func(t *testing.T) {
		x := require.New(t)
		_, err := l.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder: app.HolderRef_builder{Id: l.Who.Bytes()}.Build(), Address: "unchecked@contoso.example",
		}.Build())
		x.NoError(err)
		bob := addHolder(t, ctx, l.Server, l.Contoso, "bob")
		proves(t, ctx, l.Server, bob, "bob@contoso.example")

		for _, to := range []string{"unchecked@contoso.example", "bob@contoso.example", "nobody@contoso.example", ""} {
			_, err := l.mint(t, l.Who, to)
			x.Equal(codes.FailedPrecondition, status.Code(err), "a link was minted for %q", to)
		}

		// And one that was, and has stopped being theirs since: the mailbox it
		// went to is no proof of anything now.
		lee := addHolder(t, ctx, l.Server, l.Contoso, "lee")
		row := proves(t, ctx, l.Server, lee, "lee@contoso.example")
		token := l.minted(t, lee, "lee@contoso.example")
		_, err = l.Ungated.Email().Erase(ctx, app.EmailRef_builder{Id: row.GetId()}.Build())
		x.NoError(err)
		_, err = l.leave(t, token, "lee")
		x.Equal(codes.NotFound, status.Code(err), "a link deleted an account from an address taken off it")
	})

	t.Run("somebody suspended", func(t *testing.T) {
		x := require.New(t)
		kim := addHolder(t, ctx, l.Server, l.Contoso, "kim")
		proves(t, ctx, l.Server, kim, "kim@contoso.example")
		token := l.minted(t, kim, "kim@contoso.example")

		_, err := l.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{
			Ref: app.HolderRef_builder{Id: kim.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		_, err = l.mint(t, kim, "kim@contoso.example")
		x.Equal(codes.FailedPrecondition, status.Code(err))
		_, err = l.leave(t, token, "kim")
		x.Equal(codes.NotFound, status.Code(err))
	})

	t.Run("every other way of being wrong is one answer", func(t *testing.T) {
		x := require.New(t)

		// Expired: written directly, because the mint will not make one.
		stale, sum, err := staleLink()
		x.NoError(err)
		_, err = l.Ungated.Link().Add(ctx, app.LinkAddRequest_builder{
			Holder:      app.HolderRef_builder{Id: l.Who.Bytes()}.Build(),
			Email:       app.EmailRef_builder{Id: l.address(t, "someone@contoso.example")}.Build(),
			Leave:       z.Ptr(true),
			Secret:      sum,
			Issuer:      issuerOf(t, ctx, l.keyedBuilt),
			DateExpires: timestamppb.New(time.Now().Add(-time.Minute)),
		}.Build())
		x.NoError(err)

		// A recovery link, which is a way in and not this; and a verification of
		// the very address this kind is mailed to, which names it as this kind
		// does and confirms it instead.
		recovery, err := l.c.Link(l.as, app.VouchLinkRequest_builder{
			Who: app.VouchWho_builder{Id: l.Who.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		verification, err := app.NewEmailServiceClient(l.Conn).Verify(l.as, app.EmailVerifyRequest_builder{
			Ref: app.EmailRef_builder{Id: l.address(t, "someone@contoso.example")}.Build(),
		}.Build())
		x.NoError(err)

		for _, tc := range []struct{ desc, token string }{
			{"never here", vouch.PrefixLink + "nothing-at-all"},
			{"not one of ours", "not-even-prefixed"},
			{"expired", stale},
			{"a recovery link", recovery.GetToken()},
			{"a verification", verification.GetToken()},
		} {
			_, err := l.leave(t, tc.token, "someone")
			x.Equal(codes.NotFound, status.Code(err), tc.desc)
		}

		// And one app does not spend what another was issued.
		mine := l.minted(t, l.Who, "someone@contoso.example")
		theirs := keyed(t, ctx, l.keyedBuilt, "another-app", []string{leaveLink, leaveByLink})
		_, err = l.c.Leave(bearing(ctx, theirs), app.VouchLeaveRequest_builder{Token: mine, Confirm: "someone"}.Build())
		x.Equal(codes.NotFound, status.Code(err), "one app spent what another was issued")
		x.True(l.unspent(t, mine))

		erased, _ := erasedAt(t, ctx, l.Server, l.Who)
		x.Nil(erased)
	})
}

// TestALinkThatConfirmsLeavingLeavesAsThePerson: what the link ends in is the
// erase `MeService.Leave` makes -- as the person, so the trail says it was
// them; heard by every app; and told when it will be forgotten.
func TestALinkThatConfirmsLeavingLeavesAsThePerson(t *testing.T) {
	x := require.New(t)
	l := leavingFor(t, func(c *cmd.Config) { c.Holder.ForgetAfter = 720 * time.Hour })
	ctx, stop := context.WithCancel(t.Context())
	defer stop()

	// Served with the configuration it was built from, for the forget window.
	g, err := l.Grpc(ctx, l.Config)
	x.NoError(err)
	l.Conn = pdtest.Serve(t, g)
	l.c = app.NewVouchServiceClient(l.Conn)

	heard := syncing(t, bearing(ctx, l.Token), l.Conn, addHolder(t, ctx, l.Server, l.Contoso, "canary"))
	token := l.minted(t, l.Who, "someone@contoso.example")

	v, err := l.leave(t, token, "someone")
	x.NoError(err)
	x.WithinDuration(v.GetDateErased().AsTime().Add(720*time.Hour), v.GetDateForgotten().AsTime(), time.Second)

	ev := about(t, heard, l.Who)
	x.Equal(app.SyncReason_SYNC_REASON_ERASED, ev.GetReason())

	// Signed out everywhere and erased, each a line of the trail under the
	// call that did it, and each the person's own act.
	rows, err := l.Ent.Audit.Query().
		Where(entaudit.ObjectIdEQ(l.Who.Uuid()), entaudit.ActionEQ(leaveByLink)).
		All(ctx)
	x.NoError(err)
	x.Len(rows, 2, "%v", rows)
	for _, v := range rows {
		x.Equal(l.Who.Uuid(), v.ActorId, "the trail says somebody else made them leave")
	}

	// And the spend is on it as well, as the act of whoever presented the link.
	spent, err := l.Ent.Audit.Query().
		Where(entaudit.ActionEQ(leaveByLink), entaudit.ObjectIdNEQ(l.Who.Uuid())).
		Count(ctx)
	x.NoError(err)
	x.Equal(1, spent, "the link was spent with nothing on the trail")
}

// sum256 is what a link is found by.
func sum256(token string) []byte {
	v := sha256.Sum256([]byte(token))

	return v[:]
}

// address is the identifier of an address somebody has.
func (l *leavingBy) address(t *testing.T, address string) []byte {
	t.Helper()
	v, err := l.Ungated.Email().Get(t.Context(), app.EmailGetRequest_builder{
		Ref: app.EmailRef_builder{At: app.EmailRefByAt_builder{TenantId: l.Contoso.Bytes(), Address: z.Ptr(address)}.Build()}.Build(),
	}.Build())
	require.NoError(t, err)

	return v.GetId()
}

// TestALinkThatConfirmsLeavingIsATerminalCommandAsWell: what a front door does
// with these two methods, a shell does with its key -- the link printed once,
// on stdout, with where it may go; and spent from stdin, never an argument.
func TestALinkThatConfirmsLeavingIsATerminalCommandAsWell(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, leaveLink, leaveByLink)
	ctx := t.Context()
	tn := mustId(t, b.Tenant.GetId())
	offersLeave(t, ctx, b.Server, tn, true)
	alice := mustId(t, b.Alice.GetId())
	proves(t, ctx, b.Server, alice, "alice@newco.example")

	_, err := pipedOut(t, "", cli.Cmd(&b.Hers), "vouch", "leave-link", "@newco/alice")
	x.ErrorContains(err, "--to", "a link was minted with nowhere said to send it")

	token, said, err := pipedErr(t, "", cli.Cmd(&b.Hers), "vouch", "leave-link", "--to", "alice@newco.example", "@newco/alice")
	x.NoError(err)
	x.True(strings.HasPrefix(token, vouch.PrefixLink), "%q", token)
	x.Contains(said, "alice@newco.example")

	_, err = pipedOut(t, token+"\n", cli.Cmd(&b.Hers), "vouch", "leave", "--token-stdin", "--confirm", "alicee")
	x.Error(err, "a mistyped alias deleted an account")

	_, said, err = pipedErr(t, token+"\n", cli.Cmd(&b.Hers), "vouch", "leave", "--token-stdin", "--confirm", "alice")
	x.NoError(err)
	x.Contains(said, "erased at")
	erased, _ := erasedAt(t, ctx, b.Server, alice)
	x.NotNil(erased)
}
