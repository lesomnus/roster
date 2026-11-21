package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"uuid"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/audit"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/pd"
)

// TestAKeysLastUseIsALineOfItsOwn: when a key was last used is written by the
// deployment as it reads the key, and the trail says so under that act and no
// other.
//
// # What it was
//
// `ApiKey.date_used` is written through the stack while the call the key was
// presented with is still being authenticated, and a line of the trail is
// filed under the method gRPC says is being served. So the write was a line on
// an api-key, by nobody, under the verb of whatever the key had been presented
// for: `HolderService/Add` on a key, on the control plane's trail for a
// deployment key and on the data plane's for a tenant's. Somebody reading the
// trail for who added people found lines that were not that (#280).
//
// It stays a line. A write past the recorder, as a lock's is, leaves a row a
// check of the rows finds: the lock writes nothing of its row, and this writes
// a column the digest covers. So the line there already was says what it is,
// `roster.keys.use`, at most once an hour for a key (`keys.Touched`).
func TestAKeysLastUseIsALineOfItsOwn(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	var c cmd.Config
	b := keyedFor(t, keepingDigests(&c), app.HolderService_Add_FullMethodName)

	// A tenant's key beside the deployment's: somebody's own, on the data
	// plane, where the deployment's is a row of the control plane.
	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	made, err := b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(), Alias: "laptop", Secret: sum,
		Methods: []string{app.MeService_Get_FullMethodName},
	}.Build())
	x.NoError(err)
	theirs := mustId(t, made.GetId())
	ours := mustId(t, issuerOf(t, ctx, b))

	// Each presented for a call, twice: the deployment's to add somebody, and
	// their own to read who they are.
	for _, alias := range []string{"newcomer", "another"} {
		_, err = app.NewHolderServiceClient(b.Conn).Add(bearing(ctx, b.Token), app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: alias,
		}.Build())
		x.NoError(err)
		_, err = app.NewMeServiceClient(b.Conn).Get(bearing(ctx, token), app.MeGetRequest_builder{}.Build())
		x.NoError(err)
	}

	for _, of := range []struct {
		whose string
		db    *ent.Client
		key   uuid.UUID
	}{
		{"a tenant's key, on the data plane's trail", b.Ent, theirs.Uuid()},
		{"the deployment's key, on the control plane's trail", b.Control.Ent, ours.Uuid()},
	} {
		t.Run(of.whose, func(t *testing.T) {
			x := require.New(t)

			vs, err := of.db.Audit.Query().Where(audit.DomainEQ(uint32(pd.ApiKeyDomain))).Order(audit.ByDateCreated(), audit.ById()).All(ctx)
			x.NoError(err)

			// Made, and used: the first use is written, and the one a moment
			// later is not.
			acts := []string{}
			for _, v := range vs {
				if v.ObjectId == of.key {
					acts = append(acts, v.Action)
				}
			}
			x.Equal([]string{app.ApiKeyService_Add_FullMethodName, keys.Used}, acts, "what the trail says of a key that was used")

			// And no line about a key is under a verb that is about
			// something else.
			for _, v := range vs {
				x.Contains([]string{app.ApiKeyService_Add_FullMethodName, keys.Used}, v.Action, "a line about a key")
				if v.Action == keys.Used {
					x.Equal(uuid.Nil(), v.ActorId, "nobody asked for it: the deployment wrote it of itself")
				}
			}
		})
	}

	// And a key that was used is as the trail keeps it.
	x.NoError(cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}))
}
