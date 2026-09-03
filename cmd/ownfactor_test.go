package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// TestAPersonEnrolsTheirOwnSecondFactor is the second-factor half of the same
// self-service screen `TestAPersonChangesTheirOwnPassword` is the password half
// of, and it has the same shape: no verb of its own. A person calls
// `Credential.Enrol` with their **own** reference, the operator's verb, and the
// layer's `mayReach` lets it through because the target is the caller.
//
// A role naming `Enrol` reaches anybody no wider than the caller -- RBAC as it
// is -- and the app that serves a person is what passes only their reference.
// What is proved here is the verb: the code the enrolment answered with
// verifies for the row it was made for.
func TestAPersonEnrolsTheirOwnSecondFactor(t *testing.T) {
	const enrol = "/roster.CredentialService/Enrol"

	x := require.New(t)
	b := keyFor(t, enrol)
	ctx := t.Context()

	// A role that names Enrol, and her own key holding it -- the two gates every
	// write meets: the key's list, and the holder's role.
	permits(t, ctx, b, b.Contoso, b.Who, "self", enrol)
	hers := mintFor(t, ctx, b, b.Who, "laptop", []string{enrol}, time.Time{})
	cl := app.NewCredentialServiceClient(b.Conn)

	res, err := cl.Enrol(bearing(ctx, hers), app.CredentialEnrolRequest_builder{
		Ref:  app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Kind: vouch.KindTotp,
	}.Build())
	x.NoError(err)
	x.NotEmpty(res.GetSeed())
	x.Contains(res.GetUri(), "otpauth://totp/")

	// It landed on **her** account: the code it made verifies for the row the
	// reference named, which is hers.
	seed := seedOf(t, res.GetSeed())
	got, err := b.keyed(t).Verify(ctx, app.VouchVerifyRequest_builder{
		Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		Kind:   vouch.KindTotp,
		Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
	}.Build())
	x.NoError(err)
	x.True(got.GetOk(), "the factor a person enrolled on themselves does not verify for them")

	row, err := b.Ent.Credential.Query().Only(ctx)
	x.NoError(err)
	x.Equal(vouch.KindTotp, row.Kind)
	x.Equal(b.Who.Uuid(), row.HolderId, "the factor landed on somebody other than the caller")
}
