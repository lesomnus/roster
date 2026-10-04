package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/server/keys"
)

// TestAFrontDoorInThisProcessMakesItsOwnKeys is `account.key` and `account.keys` left empty
// inside `roster serve`: the rows `roster account provision` writes, and the
// key in memory rather than in a file, which is one replica.
//
// What a deployment gives up by writing nothing down is only what a rotation
// costs -- the account page asks everybody to sign in again after a restart --
// because the key is a row like any other whichever way it was made: in the
// trail, revocable, and narrowed by the wall to the tenant it is for.
func TestAFrontDoorInThisProcessMakesItsOwnKeys(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	s := deployment(t)
	contoso := tenantCalled(t, s, "contoso")
	answersAt(t, s, contoso, "contoso.example")

	// `connect` named, because the rest of the block is about this process's
	// own listeners and this test has none.
	c := &cmd.Config{Account: cmd.AccountConfig{Addr: ":0", Connect: "http://127.0.0.1:1"}}
	ac, err := frontDoor(ctx, c, listening(t), s)
	x.NoError(err)
	x.Empty(ac.Keys, "a key per tenant was made, which #76 replaced")
	x.True(strings.HasPrefix(ac.Key, keys.PrefixDeployment), "not a deployment key: %q", ac.Key)

	t.Run("and a reference that resolves to nothing is still a refusal", func(t *testing.T) {
		x := require.New(t)

		// Written down and wrong is not the same as unsaid: a fresh key made
		// quietly over a broken reference would be a deployment that works and
		// is not the one somebody configured.
		c := &cmd.Config{Account: cmd.AccountConfig{
			Addr: ":0", Connect: "http://127.0.0.1:1",
			Keys: map[string]string{"contoso": "env:NOTHING_SET_HERE"},
		}}
		_, err := frontDoor(ctx, c, listening(t), s)
		x.Error(err)
		x.ErrorContains(err, "keys.contoso")
	})

	t.Run("and a key named is a reference that has to resolve", func(t *testing.T) {
		x := require.New(t)

		c := &cmd.Config{Account: cmd.AccountConfig{
			Addr: ":0", Connect: "http://127.0.0.1:1",
			Key: "env:NOTHING_SET_HERE",
		}}
		_, err := frontDoor(ctx, c, listening(t), s)
		x.ErrorContains(err, "account.key")
	})

	t.Run("and nobody to front yet still makes the key", func(t *testing.T) {
		x := require.New(t)

		// A fresh deployment: no tenant has a name yet. A deployment key needs
		// no tenant to exist, so it is made anyway, and the app finds the
		// tenants it is later nominated in for itself.
		empty := deployment(t)
		c := &cmd.Config{Account: cmd.AccountConfig{Addr: ":0", Connect: "http://127.0.0.1:1"}}
		ac, err := frontDoor(ctx, c, listening(t), empty)
		x.NoError(err)
		x.Empty(ac.Keys)
		x.True(strings.HasPrefix(ac.Key, keys.PrefixDeployment))
	})
}
