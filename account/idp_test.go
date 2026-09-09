package account_test

import (
	"testing"

	"github.com/lesomnus/roster/internal/idptest"
)

// clientId is what the fake provider knows this app as.
const clientId = "account-app"

// The fake directory is `internal/idptest`, because the Login App is a relying
// party against the same `Connection` rows and needs the same one.
type idp = idptest.Idp

func newIdp(t *testing.T) *idp { return idptest.New(t, clientId) }
