package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/server/keys"
)

// TestAFrontDoorInThisProcessMakesItsOwnKeys is `account.deployment_key` and
// `account.tenant_keys` left empty inside `roster serve`: the rows `roster
// account provision` writes, and the key in memory rather than in a file,
// which is one replica.
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
	_, made, err := frontDoor(ctx, c, listening(t), s)
	x.NoError(err)
	x.Empty(made.keys, "a key per tenant was made, which #76 replaced")
	x.True(strings.HasPrefix(made.key, keys.PrefixDeployment), "not a deployment key: %q", made.key)

	t.Run("and a reference that resolves to nothing is still a refusal", func(t *testing.T) {
		x := require.New(t)

		// Written down and wrong is not the same as unsaid: a fresh key made
		// quietly over a broken reference would be a deployment that works and
		// is not the one somebody configured.
		c := &cmd.Config{Account: cmd.AccountConfig{
			Addr: ":0", Connect: "http://127.0.0.1:1",
			TenantKeys: map[string]cmd.Token{"contoso": keyAt(t, "${file:/nowhere/contoso.key}")},
		}}
		_, _, err := frontDoor(ctx, c, listening(t), s)
		x.Error(err)
		x.ErrorContains(err, "tenant_keys.contoso")
	})

	t.Run("and a key named is a reference that has to resolve", func(t *testing.T) {
		x := require.New(t)

		c := &cmd.Config{Account: cmd.AccountConfig{
			Addr: ":0", Connect: "http://127.0.0.1:1",
			DeploymentKey: keyAt(t, "${file:/nowhere/account.key}"),
		}}
		_, _, err := frontDoor(ctx, c, listening(t), s)
		x.ErrorContains(err, "account.deployment_key")
	})

	t.Run("and both kinds of key are refused in the file's words", func(t *testing.T) {
		c := &cmd.Config{
			Account: cmd.AccountConfig{
				Addr: ":0", Connect: "http://127.0.0.1:1",
				DeploymentKey: keyAt(t, "rk_x"), TenantKeys: map[string]cmd.Token{"contoso": keyAt(t, "rt_x")},
			},
			Ldap: cmd.LdapConfig{DeploymentKey: keyAt(t, "rk_x"), TenantKeys: map[string]cmd.Token{"contoso": keyAt(t, "rt_x")}},
		}
		_, _, err := frontDoor(ctx, c, listening(t), s)
		require.ErrorContains(t, err, "account.deployment_key (--deployment-key) and account.tenant_keys (--tenant-key): one way to front tenants, not two")
		_, _, err = directory(c, listening(t))
		require.ErrorContains(t, err, "ldap.deployment_key (--deployment-key) and ldap.tenant_keys (--tenant-key): one way to front tenants, not two")
	})

	t.Run("and nobody to front yet still makes the key", func(t *testing.T) {
		x := require.New(t)

		// A fresh deployment: no tenant has a name yet. A deployment key needs
		// no tenant to exist, so it is made anyway, and the app finds the
		// tenants it is later nominated in for itself.
		empty := deployment(t)
		c := &cmd.Config{Account: cmd.AccountConfig{Addr: ":0", Connect: "http://127.0.0.1:1"}}
		_, made, err := frontDoor(ctx, c, listening(t), empty)
		x.NoError(err)
		x.Empty(made.keys)
		x.True(strings.HasPrefix(made.key, keys.PrefixDeployment))
	})
}

// TestServeIsToldWhichProcessRunsTheLoops: `spin.lease` is named rather than
// defaulted, as `watch.broker` is -- the default would run every sweep in
// every replica -- and `serve` refuses before it opens anything when it is not
// (payday#54).
func TestServeIsToldWhichProcessRunsTheLoops(t *testing.T) {
	x := require.New(t)

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = config.BrokerMemory

	err := NewCmdServe(&c).Run(t.Context(), nil)
	x.ErrorContains(err, "spin.lease")
	x.ErrorContains(err, "one replica", "and why")
}
