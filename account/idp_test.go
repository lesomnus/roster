package account_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/internal/idptest"
)

// clientId is what the fake provider knows this app as.
const clientId = "account-app"

// The fake directory is `internal/idptest`, because the Login App is a relying
// party against the same `Connection` rows and needs the same one.
type idp = idptest.Idp

func newIdp(t *testing.T) *idp { return idptest.New(t, clientId) }

// onThisMachine is what the deployment under test says of its providers: they
// may be on this machine, which is where the fake is and where a front door
// asks no provider unless it is told it may (`account.private_hosts`).
func onThisMachine(t *testing.T) arrives.PrivateHosts {
	t.Helper()
	hosts, err := arrives.ParsePrivateHosts(idptest.Loopback)
	require.NoError(t, err)

	return hosts
}
