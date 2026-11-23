package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestAGeneralWriteDoesNotOpenACredentialsRow: `CredentialService/Patch` and
// `/Apply` are not served, whatever a deployment says of general writes.
//
// # What it was
//
// A credential's reads and its `Add` are shut by name, since the reads answer
// a verifier and `Add` takes one a caller chose. Its `Patch` and `Apply` were
// left to `grpcx.GeneralWrite`, which a deployment turns off with one line --
// `allow_general_writes` -- for a reason that is about some other entity.
// Nothing in `server/core` is over either: every rule a credential is written
// by is on a verb of its own (`Set`, `Issue`, `Enrol`, `Unlock`, `Erase`). So
// with general writes on, a role naming `CredentialService/Patch` reached the
// store, for anybody's row, past the rule about whose account a way in is
// written into, the length, the corpus and `current`: seen on a served
// deployment as the store asking for a version, and nothing refusing (#275).
//
// A key's and a delegation's general writes were already shut by name for the
// same reason, and this was the third of the three.
func TestAGeneralWriteDoesNotOpenACredentialsRow(t *testing.T) {
	b, ctx := build(t)

	// Somebody whose role names every method the service has.
	b.binds(t, b.ContosoUser, b.role(t, ctx, "credentials", "/roster.CredentialService/*"), nil)

	other := b.holder(t, ctx, b.Contoso, "somebody-else")
	b.sets(t, ctx, other, "the one they chose")
	theirs := app.CredentialRef_builder{
		Kind: app.CredentialRefByKind_builder{
			Holder: app.HolderRef_builder{Id: other.Bytes()}.Build(),
			Kind:   z.Ptr("password"),
		}.Build(),
	}.Build()

	// A deployment that turned general writes on, which is one line of
	// configuration and nothing else.
	on := config.ServerConfig{AllowGeneralWrites: true}

	refused := func(t *testing.T, c app.CredentialServiceClient) {
		t.Helper()
		x := require.New(t)
		wire := asOverTheWire(ctx, b.ContosoUser)

		_, err := c.Patch(wire, app.CredentialPatchRequest_builder{Ref: theirs}.Build())
		x.Equal(codes.Unimplemented, status.Code(err), "Patch: %v", err)
		x.ErrorContains(err, "is not served")

		_, err = c.Apply(wire, app.CredentialApplyRequest_builder{Ref: theirs}.Build())
		x.Equal(codes.Unimplemented, status.Code(err), "Apply: %v", err)
		x.ErrorContains(err, "is not served")
	}

	t.Run("on the data plane", func(t *testing.T) {
		g, err := b.Grpc(ctx, cmd.Config{Server: cmd.ServerConfig{ServerConfig: on}})
		require.NoError(t, err)

		refused(t, app.NewCredentialServiceClient(pdtest.Serve(t, g)))
	})

	t.Run("and the verbs of its own are still there", func(t *testing.T) {
		x := require.New(t)
		g, err := b.Grpc(ctx, cmd.Config{Server: cmd.ServerConfig{ServerConfig: on}})
		x.NoError(err)

		// Held to its rule, which is the point of a verb of its own: they hold
		// nothing somebody-else does not, so the reach rule passes, and what
		// refuses is that a password is replaced and not removed.
		_, err = app.NewCredentialServiceClient(pdtest.Serve(t, g)).Erase(asOverTheWire(ctx, b.ContosoUser), theirs)
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	})
}
