package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestAGeneralWriteDoesNotOpenAKeysRow: `ApiKeyService/Patch` and `/Apply` are
// not served, on any port, whatever a deployment says of general writes.
//
// # What it was
//
// They were shut by name on every port but one. On the control plane's -- the
// port that manages the deployment's own keys -- they were left to
// `grpcx.GeneralWrite`, so `control.allow_general_writes`, one line turned on
// for some other entity's sake, served both. `Patch` takes a `secret`: the
// verifier of a key, chosen by the caller, for any key row. Nothing asks whose
// key it is -- `server/core` holds `Patch` to the method list and nothing
// else, and `Apply` to nothing at all, the list included. So a role naming
// either was every key of the deployment: the owner's, with a verifier the
// caller knows (#276).
//
// Nothing of roster's calls either over the wire. A key is minted by `Issue`
// and ended by `Erase`, each held to its rules; what is written through `Patch`
// is `date_used`, by the server, in process.
//
// A credential's were shut by name in #275, on the sentence that a key's and a
// delegation's already were. A delegation's were. This makes the sentence true.
func TestAGeneralWriteDoesNotOpenAKeysRow(t *testing.T) {
	// An operator's key that names every method the service has, on a
	// deployment that turned general writes on at the keys port.
	b := keyedFor(t, func(c *cmd.Config) { c.Control.AllowGeneralWrites = true }, "/roster.ApiKeyService/*")
	ctx := t.Context()
	x := require.New(t)

	boundOnControl(t, ctx, b.Server, b.Service.Bytes(), "keys", "/roster.ApiKeyService/*")

	// Somebody else's key, which is wider: everything.
	_, wide := serviceKey(t, b.Server, "owner", "/roster.*/*")
	row, err := b.Control.Ungated.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
		Ref: app.ApiKeyRef_builder{Secret: keys.Sum(wide)}.Build(),
	}.Build())
	x.NoError(err)
	theirs := app.ApiKeyRef_builder{Id: row.GetId()}.Build()

	g, err := b.GrpcControl(ctx, b.Config)
	x.NoError(err)

	c := app.NewApiKeyServiceClient(pdtest.Serve(t, g))
	as := bearing(ctx, b.Token)

	_, chosen, err := keys.Mint(keys.PrefixDeployment)
	x.NoError(err)

	_, err = c.Patch(as, app.ApiKeyPatchRequest_builder{Ref: theirs, Secret: chosen}.Build())
	x.Equal(codes.Unimplemented, status.Code(err), "Patch: %v", err)
	x.ErrorContains(err, "is not served")

	_, err = c.Apply(as, app.ApiKeyApplyRequest_builder{Ref: theirs}.Build())
	x.Equal(codes.Unimplemented, status.Code(err), "Apply: %v", err)
	x.ErrorContains(err, "is not served")

	t.Run("and the verbs of its own are still there", func(t *testing.T) {
		x := require.New(t)

		// Read, with no verifier in the answer.
		vs, err := c.List(as, app.ApiKeyListRequest_builder{}.Build())
		x.NoError(err)
		x.NotEmpty(vs.GetItems())
		for _, v := range vs.GetItems() {
			x.Empty(v.GetSecret())
		}

		// And minted, held to its rule, which is the point of a verb of its
		// own: nobody hands out what they do not hold.
		_, err = c.Issue(as, app.ApiKeyIssueRequest_builder{
			HolderAlias: "another", Alias: "wider", Methods: []string{"/roster.*/*"},
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})
}
