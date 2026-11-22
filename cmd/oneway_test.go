package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	entcredential "github.com/lesomnus/roster/internal/ent/credential"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/vouch"
)

// TestActingForNobodyIsNotCallingAsOneself: `roster-as` that is there and says
// nothing is refused, as `roster-at` that says nothing is.
//
// # What it was
//
// `keys.Acting` read the header's last value that was not empty, so a header
// that was there and empty was a header that was not there: the handler passed
// the request on, and `Bearer` answered it as the app's key itself. An app that
// meant to act as one person and sent an empty value -- a session that had
// lost its delegation, a variable that was never set -- was answered as itself
// instead, across every tenant for a deployment key: wider than it asked, and
// silently (#276).
//
// `roster-at` had this and refuses it (`keys.ArrivedAt`): a header that is
// there and does not name one thing is not nothing said.
func TestActingForNobodyIsNotCallingAsOneself(t *testing.T) {
	b := keyFor(t, listHolders)
	ctx := t.Context()

	holders := app.NewHolderServiceClient(b.Conn)
	list := func(md ...string) error {
		_, err := holders.List(metadata.AppendToOutgoingContext(bearing(ctx, b.Token), md...),
			app.HolderListRequest_builder{}.Build())

		return err
	}
	require.NoError(t, list(), "the control: the key as itself")

	for name, md := range map[string][]string{
		"empty":            {keys.HeaderActing, ""},
		"blank":            {keys.HeaderActing, "  "},
		"two of them":      {keys.HeaderActing, "rd_one", keys.HeaderActing, "rd_two"},
		"one and an empty": {keys.HeaderActing, "", keys.HeaderActing, "rd_one"},
	} {
		require.Equal(t, codes.Unauthenticated, status.Code(list(md...)),
			"%s: a call that said it was acting for somebody was served as the app itself", name)
	}
}

// TestWhatSaysWhenSomebodySignedInIsWhatTheCallCameWith: the sign-in a call is
// held to is the one its own credential carries.
//
// # What it was
//
// Taking a second factor off one's own account takes a sign-in a few minutes
// old, and *when* is read off the credential (`keys.Authenticated`): the
// delegation in `roster-as`, or a key of the person's own. It read those
// headers itself, and they were not always what had authenticated the call. A
// session cookie was first in the chain and answered a request that carried
// both -- so beside a cookie, `roster-as` was never checked by anything: not
// that it had a key beside it, not which app it was given to, not that it was
// still alive. A session, which says nothing of when, was fresh for as long as
// any delegation string of that person's was a few minutes old (#276).
//
// A request says who is asking one way. The headers answer first and a cookie
// last, so what `Authenticated` reads is what was checked; and a request with
// two keys in it is read by nobody as either.
func TestWhatSaysWhenSomebodySignedInIsWhatTheCallCameWith(t *testing.T) {
	const (
		erase    = "/roster.CredentialService/Erase"
		password = "correct horse battery staple"
	)

	b := keyedFor(t, func(c *cmd.Config) { c.SignIn.Enabled = true }, erase)
	ctx := t.Context()
	x := require.New(t)

	answersAt(t, b.Server, b.Contoso, "contoso.example")
	permits(t, ctx, b, b.Contoso, b.Who, "self", erase)
	x.NoError(setFor(ctx, b.Server, b.Who.Bytes(), password))

	own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()
	cl := app.NewCredentialServiceClient(b.Conn)

	// A session at roster's own page, which is a cookie.
	var h metadata.MD
	_, err := app.NewAuthServiceClient(b.Conn).SignIn(
		metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example")),
		app.AuthSignInRequest_builder{Alias: "someone", Password: password}.Build(), grpc.Header(&h))
	x.NoError(err)
	cookie := cookieOf(h)
	x.NotEmpty(cookie)

	// Enrolled after, so that the sign-in above was one factor.
	_, err = b.Ungated.Credential().Enrol(ctx, app.CredentialEnrolRequest_builder{Holder: own, Kind: vouch.KindTotp, Name: "phone"}.Build())
	x.NoError(err)

	phone := app.CredentialRef_builder{
		Kind: app.CredentialRefByKind_builder{Holder: own, Kind: ptr(vouch.KindTotp), Name: ptr("phone")}.Build(),
	}.Build()
	factors := func(t *testing.T) int {
		t.Helper()

		n, err := b.Ent.Credential.Query().Where(entcredential.KindEQ(vouch.KindTotp), entcredential.DateErasedIsNil()).Count(ctx)
		require.NoError(t, err)

		return n
	}
	session := func(md ...string) context.Context {
		return metadata.AppendToOutgoingContext(ctx, append([]string{"cookie", cookie}, md...)...)
	}

	t.Run("a session says nothing of when", func(t *testing.T) {
		x := require.New(t)

		_, err := cl.Erase(session(), phone)
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
		x.Equal(1, factors(t))
	})

	t.Run("and a delegation beside it is not what the call came with", func(t *testing.T) {
		x := require.New(t)

		// Theirs, and minutes old: what some app holds for them. Nothing here
		// presents the key it was given to.
		theirs := signedInAt(t, ctx, b, b.Who, []string{erase}, time.Now())

		_, err := cl.Erase(session(keys.HeaderActing, theirs), phone)
		x.Error(err)
		x.Equal(1, factors(t), "a session was read as fresh by a delegation nothing had checked")
	})

	t.Run("nor is a second key", func(t *testing.T) {
		x := require.New(t)

		// Two keys of theirs in one request: one with no sign-in behind it,
		// and one handed to a terminal a moment ago.
		added := addedKey(t, ctx, b, b.Who, []string{erase})
		fresh := issued(t, acting(ctx, b.Token, signedInAt(t, ctx, b, b.Who, []string{"/roster.ApiKeyService/Issue"}, time.Now())),
			b, b.Who, []string{erase})

		_, err := cl.Erase(metadata.AppendToOutgoingContext(ctx,
			"authorization", "Bearer "+added, "authorization", "Bearer "+fresh), phone)
		x.Error(err)
		x.Equal(1, factors(t), "one key authenticated the call and another said when")
	})
}

// TestASignInFromBeforeEverythingWasEndedIsNotFresh: a key carries the sign-in
// it was issued with, until that sign-in is ended.
//
// A key outlives *sign out everywhere*, on purpose: it is a credential of its
// own, ended by name. What it inherits is another matter. `date_authenticated`
// on a key is the sign-in it was issued from, and `Holder.Invalidate` voids
// everything issued before a moment -- that sign-in among it. The key went on
// answering *signed in two minutes ago* for a sign-in the person had just
// ended, or that changing their password had ended for them (#276).
func TestASignInFromBeforeEverythingWasEndedIsNotFresh(t *testing.T) {
	const (
		erase = "/roster.CredentialService/Erase"
		issue = "/roster.ApiKeyService/Issue"
	)

	b := keyFor(t, erase, issue)
	ctx := t.Context()
	x := require.New(t)

	permits(t, ctx, b, b.Contoso, b.Who, "self", erase, issue)

	own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()
	cl := app.NewCredentialServiceClient(b.Conn)

	enrol := func(name string) *app.CredentialRef {
		_, err := b.Ungated.Credential().Enrol(ctx, app.CredentialEnrolRequest_builder{Holder: own, Kind: vouch.KindTotp, Name: name}.Build())
		x.NoError(err)

		return app.CredentialRef_builder{
			Kind: app.CredentialRefByKind_builder{Holder: own, Kind: ptr(vouch.KindTotp), Name: ptr(name)}.Build(),
		}.Build()
	}
	phone, tablet := enrol("phone"), enrol("tablet")

	// A terminal's key, handed over by a sign-in a moment ago.
	terminal := bearing(ctx, issued(t,
		acting(ctx, b.Token, signedInAt(t, ctx, b, b.Who, []string{issue}, time.Now())), b, b.Who, []string{erase}))

	_, err := cl.Erase(terminal, phone)
	x.NoError(err, "the control: a key carries the sign-in it was issued with")

	_, err = b.Ungated.Holder().Invalidate(ctx, app.HolderInvalidateRequest_builder{Ref: own}.Build())
	x.NoError(err)

	// Still a key, and no longer a sign-in.
	_, err = cl.Erase(terminal, tablet)
	x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
}
