package cmd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/audit"

	"github.com/lesomnus/roster/cli"
	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/keys"
)

// TestARestoreGivesBackTheAccountAndNotTheSignIns: an erase stops everything
// issued to somebody, and a restore cleared the one column and nothing else.
// So a delegation minted before an operator's erase answered again after
// `roster restore`, and so did a recovery link still inside its quarter hour:
// each `Unauthenticated`, or refused, while they were erased, and OK once they
// were back. An account is erased because somebody else was in it as often as
// by mistake, and nothing could be done first, since somebody erased cannot be
// signed out everywhere (`Invalidate` of them is `NotFound`). Somebody who
// left on their own had no such thing to come back to: `MeService/Leave` signs
// out everywhere before it erases.
//
// The two agree now. A restore stamps the moment everything issued before it
// is void, as `Invalidate` does, and what comes back is the account: its
// rows, its ways in, its roles. A sign-in made after it is one.
//
// A key is not ended by it, as it is not ended by signing out everywhere --
// it is named, listed and revoked one at a time (`docs/usage/ways-in.md`) --
// and the manual says so where a restore is described: it answers again with
// its person, unless an operator revoked it while they were erased, which
// `roster key list` and `roster key revoke` do for somebody erased as for
// anybody.
func TestARestoreGivesBackTheAccountAndNotTheSignIns(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify, link, redeem, listHolders)
	ctx := t.Context()
	as := bearing(ctx, b.Token)
	c := app.NewMeServiceClient(b.Conn)
	vouches := app.NewVouchServiceClient(b.Conn)
	ref := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()

	// What somebody has out: a sign-in a front door holds, a recovery link in
	// a mailbox, and two keys in two scripts.
	signedIn := signedInAt(t, ctx, b, b.Who, []string{getMe}, time.Now())
	mailed, err := vouches.Link(as, app.VouchLinkRequest_builder{Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build()}.Build())
	x.NoError(err)
	kept := addedKey(t, ctx, b, b.Who, []string{getMe})
	revoked := addedKey(t, ctx, b, b.Who, []string{getMe})

	answers := func(as context.Context) codes.Code {
		t.Helper()
		_, err := c.Get(as, app.MeGetRequest_builder{}.Build())

		return status.Code(err)
	}
	for _, v := range []context.Context{acting(ctx, b.Token, signedIn), bearing(ctx, kept), bearing(ctx, revoked)} {
		x.Equal(codes.OK, answers(v), "what they had out did not work before the erase, so this proves nothing")
	}

	// An operator's erase, which stops all of it at once.
	_, err = b.Ungated.Holder().Erase(ctx, ref)
	x.NoError(err)
	x.Equal(codes.Unauthenticated, answers(acting(ctx, b.Token, signedIn)))
	x.Equal(codes.Unauthenticated, answers(bearing(ctx, kept)))

	// And nobody signs them out everywhere while they are gone.
	_, err = b.Ungated.Holder().Invalidate(ctx, app.HolderInvalidateRequest_builder{Ref: ref}.Build())
	x.Equal(codes.NotFound, status.Code(err), "somebody erased is signed out everywhere, so a restore has less to do than this says")

	// What an operator can do about a key, they can do now: a revoke is an
	// erase of the key's own row.
	stopped, err := b.Ungated.ApiKey().Erase(ctx, app.ApiKeyRef_builder{Secret: keys.Sum(revoked)}.Build())
	x.NoError(err)
	x.True(stopped.GetErased())

	before := time.Now()
	x.NoError(forget.Restore(ctx, b.Ent, b.Who, audit.Contents{}, nil))

	row, err := b.Ent.Holder.Get(ctx, b.Who.Uuid())
	x.NoError(err)
	x.Nil(row.DateErased)
	x.Equal("someone", row.Alias, "the account did not come back")
	x.NotNil(row.DateInvalidated, "a restore says nothing of what was issued before it")
	x.False(row.DateInvalidated.Before(before.Truncate(time.Millisecond)), "the moment is one before the restore")

	t.Run("a sign-in made before the erase is not one after the restore", func(t *testing.T) {
		require.Equal(t, codes.Unauthenticated, answers(acting(ctx, b.Token, signedIn)),
			"a restore brought back the sign-in the erase stopped")
	})

	t.Run("nor is a link mailed before it", func(t *testing.T) {
		x := require.New(t)
		res, err := vouches.Redeem(as, app.VouchRedeemRequest_builder{
			Token: mailed.GetToken(), Methods: []string{listHolders},
		}.Build())
		x.NoError(err)
		x.False(res.GetVerified().GetOk(), "a recovery link mailed before the erase signed them in after the restore")
		x.Empty(res.GetToken())
	})

	t.Run("a sign-in made after it is", func(t *testing.T) {
		after := signedInAt(t, ctx, b, b.Who, []string{getMe}, time.Now())
		require.Equal(t, codes.OK, answers(acting(ctx, b.Token, after)))
	})

	t.Run("a key answers again with its person, and one revoked while they were gone does not", func(t *testing.T) {
		x := require.New(t)
		x.Equal(codes.OK, answers(bearing(ctx, kept)), "signing somebody out everywhere ended a key, which it is not to")
		x.Equal(codes.Unauthenticated, answers(bearing(ctx, revoked)))
	})
}

// TestARestoreSaysWhichNameIsTaken: an erase frees the alias at once, so
// somebody new may be called what somebody erased was. A restore of the first
// then met the index that keeps an alias one person's, and `roster restore`
// printed the store's own words: `ent: constraint failed: ... UNIQUE
// constraint failed: holder.alias, holder.tenant_id`. Right is a sentence:
// that it is their alias, who has it now, and what an operator does -- and the
// command it gives is one the binary takes, which this runs as it is told.
//
// Somebody erased is renamed by nothing, since no server reads them, so it is
// whoever has the name now that makes way.
//
// And the sentence names nobody. It said *... was @newco/alice* to whoever
// had asked by identifier, and `restore` is one of the commands the manual
// says print no field anybody's row declares personal, which is why it takes
// no `--as` and leaves no row of the access record. So it says who has the
// name, by identifier, and the first command it gives is a read with `--as`:
// the name is read there, by somebody the record names.
func TestARestoreSaysWhichNameIsTaken(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, getMe)
	ctx := t.Context()
	was := mustId(t, b.Alice.GetId())

	_, err := b.Server.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: was.Bytes()}.Build())
	x.NoError(err)
	v, err := b.Server.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.GetId()}.Build(), Alias: "alice",
	}.Build())
	x.NoError(err)
	now := mustId(t, v.GetId())

	t.Run("named by an alias two people have had, it is told who they are", func(t *testing.T) {
		x := require.New(t)

		_, err := term(t, cli.NewCmdRestore(&b.Local), "@newco/alice")
		x.Error(err)
		x.ErrorContains(err, was.String(), "the one who was erased is not named, so there is no identifier to restore by")
		x.ErrorContains(err, now.String())
		x.ErrorContains(err, "erased", "which of the two was erased is not said")
	})

	_, err = term(t, cli.NewCmdRestore(&b.Local), was.String())
	x.Error(err)
	x.NotContains(err.Error(), "constraint", "the store's own words: %v", err)
	x.NotContains(err.Error(), "alice", "a restore asked by identifier printed the alias, which is declared personal: %v", err)
	x.ErrorContains(err, "alias", "which of their names is taken is not said")
	x.ErrorContains(err, now.String(), "who has it now is not said")
	x.ErrorContains(err, "holder get -o json "+now.String(), "where the version a rename takes is read is not said")
	x.ErrorContains(err, "holder realias "+now.String(), "what to do is not said")

	row, err := b.Server.Ent.Holder.Get(ctx, was.Uuid())
	x.NoError(err)
	x.NotNil(row.DateErased, "a restore that was refused restored")
	x.Nil(row.DateInvalidated, "a restore that was refused wrote")

	// As it says: the version, read whole, and the rename.
	out, err := cliRun(t, &b.Local, "holder", "get", "-o", "json", now.String())
	x.NoError(err)
	var got struct {
		DateUpdated string `json:"dateUpdated"`
	}
	x.NoError(json.Unmarshal([]byte(out), &got))
	_, err = cliRun(t, &b.Local, "holder", "realias", now.String(),
		fmt.Sprintf(`{"alias":"alice-2","date_updated":%q}`, got.DateUpdated))
	x.NoError(err, "the command the refusal gives is not one the binary takes")

	_, err = term(t, cli.NewCmdRestore(&b.Local), was.String())
	x.NoError(err)
	row, err = b.Server.Ent.Holder.Get(ctx, was.Uuid())
	x.NoError(err)
	x.Nil(row.DateErased)
	x.Equal("alice", row.Alias)
}

// TestARestoreSaysWhenTheirDirectoryCallsSomebodyElseThatNow: what a directory
// calls somebody is one person's among the living of a tenant, as an alias is,
// and is free from the erase in the same way. So a restore of somebody whose
// directory has called somebody else by their name since met the same kind of
// index, and would have answered in the store's words too. It says which
// column, who is called that now, and that which of the two the directory
// means is an operator's to say.
func TestARestoreSaysWhenTheirDirectoryCallsSomebodyElseThatNow(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, getMe)
	ctx := t.Context()
	was, now := mustId(t, b.Alice.GetId()), mustId(t, b.Bob.GetId())

	_, err := b.Server.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.GetId()}.Build(), Name: "entra",
		Issuer: "https://login.microsoftonline.com/newco/v2.0", ClientId: "the-app", Provisions: true,
	}.Build())
	x.NoError(err)
	told := func(who []byte) {
		t.Helper()
		_, err := b.Server.Ungated.Holder().Tell(ctx, app.HolderTellRequest_builder{
			Ref: app.HolderRef_builder{Id: who}.Build(), Name: proto.String("a.liddell@newco.example"),
		}.Build())
		x.NoError(err)
	}

	told(was.Bytes())
	_, err = b.Server.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: was.Bytes()}.Build())
	x.NoError(err)
	told(now.Bytes())

	_, err = term(t, cli.NewCmdRestore(&b.Local), was.String())
	x.Error(err)
	x.NotContains(err.Error(), "constraint", "the store's own words: %v", err)
	x.ErrorContains(err, "directory_name", "which name is taken is not said")
	x.ErrorContains(err, now.String(), "who is called it now is not said")
	x.ErrorContains(err, "holder erase "+now.String(), "what to do is not said")

	// As it says, for an operator who decides it is the first the directory
	// means.
	_, err = cliRun(t, &b.Local, "holder", "erase", now.String())
	x.NoError(err, "the command the refusal gives is not one the binary takes")
	_, err = term(t, cli.NewCmdRestore(&b.Local), was.String())
	x.NoError(err)
}

// TestATenantRestoredAtTheBoxIsOnTheTrailAsARestore: `roster restore @tenant`
// calls the verb an operator over the wire reaches, in process, and a write
// made in process is filed under the generated method that made it. So the
// trail said `/roster.TenantService/Apply` of a restore, where the close
// before it, made by `roster tenant close`, says `Close`: somebody reading a
// tenant's trail for when it came back found no restore in it.
func TestATenantRestoredAtTheBoxIsOnTheTrailAsARestore(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, getMe)
	ctx := t.Context()
	newco := mustId(t, b.Tenant.GetId())

	said := func() []string {
		t.Helper()
		vs, err := b.Server.Ent.Audit.Query().
			Where(entaudit.ObjectId(newco.Uuid())).
			Order(entaudit.ByDateCreated(), entaudit.ById()).
			All(ctx)
		x.NoError(err)

		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = v.Action
		}

		return out
	}
	was := len(said())

	_, err := cliRun(t, &b.Local, "tenant", "close", "@newco")
	x.NoError(err)
	_, err = term(t, cli.NewCmdRestore(&b.Local), "@newco")
	x.NoError(err)

	x.Equal([]string{app.TenantService_Close_FullMethodName, app.TenantService_Restore_FullMethodName}, said()[was:])
}
