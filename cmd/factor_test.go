package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// TestSomebodyRemovesASecondFactorOfTheirOwn is `Credential.Erase` on the wire
// (`ts/plan.md` § B): the verb that existed, served, with the layer it owed.
//
// A person removes a factor they no longer have -- the phone that was lost --
// by calling the operator's verb about their own row, which is the line. What
// the layer adds: a password is never removed, only replaced, so nobody is
// stranded by a button and nobody is downgraded by one; and the row's holder
// is held to `mayReach`, so a plain person cannot take a factor off somebody
// wider than they are.
func TestSomebodyRemovesASecondFactorOfTheirOwn(t *testing.T) {
	const erase = "/roster.CredentialService/Erase"

	x := require.New(t)
	b := keyFor(t, erase)
	ctx := t.Context()

	own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()

	// A password and a phone, set the operator way.
	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{Ref: own, Secret: []byte("correct horse battery staple")}.Build())
	x.NoError(err)
	_, err = b.Ungated.Credential().Enrol(ctx, app.CredentialEnrolRequest_builder{Ref: own, Kind: vouch.KindTotp, Name: "phone"}.Build())
	x.NoError(err)

	permits(t, ctx, b, b.Contoso, b.Who, "self", erase)
	hers := mintFor(t, ctx, b, b.Who, "laptop", []string{erase}, time.Time{})
	cl := app.NewCredentialServiceClient(b.Conn)

	byKind := func(who *app.HolderRef, kind, name string) *app.CredentialRef {
		return app.CredentialRef_builder{
			Kind: app.CredentialRefByKind_builder{Holder: who, Kind: ptr(kind), Name: ptr(name)}.Build(),
		}.Build()
	}

	t.Run("the password is replaced, never removed", func(t *testing.T) {
		x := require.New(t)

		_, err := cl.Erase(bearing(ctx, hers), byKind(own, vouch.KindPassword, ""))
		x.Equal(codes.FailedPrecondition, status.Code(err), "a password was removed")
	})

	t.Run("the phone goes, and the password still signs her in", func(t *testing.T) {
		x := require.New(t)

		_, err := cl.Erase(bearing(ctx, hers), byKind(own, vouch.KindTotp, "phone"))
		x.NoError(err)

		ways, err := b.Ungated.Holder().SignsIn(ctx, app.HolderSignsInRequest_builder{Ref: own}.Build())
		x.NoError(err)
		x.Len(ways.GetCredentials(), 1)
		x.Equal(vouch.KindPassword, ways.GetCredentials()[0].GetKind())

		// Twice is once: erasing what is not there succeeds and removes
		// nothing, which is `Erase`'s rule.
		_, err = cl.Erase(bearing(ctx, hers), byKind(own, vouch.KindTotp, "phone"))
		x.NoError(err)
	})

	t.Run("and not somebody wider's", func(t *testing.T) {
		x := require.New(t)

		boss := addHolder(t, ctx, b.Server, b.Contoso, "boss")
		theirs := app.HolderRef_builder{Id: boss.Bytes()}.Build()
		permits(t, ctx, b, b.Contoso, boss, "admin", "/roster.HolderService/Erase")
		_, err := b.Ungated.Credential().Enrol(ctx, app.CredentialEnrolRequest_builder{Ref: theirs, Kind: vouch.KindTotp, Name: "key"}.Build())
		x.NoError(err)

		_, err = cl.Erase(bearing(ctx, hers), byKind(theirs, vouch.KindTotp, "key"))
		x.Equal(codes.PermissionDenied, status.Code(err), "a plain person took a factor off an administrator")
	})
}
