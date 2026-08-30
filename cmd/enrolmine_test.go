package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// TestAPersonEnrolsTheirOwnSecondFactor is `EnrolMine`: the second-factor half
// of the same self-service screen `ChangeMine` is the password half of.
//
// It is `Enrol` with no subject -- the row is the frame's actor, and there is
// no field a caller could redirect it with -- so a role naming it grants *add a
// factor to your own account* and nothing wider. What proves it is on the
// caller's own row is that the code the enrolment answered with verifies for
// them.
func TestAPersonEnrolsTheirOwnSecondFactor(t *testing.T) {
	const enrolMine = "/roster.CredentialService/EnrolMine"

	x := require.New(t)
	b := keyFor(t, enrolMine)
	ctx := t.Context()

	// A role that names EnrolMine, and her own key holding it -- the two gates
	// every self-service write meets: the key's list, and the holder's role.
	permits(t, ctx, b, b.Contoso, b.Who, "self", enrolMine)
	hers := mintFor(t, ctx, b, b.Who, "laptop", []string{enrolMine}, time.Time{})
	cl := app.NewCredentialServiceClient(b.Conn)

	res, err := cl.EnrolMine(bearing(ctx, hers), app.CredentialEnrolMineRequest_builder{
		Kind: vouch.KindTotp,
	}.Build())
	x.NoError(err)
	x.NotEmpty(res.GetSeed())
	x.Contains(res.GetUri(), "otpauth://totp/")

	// It landed on **her** account, which is the whole of what "no subject"
	// buys: the code it made verifies for the frame's actor and for nobody she
	// might have named, because there was no name to give.
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
	x.Equal(b.Who.Uuid(), row.HolderID, "the factor landed on somebody other than the caller")
}
