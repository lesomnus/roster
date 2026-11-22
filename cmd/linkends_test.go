package cmd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	entcontinuation "github.com/lesomnus/roster/internal/ent/continuation"
	entlink "github.com/lesomnus/roster/internal/ent/link"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// TestALinkEndsWithWhatEndsASignIn: a link minted before its holder's epoch is
// no link, as a delegation minted before it is no delegation.
//
// # What it was
//
// `Holder.date_invalidated` says everything issued before a moment is void. A
// password issued for somebody stamps it, a password they change does, and so
// does *sign out everywhere*; a delegation is read against it
// (`keys.findDelegation`), a session at roster's own consoles is, and the
// sign-in a key carries. A link was not. The read every kind passes through
// (`vouch.row`) asked whether the link had expired and whether its holder was
// erased, suspended or of a tenant that closed, and never when the link was
// minted.
//
// So of two recovery links asked for, the second followed -- a password handed
// over, everything signed out -- left the first good for the rest of its
// quarter hour, and following that one replaced the password the second had
// just handed over. Seen on a served build. A link out before *sign out
// everywhere* was a way in after it; one that confirms leaving deleted the
// account of somebody who had ended everything since; and one a front door
// minted for the user console opened a session there (#277).
//
// A link that confirms an address is not ended, and is here so that it stays
// so: it signs nobody in and hands nothing over, and somebody who changed their
// password a minute ago still reads their mailbox.
//
// What does end that one is what the other three were already refused for, and
// it was not: `Email.Confirm` reads the link for itself and never asked whether
// whoever it is for may still sign in. An address was stamped as proved for
// somebody suspended or erased since the mail went, or whose tenant had closed
// -- a name that resolves them and a mailbox a recovery link goes to, theirs
// the moment they were enabled or restored (#277).
func TestALinkEndsWithWhatEndsASignIn(t *testing.T) {
	const (
		issue   = "/roster.CredentialService/Issue"
		set     = "/roster.CredentialService/Set"
		signOut = "/roster.MeService/SignOutEverywhere"

		held = "correct horse battery staple"
	)

	// The account app's key, more or less: it mints and spends every kind.
	b := keyedFor(t, func(c *cmd.Config) { c.SignIn.Enabled = true },
		link, redeem, leaveLink, leaveByLink, accept, verifyEmail, confirmEmail, issue, set, signOut, getMe)
	ctx := t.Context()

	// contoso answers at a name, has said where its people sign in, lets them
	// leave, and some of them arrive through entra.
	answersAt(t, b.Server, b.Contoso, "contoso.example")
	require.NoError(t, decides(t, b.Server, b.Contoso, app.TenantConfig_builder{FrontDoor: "https://account.contoso.example"}.Build()))
	offersLeave(t, ctx, b.Server, b.Contoso, true)
	arrivesThrough(t, b.Server, b.Contoso, "entra")

	as := bearing(ctx, b.Token)
	vouches := app.NewVouchServiceClient(b.Conn)

	// Somebody for each case, since the epoch is one stamp on one row and each
	// case is about its own: with a password, and an address they proved.
	person := func(t *testing.T, alias string) pdid.Id {
		t.Helper()

		who := addHolder(t, ctx, b.Server, b.Contoso, alias)
		require.NoError(t, setFor(ctx, b.Server, who.Bytes(), held))
		proves(t, ctx, b.Server, who, alias+"@contoso.example")

		return who
	}

	recovery := func(t *testing.T, who pdid.Id) string {
		t.Helper()

		v, err := vouches.Link(as, app.VouchLinkRequest_builder{
			Who: app.VouchWho_builder{Id: who.Bytes()}.Build(),
		}.Build())
		require.NoError(t, err)

		return v.GetToken()
	}
	redeems := func(t *testing.T, token string) bool {
		t.Helper()

		res, err := vouches.Redeem(as, app.VouchRedeemRequest_builder{Token: token, Methods: []string{getMe}}.Build())
		require.NoError(t, err)

		return res.GetVerified().GetOk()
	}

	// The stamp is the server's clock, and a row written in the same
	// millisecond ties (`disable_test.go`). Slept either side, so that before
	// means before and after means after; the tie has a case of its own below.
	ended := func(t *testing.T, by func()) {
		t.Helper()

		time.Sleep(2 * time.Millisecond)
		by()
		time.Sleep(2 * time.Millisecond)
	}

	// Three things end every sign-in somebody has, and all three are one stamp.
	signsOut := func(t *testing.T, who pdid.Id) {
		t.Helper()

		ended(t, func() {
			// Their own act, by a sign-in of theirs.
			_, err := app.NewMeServiceClient(b.Conn).SignOutEverywhere(
				acting(ctx, b.Token, signedInAt(t, ctx, b, who, []string{signOut}, time.Now())),
				app.MeSignOutEverywhereRequest_builder{}.Build())
			require.NoError(t, err)
		})
	}
	changes := func(t *testing.T, who pdid.Id) {
		t.Helper()

		// A role naming `Set`, theirs alone: this key holds no role in
		// contoso, so it writes no way in for somebody who holds one
		// (`server/core/escalate.go`), and the other cases have it issue a
		// password and prove an address.
		permits(t, ctx, b, b.Contoso, who, "self-"+who.String(), set)
		ended(t, func() {
			_, err := app.NewCredentialServiceClient(b.Conn).Set(
				acting(ctx, b.Token, signedInAt(t, ctx, b, who, []string{set}, time.Now())),
				app.CredentialSetRequest_builder{
					Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
					Current: []byte(held),
					Secret:  []byte("a whole new set of words entirely"),
				}.Build())
			require.NoError(t, err)
		})
	}
	issues := func(t *testing.T, who pdid.Id) {
		t.Helper()

		ended(t, func() {
			// As the front door's key and by reference, which is what the
			// account app does with a recovery link somebody followed.
			_, err := app.NewCredentialServiceClient(b.Conn).Issue(as, app.CredentialIssueRequest_builder{
				Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			}.Build())
			require.NoError(t, err)
		})
	}

	t.Run("a recovery link, by a newer one followed", func(t *testing.T) {
		x := require.New(t)
		who := person(t, "ann")

		// Asked for twice: mail was slow, and somebody pressed the button again.
		first, second := recovery(t, who), recovery(t, who)

		// A recovery is the link spent and then a password issued, and it is
		// the second half that signs everything out.
		x.True(redeems(t, second), "the control: a link is a way in")
		issues(t, who)

		x.False(redeems(t, first),
			"the link asked for first is still a way in, and would replace the password the second handed over")
	})

	for _, tc := range []struct {
		alias, what string
		ends        func(*testing.T, pdid.Id)
	}{
		{"bea", "by signing out everywhere", signsOut},
		{"cho", "by their own password changed", changes},
	} {
		t.Run("a recovery link, "+tc.what, func(t *testing.T) {
			x := require.New(t)
			who := person(t, tc.alias)

			before := recovery(t, who)
			tc.ends(t, who)
			x.False(redeems(t, before), "a link out before everything was ended is a way in after it")

			// Which is about when and not about whom: one asked for since is a
			// link.
			x.True(redeems(t, recovery(t, who)), "nobody recovers an account after signing out of it")
		})
	}

	t.Run("a link that confirms leaving", func(t *testing.T) {
		x := require.New(t)
		who := person(t, "dan")

		mint := func() string {
			v, err := vouches.LeaveLink(as, app.VouchLeaveLinkRequest_builder{
				Who:     app.VouchWho_builder{Id: who.Bytes()}.Build(),
				Address: "dan@contoso.example",
			}.Build())
			x.NoError(err)

			return v.GetToken()
		}
		leaves := func(token string) error {
			_, err := vouches.Leave(as, app.VouchLeaveRequest_builder{Token: token, Confirm: "dan"}.Build())

			return err
		}

		before := mint()
		signsOut(t, who)

		// The one answer every dead link gets, which says nothing of why.
		x.Equal(codes.NotFound, status.Code(leaves(before)),
			"a link out before everything was ended deleted the account after it")
		erased, _ := erasedAt(t, ctx, b.Server, who)
		x.Nil(erased)

		x.NoError(leaves(mint()), "nobody leaves by a link after signing out")
	})

	t.Run("a link a front door hands a browser for the user console", func(t *testing.T) {
		x := require.New(t)
		who := person(t, "eve")
		mustIdentity(t, ctx, b.Server, who, "entra", "eve-at-entra")

		auth := app.NewAuthServiceClient(b.Conn)
		at := metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example"))
		mint := func() string {
			res, err := vouches.Accept(as, app.VouchAcceptRequest_builder{
				Claim:   claimBy(b.Contoso, "entra", "eve-at-entra"),
				Methods: []string{getMe},
				At:      "contoso.example",
			}.Build())
			x.NoError(err)
			x.NotEmpty(res.GetLink())

			return res.GetLink()
		}
		signsIn := func(token string) error {
			_, err := auth.SignIn(at, app.AuthSignInRequest_builder{Link: token}.Build())

			return err
		}

		before := mint()
		signsOut(t, who)

		x.Equal(codes.Unauthenticated, status.Code(signsIn(before)),
			"a link out before everything was ended opened a session after it")
		x.NoError(signsIn(mint()), "nobody is handed into the user console after signing out")
	})

	// Written rather than minted, because nothing mints at a moment it is told:
	// a link whose `date_created` is the epoch to the digit.
	t.Run("and one minted in the instant of the stamp is too old, as a delegation is", func(t *testing.T) {
		x := require.New(t)
		who := person(t, "fay")
		signsOut(t, who)

		row, err := b.Ent.Holder.Get(ctx, who.Uuid())
		x.NoError(err)
		x.NotNil(row.DateInvalidated)

		written := func(at time.Time) string {
			token, sum, err := staleLink()
			x.NoError(err)

			_, err = b.Ungated.Link().Add(ctx, app.LinkAddRequest_builder{
				Holder:      app.HolderRef_builder{Id: who.Bytes()}.Build(),
				Secret:      sum,
				Issuer:      issuerOf(t, ctx, b),
				DateExpires: timestamppb.New(time.Now().Add(time.Minute)),
				DateCreated: timestamppb.New(at),
			}.Build())
			x.NoError(err)

			return token
		}

		x.False(redeems(t, written(*row.DateInvalidated)), "a tie was read as newer")
		x.True(redeems(t, written(row.DateInvalidated.Add(time.Millisecond))), "the control: the same row, a moment later")
	})

	t.Run("and not a link that confirms an address, which signs nobody in", func(t *testing.T) {
		x := require.New(t)
		who := person(t, "gil")

		e, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Address: "gil@elsewhere.example",
		}.Build())
		x.NoError(err)

		emails := app.NewEmailServiceClient(b.Conn)
		v, err := emails.Verify(as, app.EmailVerifyRequest_builder{Ref: app.EmailRef_builder{Id: e.GetId()}.Build()}.Build())
		x.NoError(err)

		signsOut(t, who)

		res, err := emails.Confirm(as, app.EmailConfirmRequest_builder{Token: v.GetToken()}.Build())
		x.NoError(err, "a mailbox stopped being somebody's when they signed out")
		x.NotNil(res.GetEmail().GetDateVerified())
	})

	// The epoch is about when, and this is about whom: nothing is proved for
	// somebody who may no longer sign in, at this door as at the other three.
	// Each stopped between the mail going and the link coming back.
	northwind := add(t, ctx, b.Server, "northwind")
	for _, tc := range []struct {
		alias, what string
		in          pdid.Id
		stops       func(*testing.T, pdid.Id)
	}{
		{"hal", "suspended", b.Contoso, func(t *testing.T, who pdid.Id) { disables(t, ctx, b, who) }},
		{"ida", "erased", b.Contoso, func(t *testing.T, who pdid.Id) {
			_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
			require.NoError(t, err)
		}},
		{"jan", "of a tenant that closed", northwind, func(t *testing.T, _ pdid.Id) { closes(t, ctx, b.Ungated, northwind) }},
	} {
		t.Run("but it confirms nothing for somebody "+tc.what+" since", func(t *testing.T) {
			x := require.New(t)
			who := addHolder(t, ctx, b.Server, tc.in, tc.alias)

			e, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
				Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
				Address: tc.alias + "@elsewhere.example",
			}.Build())
			x.NoError(err)
			ref := app.EmailRef_builder{Id: e.GetId()}.Build()

			emails := app.NewEmailServiceClient(b.Conn)
			v, err := emails.Verify(as, app.EmailVerifyRequest_builder{Ref: ref}.Build())
			x.NoError(err)

			tc.stops(t, who)

			// The one answer every dead link gets here.
			_, err = emails.Confirm(as, app.EmailConfirmRequest_builder{Token: v.GetToken()}.Build())
			x.Equal(codes.NotFound, status.Code(err), "%v", err)

			row, err := b.Ent.Email.Get(ctx, mustId(t, e.GetId()).Uuid())
			x.NoError(err)
			x.Nil(row.DateVerified, "an address was proved for somebody who may not sign in")
		})
	}
}

// TestASignInBegunBeforeEverythingWasEndedDoesNotFinish: an attempt begun
// before its holder's epoch is no attempt, as a link minted before it is no
// link.
//
// # What it was
//
// Somebody with a second factor signs in in two calls: the first answers with
// an attempt (`Continuation`), open for five minutes, and the second finishes
// it. The read of an attempt asked whether it had expired and whether its
// holder was erased, suspended or of a tenant that closed, and never when it
// was begun.
//
// So a first factor proved before everything was ended was still proved after
// it. A sign-in begun with a password finished with the code once that
// password had been replaced, and what it finished in -- a delegation, or a
// session at roster's own door -- was minted after the stamp, so nothing read
// it as old. Whoever had the password a reset was issued against kept the five
// minutes. And a recovery link spent before the stamp, by somebody with a
// second factor, was the same sign-in begun by the link: the half of a link
// outliving a recovery that the link's own read does not see (#277).
func TestASignInBegunBeforeEverythingWasEndedDoesNotFinish(t *testing.T) {
	const (
		issue = "/roster.CredentialService/Issue"
		next  = "/roster.VouchService/Continue"

		// What [enrolled] gives everybody.
		held = "correct horse battery staple"
	)

	b := keyedFor(t, func(c *cmd.Config) { c.SignIn.Enabled = true }, delegate, next, link, redeem, issue, getMe)
	ctx := t.Context()
	answersAt(t, b.Server, b.Contoso, "contoso.example")

	as := bearing(ctx, b.Token)
	vouches := app.NewVouchServiceClient(b.Conn)
	local := b.keyed(t)

	// Somebody for each case, with a password and an authenticator app: the
	// epoch is one stamp on one row, and a code that finished one sign-in
	// finishes no other.
	person := func(t *testing.T, alias string) (pdid.Id, func() []byte) {
		t.Helper()

		who := addHolder(t, ctx, b.Server, b.Contoso, alias)
		seed := enrolled(t, ctx, b.Ungated.Credential(), local, who)

		return who, func() []byte { return []byte(vouch.CodeAt(seed, time.Now().Unix()/30)) }
	}

	// The first call at an app's door: right so far, and an attempt to show
	// for it.
	begins := func(t *testing.T, who pdid.Id, password string) string {
		t.Helper()

		res, err := vouches.Delegate(as, app.VouchDelegateRequest_builder{
			Who:     app.VouchWho_builder{Id: who.Bytes()}.Build(),
			Secret:  []byte(password),
			Methods: []string{getMe},
		}.Build())
		require.NoError(t, err)
		require.False(t, res.GetVerified().GetOk())
		require.NotEmpty(t, res.GetVerified().GetContinuation())

		return res.GetVerified().GetContinuation()
	}
	// And the second, which answers whether it finished: a delegation, or not.
	finishes := func(t *testing.T, attempt string, code []byte) bool {
		t.Helper()

		res, err := vouches.Delegate(as, app.VouchDelegateRequest_builder{
			Continuation: attempt,
			Kind:         vouch.KindTotp,
			Secret:       code,
			Methods:      []string{getMe},
		}.Build())
		require.NoError(t, err)
		require.Equal(t, res.GetVerified().GetOk(), res.GetToken() != "")

		return res.GetVerified().GetOk()
	}

	// Slept either side, as a link's test sleeps and for its reason.
	ended := func(t *testing.T, by func()) {
		t.Helper()

		time.Sleep(2 * time.Millisecond)
		by()
		time.Sleep(2 * time.Millisecond)
	}
	// A password issued for them, which is a reset: answered with the new one.
	issues := func(t *testing.T, who pdid.Id) string {
		t.Helper()

		secret := ""
		ended(t, func() {
			res, err := app.NewCredentialServiceClient(b.Conn).Issue(as, app.CredentialIssueRequest_builder{
				Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			}.Build())
			require.NoError(t, err)
			secret = res.GetSecret()
		})

		return secret
	}

	t.Run("begun with a password that has been replaced since", func(t *testing.T) {
		x := require.New(t)
		who, code := person(t, "ann")

		attempt := begins(t, who, held)
		replaced := issues(t, who)

		x.False(finishes(t, attempt, code()),
			"a sign-in begun with the password a reset replaced was finished after it")

		// Which is about when and not about whom: the same person, with the
		// password they were handed and the same code, a moment later.
		x.True(finishes(t, begins(t, who, replaced), code()), "nobody signs in after a reset")
	})

	t.Run("begun by a link, and everything signed out since", func(t *testing.T) {
		x := require.New(t)
		who, code := person(t, "bea")

		spends := func() string {
			made, err := vouches.Link(as, app.VouchLinkRequest_builder{
				Who: app.VouchWho_builder{Id: who.Bytes()}.Build(),
			}.Build())
			x.NoError(err)

			res, err := vouches.Redeem(as, app.VouchRedeemRequest_builder{Token: made.GetToken(), Methods: []string{getMe}}.Build())
			x.NoError(err)
			x.False(res.GetVerified().GetOk())
			x.NotEmpty(res.GetVerified().GetContinuation())

			return res.GetVerified().GetContinuation()
		}

		// The link was spent while it was one, so its own read has nothing to
		// refuse; what is left of it is the attempt.
		attempt := spends()
		ended(t, func() { invalidates(t, ctx, b, who) })

		x.False(finishes(t, attempt, code()), "a link spent before everything was ended signed somebody in after it")
		x.True(finishes(t, spends(), code()), "nobody finishes a link's sign-in after signing out")
	})

	t.Run("by the method that only proves", func(t *testing.T) {
		x := require.New(t)
		who, code := person(t, "cho")

		proves := func(attempt string) bool {
			res, err := vouches.Continue(as, app.VouchContinueRequest_builder{
				Continuation: attempt, Kind: vouch.KindTotp, Secret: code(),
			}.Build())
			x.NoError(err)

			return res.GetVerified().GetOk()
		}

		attempt := begins(t, who, held)
		replaced := issues(t, who)

		x.False(proves(attempt), "a first factor proved before everything was ended was still proved after it")
		x.True(proves(begins(t, who, replaced)))
	})

	t.Run("at roster's own door", func(t *testing.T) {
		x := require.New(t)
		who, code := person(t, "dan")

		auth := app.NewAuthServiceClient(b.Conn)
		at := metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example"))
		begins := func(password string) string {
			res, err := auth.SignIn(at, app.AuthSignInRequest_builder{Alias: "dan", Secret: []byte(password)}.Build())
			x.NoError(err)
			x.NotEmpty(res.GetContinuation())

			return res.GetContinuation()
		}
		finishes := func(attempt string) (string, error) {
			var h metadata.MD
			_, err := auth.SignIn(at, app.AuthSignInRequest_builder{
				Continuation: attempt, Kind: vouch.KindTotp, Secret: code(),
			}.Build(), grpc.Header(&h))

			return cookieOf(h), err
		}

		attempt := begins(held)
		replaced := issues(t, who)

		cookie, err := finishes(attempt)
		x.Equal(codes.Unauthenticated, status.Code(err), "%v", err)
		x.Empty(cookie, "a sign-in begun with the password a reset replaced ended in a session")

		cookie, err = finishes(begins(replaced))
		x.NoError(err)
		x.NotEmpty(cookie)
	})

	// Written rather than begun, because nothing begins at a moment it is told.
	t.Run("and one begun in the instant of the stamp is too old, as a link is", func(t *testing.T) {
		x := require.New(t)
		who, code := person(t, "eve")
		ended(t, func() { invalidates(t, ctx, b, who) })

		row, err := b.Ent.Holder.Get(ctx, who.Uuid())
		x.NoError(err)
		x.NotNil(row.DateInvalidated)

		written := func(at time.Time) string {
			// A handle as random as a link's token, under an attempt's prefix.
			token, _, err := staleLink()
			x.NoError(err)
			handle := vouch.PrefixContinuation + strings.TrimPrefix(token, vouch.PrefixLink)

			_, err = b.Ungated.Continuation().Add(ctx, app.ContinuationAddRequest_builder{
				Holder:      app.HolderRef_builder{Id: who.Bytes()}.Build(),
				Satisfied:   []string{vouch.KindPassword},
				Secret:      sum256(handle),
				Issuer:      issuerOf(t, ctx, b),
				DateExpires: timestamppb.New(time.Now().Add(time.Minute)),
				DateCreated: timestamppb.New(at),
			}.Build())
			x.NoError(err)

			return handle
		}

		x.False(finishes(t, written(*row.DateInvalidated), code()), "a tie was read as newer")
		x.True(finishes(t, written(row.DateInvalidated.Add(time.Millisecond)), code()), "the control: the same row, a moment later")
	})
}

// TestALinkOfSomebodyErasedIsOneThatWasNeverThere: where a link or a sign-in
// half made is read, a holder who is erased is answered as every dead one is,
// and nothing is spent to say so.
//
// # What it was
//
// The read of a link and the read of an attempt each asked whether the holder
// was erased, by a `date_erased` selected on the edge. An edge is loaded
// through the rows still there, so somebody erased comes back as a bare
// reference -- the identifier, and nothing of them -- and that column was never
// there to read. The check passed.
//
// What stopped them was what came next finding no tenant on the row.
// `Vouch.Redeem` spent the link and then failed on an identifier with no bytes,
// and `Vouch.Leave` failed the same way: `Unknown`, where every other dead
// link is one answer, so a string was told apart as having been a real link.
// An attempt was spent on the way to its `no`, and the user console refused
// only because a tenant with no alias matched no name (#277).
//
// It is `server/keys`' reading of a token (`signable`): every holder has a
// tenant, the read selects it, and one that came back with none was not found.
func TestALinkOfSomebodyErasedIsOneThatWasNeverThere(t *testing.T) {
	b := keyedFor(t, func(c *cmd.Config) { c.SignIn.Enabled = true },
		link, redeem, leaveLink, leaveByLink, accept, delegate, getMe)
	ctx := t.Context()

	answersAt(t, b.Server, b.Contoso, "contoso.example")
	require.NoError(t, decides(t, b.Server, b.Contoso, app.TenantConfig_builder{FrontDoor: "https://account.contoso.example"}.Build()))
	offersLeave(t, ctx, b.Server, b.Contoso, true)
	arrivesThrough(t, b.Server, b.Contoso, "entra")

	as := bearing(ctx, b.Token)
	vouches := app.NewVouchServiceClient(b.Conn)

	erases := func(t *testing.T, who pdid.Id) {
		t.Helper()

		v, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
		require.NoError(t, err)
		require.True(t, v.GetErased())
	}
	// unspent says a link is still a row nobody has spent.
	unspent := func(t *testing.T, token string) bool {
		t.Helper()

		n, err := b.Ent.Link.Query().Where(entlink.Secret(sum256(token)), entlink.DateErasedIsNil()).Count(ctx)
		require.NoError(t, err)

		return n == 1
	}

	t.Run("a recovery link", func(t *testing.T) {
		x := require.New(t)
		who := addHolder(t, ctx, b.Server, b.Contoso, "ann")

		made, err := vouches.Link(as, app.VouchLinkRequest_builder{
			Who: app.VouchWho_builder{Id: who.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		erases(t, who)

		res, err := vouches.Redeem(as, app.VouchRedeemRequest_builder{Token: made.GetToken(), Methods: []string{getMe}}.Build())
		x.NoError(err, "a link of somebody erased is told apart from one that was never a link")
		x.False(res.GetVerified().GetOk())
		x.Empty(res.GetToken())
		x.True(unspent(t, made.GetToken()), "a link was spent for somebody who is not there")
	})

	t.Run("a link that confirms leaving", func(t *testing.T) {
		x := require.New(t)
		who := addHolder(t, ctx, b.Server, b.Contoso, "bea")
		proves(t, ctx, b.Server, who, "bea@contoso.example")

		made, err := vouches.LeaveLink(as, app.VouchLeaveLinkRequest_builder{
			Who:     app.VouchWho_builder{Id: who.Bytes()}.Build(),
			Address: "bea@contoso.example",
		}.Build())
		x.NoError(err)
		erases(t, who)

		_, err = vouches.Leave(as, app.VouchLeaveRequest_builder{Token: made.GetToken(), Confirm: "bea"}.Build())
		x.Equal(codes.NotFound, status.Code(err), "%v", err)
		x.True(unspent(t, made.GetToken()))
	})

	t.Run("a link a front door hands a browser for the user console", func(t *testing.T) {
		x := require.New(t)
		who := addHolder(t, ctx, b.Server, b.Contoso, "cho")
		mustIdentity(t, ctx, b.Server, who, "entra", "cho-at-entra")

		res, err := vouches.Accept(as, app.VouchAcceptRequest_builder{
			Claim:   claimBy(b.Contoso, "entra", "cho-at-entra"),
			Methods: []string{getMe},
			At:      "contoso.example",
		}.Build())
		x.NoError(err)
		x.NotEmpty(res.GetLink())
		erases(t, who)

		_, err = app.NewAuthServiceClient(b.Conn).SignIn(
			metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example")),
			app.AuthSignInRequest_builder{Link: res.GetLink()}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err), "%v", err)
		x.True(unspent(t, res.GetLink()))
	})

	t.Run("a sign-in half made", func(t *testing.T) {
		x := require.New(t)
		who := addHolder(t, ctx, b.Server, b.Contoso, "dan")
		seed := enrolled(t, ctx, b.Ungated.Credential(), b.keyed(t), who)

		begun, err := vouches.Delegate(as, app.VouchDelegateRequest_builder{
			Who:     app.VouchWho_builder{Id: who.Bytes()}.Build(),
			Secret:  []byte("correct horse battery staple"),
			Methods: []string{getMe},
		}.Build())
		x.NoError(err)
		attempt := begun.GetVerified().GetContinuation()
		x.NotEmpty(attempt)
		erases(t, who)

		res, err := vouches.Delegate(as, app.VouchDelegateRequest_builder{
			Continuation: attempt,
			Kind:         vouch.KindTotp,
			Secret:       []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
			Methods:      []string{getMe},
		}.Build())
		x.NoError(err)
		x.False(res.GetVerified().GetOk())
		x.Empty(res.GetToken())

		n, err := b.Ent.Continuation.Query().
			Where(entcontinuation.Secret(sum256(attempt)), entcontinuation.DateErasedIsNil()).
			Count(ctx)
		x.NoError(err)
		x.Equal(1, n, "an attempt was spent for somebody who is not there")
	})
}
