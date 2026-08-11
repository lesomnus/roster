package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/vouch"
)

// vouched is the service under test, wired the way `cmd.Grpc` wires it.
func (b *built) vouched() *vouch.Server { return vouch.New(b.Ungated, b.Ungated) }

func (b *built) sets(t *testing.T, ctx context.Context, who pdid.Id, secret string) {
	t.Helper()

	_, err := b.vouched().Set(ctx, app.VouchSetRequest_builder{
		Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
		Secret: []byte(secret),
	}.Build())
	require.NoError(t, err)
}

func (b *built) verifies(t *testing.T, ctx context.Context, who pdid.Id, secret string) *app.VouchVerifyResponse {
	t.Helper()

	v, err := b.vouched().Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
		Secret: []byte(secret),
	}.Build())
	require.NoError(t, err)

	return v
}

// TestASecretSetHereVerifiesHere, and the answer carries who it was.
//
// The two identifiers are the whole point of the response: they are what the
// Login App puts in the subject of whatever it goes on to issue, and roster
// owning them is decision D1.
func TestASecretSetHereVerifiesHere(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	v := b.verifies(t, ctx, b.AcmeUser, "correct horse battery staple")
	x.True(v.GetOk())
	x.Equal(b.AcmeUser.Bytes(), v.GetHolder())
	x.Equal(b.Acme.Bytes(), v.GetTenant())
}

// TestWhatIsStoredIsNotWhatWasSent is the reason hashing is this service's.
//
// The column holds an argon2id verifier, and the plaintext is nowhere. A caller
// that hashed for itself would have chosen the parameters, and a store cannot
// tell a good choice from a bad one -- what arrives is bytes either way.
func TestWhatIsStoredIsNotWhatWasSent(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	v, err := b.Ent.Credential.Query().Only(ctx)
	x.NoError(err)
	x.NotContains(string(v.Secret), "correct horse")
	x.Contains(string(v.Secret), "$argon2id$")
}

// TestAWrongSecretIsRefusedAndSaysNothingElse.
func TestAWrongSecretIsRefusedAndSaysNothingElse(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	v := b.verifies(t, ctx, b.AcmeUser, "hunter2")
	x.False(v.GetOk())

	// Nothing about who it would have been. A refusal that carried the tenant
	// would answer "does this person exist" for anybody who asked.
	x.Empty(v.GetHolder())
	x.Empty(v.GetTenant())
}

// TestSomebodyWhoIsNotHereIsRefusedTheSameWay.
//
// Not an error, and not a different answer from a wrong password: an unknown
// person, a person with no password set and a wrong password are one response,
// because the difference between them is exactly what an attacker is asking
// for.
func TestSomebodyWhoIsNotHereIsRefusedTheSameWay(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	// Nobody at all.
	nobody := b.verifies(t, ctx, pdid.New(pd.HolderDomain), "correct horse battery staple")

	// Somebody real, who has no password.
	other := b.holder(t, ctx, b.Acme, "passwordless")
	unset := b.verifies(t, ctx, other, "correct horse battery staple")

	// And somebody real with the wrong one.
	wrong := b.verifies(t, ctx, b.AcmeUser, "hunter2")

	for _, v := range []*app.VouchVerifyResponse{nobody, unset, wrong} {
		x.False(v.GetOk())
		x.Empty(v.GetHolder())
		x.Nil(v.GetLockedUntil())
	}
}

// TestEnoughWrongAnswersCloseTheAccount, and the right password does not open
// it while it is closed.
func TestEnoughWrongAnswersCloseTheAccount(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	var last *app.VouchVerifyResponse
	for range vouch.MaxFailures {
		last = b.verifies(t, ctx, b.AcmeUser, "hunter2")
	}

	x.False(last.GetOk())
	x.NotNil(last.GetLockedUntil(), "%d wrong answers did not close it", vouch.MaxFailures)
	x.WithinDuration(time.Now().Add(vouch.LockFor), last.GetLockedUntil().AsTime(), time.Minute)

	// And the right one is refused for as long as it is closed. Otherwise the
	// lockout is only a delay for whoever guesses correctly on the next try.
	v := b.verifies(t, ctx, b.AcmeUser, "correct horse battery staple")
	x.False(v.GetOk())
	x.NotNil(v.GetLockedUntil())
}

// TestTypingAtALockedAccountDoesNotPushTheLockOut is why an attempt made during
// a lockout is not counted.
//
// Counting it would mean one continuous stream of guesses keeps moving the
// expiry, and the account is gone for as long as somebody keeps going. So the
// stored count does not move while it is closed.
//
// What this does **not** claim is that an account cannot be held locked. It
// can: ten wrong guesses every fifteen minutes will do it, and that is inherent
// to locking by name rather than something this avoids. See PLAN.md, D14.
func TestTypingAtALockedAccountDoesNotPushTheLockOut(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")
	for range vouch.MaxFailures {
		b.verifies(t, ctx, b.AcmeUser, "hunter2")
	}

	before, err := b.Ent.Credential.Query().Only(ctx)
	x.NoError(err)

	for range 5 {
		b.verifies(t, ctx, b.AcmeUser, "hunter2")
	}

	after, err := b.Ent.Credential.Query().Only(ctx)
	x.NoError(err)
	x.Equal(before.DateLocked, after.DateLocked, "typing at a locked account pushed the lock out")
}

// TestGettingItRightClearsWhatGettingItWrongLeftBehind, so that a mistake
// yesterday is not half a lockout today.
func TestGettingItRightClearsWhatGettingItWrongLeftBehind(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	for range vouch.MaxFailures - 1 {
		b.verifies(t, ctx, b.AcmeUser, "hunter2")
	}

	v := b.verifies(t, ctx, b.AcmeUser, "correct horse battery staple")
	x.True(v.GetOk())

	row, err := b.Ent.Credential.Query().Only(ctx)
	x.NoError(err)
	x.Zero(row.Failures)
}

// TestSigningInDoesNotWriteWhenNothingChanged.
//
// Every sign-in would otherwise bump a row version, write an audit entry and
// publish a watch event for a fact that did not change -- on the busiest RPC
// this app has.
func TestSigningInDoesNotWriteWhenNothingChanged(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	before, err := b.Ent.Credential.Query().Only(ctx)
	x.NoError(err)

	for range 3 {
		x.True(b.verifies(t, ctx, b.AcmeUser, "correct horse battery staple").GetOk())
	}

	after, err := b.Ent.Credential.Query().Only(ctx)
	x.NoError(err)
	x.Equal(before.DateUpdated, after.DateUpdated, "a sign-in that changed nothing wrote anyway")
}

// TestANewSecretRetiresTheOldOne.
func TestANewSecretRetiresTheOldOne(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")
	b.sets(t, ctx, b.AcmeUser, "a different one entirely")

	x.False(b.verifies(t, ctx, b.AcmeUser, "correct horse battery staple").GetOk())
	x.True(b.verifies(t, ctx, b.AcmeUser, "a different one entirely").GetOk())

	// One row, not two: the unique index is on (holder, kind).
	n, err := b.Ent.Credential.Query().Count(ctx)
	x.NoError(err)
	x.Equal(1, n)
}

// TestSettingASecretUnlocksTheAccount. Somebody who has just proved they can
// change it is not who the lockout was protecting against.
func TestSettingASecretUnlocksTheAccount(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")
	for range vouch.MaxFailures {
		b.verifies(t, ctx, b.AcmeUser, "hunter2")
	}

	b.sets(t, ctx, b.AcmeUser, "a different one entirely")

	v := b.verifies(t, ctx, b.AcmeUser, "a different one entirely")
	x.True(v.GetOk())
	x.Nil(v.GetLockedUntil())
}

// TestSomebodyNamedByTenantAndAlias, which is what a username field and a
// tenant selector make.
func TestSomebodyNamedByTenantAndAlias(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	v, err := b.vouched().Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Tenant: "acme", Alias: "someone"}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)
	x.True(v.GetOk())
	x.Equal(b.AcmeUser.Bytes(), v.GetHolder())
}

// TestAnAliasInAnotherTenantIsAnotherPerson, which is what makes an alias a
// name rather than an identifier.
func TestAnAliasInAnotherTenantIsAnotherPerson(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	theirs := b.holder(t, ctx, b.Hooli, "someone")
	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")
	b.sets(t, ctx, theirs, "a different one entirely")

	v, err := b.vouched().Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Tenant: "hooli", Alias: "someone"}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)
	x.False(v.GetOk(), "acme's password opened hooli's account")
}

// TestARequestThatNamesNobodyOrTwoBodiesIsRefused.
func TestARequestThatNamesNobodyOrTwoBodiesIsRefused(t *testing.T) {
	b, ctx := build(t)

	for _, tt := range []struct {
		what string
		who  *app.VouchWho
	}{
		{"nobody", app.VouchWho_builder{}.Build()},
		{"half a name", app.VouchWho_builder{Alias: "someone"}.Build()},
		{"both ways", app.VouchWho_builder{
			Id: b.AcmeUser.Bytes(), Tenant: "acme", Alias: "someone",
		}.Build()},
	} {
		t.Run(tt.what, func(t *testing.T) {
			_, err := b.vouched().Verify(ctx, app.VouchVerifyRequest_builder{
				Who: tt.who, Secret: []byte("hunter2"),
			}.Build())
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

// TestAnEmptySecretIsNotASecret. Otherwise `Set` with a field nobody filled in
// makes an account that opens to nothing.
func TestAnEmptySecretIsNotASecret(t *testing.T) {
	b, ctx := build(t)

	_, err := b.vouched().Set(ctx, app.VouchSetRequest_builder{
		Who: app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
	}.Build())
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestTheCredentialServiceIsNotOnTheWire is the door this all rests on.
//
// `server/vouch` exists because the generated `CredentialService.Get` returns
// whatever columns it is asked for, and one of them is the verifier. This is
// the assertion that it cannot be called -- not that it narrows, not that it
// refuses, but that there is no such method on this server.
func TestTheCredentialServiceIsNotOnTheWire(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))

	// As somebody real, and somebody whose own row this is. Anonymously the
	// call is refused before it is dispatched, which would make this test pass
	// whether the service were registered or not.
	ctx = auth.PlainProvider(b.AcmeUser.String()).Provide(ctx)

	_, err := app.NewCredentialServiceClient(conn).Get(ctx, app.CredentialGetRequest_builder{
		Ref: app.CredentialRef_builder{
			Kind: app.CredentialRefByKind_builder{
				Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
				Kind:   z.Ptr(vouch.KindPassword),
			}.Build(),
		}.Build(),
		Select: app.CredentialSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())

	x.Equal(codes.Unimplemented, status.Code(err),
		"the credential service answered; the hash is on the wire")
}

// TestNobodyVerifiesAPasswordAnonymously.
//
// `Verify` was public for an afternoon, on the argument that the person signing
// in has no credential. They do not -- and they are not who is calling. The
// caller is custody or a Login App, and roster is reached by nothing else.
//
// What public cost: anybody who could reach the port could guess passwords at
// the whole organisation, and not slowly, since `grpcx.Limit` counts per tenant
// off the frame and a public call has none.
func TestNobodyVerifiesAPasswordAnonymously(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))

	// No credential at all, which is what a stranger has.
	_, err := app.NewVouchServiceClient(conn).Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())

	x.Equal(codes.Unauthenticated, status.Code(err),
		"a stranger could guess passwords at this deployment")

	// And with one, it answers -- so this is a closed door and not a broken
	// service. The role is what `roster init` binds to a deployment's first
	// person; the policy denies by default, so without it this would refuse for
	// the wrong reason.
	b.mayAnything(b.AcmeUser, b.Acme)

	v, err := app.NewVouchServiceClient(conn).Verify(
		auth.PlainProvider(b.AcmeUser.String()).Provide(ctx),
		app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.AcmeUser.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
	x.NoError(err)
	x.True(v.GetOk())
}

// TestTheApiKeyServiceCannotBeReachedEither, for the same reason: it holds a
// verifier too.
//
// **Reached** and not "registered", because from out here the two doors are one
// answer -- `grpcx.ErrClosed` is `Unimplemented` and so is a method gRPC cannot
// dispatch. Which is right for a caller and worth saying for a reader: this
// passes with either door shut, and it was checked by opening both.
func TestTheApiKeyServiceCannotBeReachedEither(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))
	ctx = auth.PlainProvider(b.AcmeUser.String()).Provide(ctx)

	_, err := app.NewApiKeyServiceClient(conn).List(ctx, app.ApiKeyListRequest_builder{}.Build())
	x.Equal(codes.Unimplemented, status.Code(err), "the key service answered")
}

// TestABatchCannotCarryACredentialRead is the other door.
//
// A batch arrives as one method carrying many, so "not registered" does not
// reach it -- the dispatcher looks the method up in the app's own table, and
// `Credential` is in it because this app's code uses it. What stops it is
// `closed`, which is why that is one function used in two places rather than
// two lists that agree today.
func TestABatchCannotCarryACredentialRead(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	conn := pdtest.Serve(t, b.Grpc(ctx, cmd.Config{}))
	ctx = auth.PlainProvider(b.AcmeUser.String()).Provide(ctx)

	req, err := anypb.New(app.CredentialGetRequest_builder{
		Ref: app.CredentialRef_builder{
			Kind: app.CredentialRefByKind_builder{
				Holder: app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
				Kind:   z.Ptr(vouch.KindPassword),
			}.Build(),
		}.Build(),
		Select: app.CredentialSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)

	_, err = pdpb.NewBatchServiceClient(conn).Do(ctx, pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{pdpb.Op_builder{
			Method:  "/" + app.CredentialService_ServiceDesc.ServiceName + "/Get",
			Request: req,
		}.Build()},
	}.Build())

	x.Error(err, "a batch read the credential the wire will not serve")
	x.NotEqual(codes.OK, status.Code(err))
}
