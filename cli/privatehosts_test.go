package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestAFrontDoorsPrivateHostsAreReadAtStart: what `account.private_hosts` and
// `login.private_hosts` name is read when the front door starts, and an entry
// that is not a host, an address or a network is refused there, by the name
// of the key it was written under -- rather than read as nothing, which is a
// provider on the premises refused at somebody's sign-in with the setting
// apparently in place.
func TestAFrontDoorsPrivateHostsAreReadAtStart(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	// What somebody pastes: the issuer, where its host was asked for.
	wrong := []string{"idp.corp.example", "https://idp.corp.example/realms/contoso"}

	err := serveAccount(ctx, cmd.AccountConfig{
		Roster: "127.0.0.1:1", Connect: "http://127.0.0.1:1", PrivateHosts: wrong,
	}, 0, appKeys{})
	x.ErrorContains(err, "account.private_hosts")
	x.ErrorContains(err, `"https://idp.corp.example/realms/contoso"`)

	err = serveLogin(ctx, cmd.LoginConfig{
		Hydra: cmd.HydraConfig{Admin: "http://127.0.0.1:1"}, PrivateHosts: wrong,
	}, "")
	x.ErrorContains(err, "login.private_hosts")
	x.ErrorContains(err, `"https://idp.corp.example/realms/contoso"`)
}
