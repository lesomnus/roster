package account_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/account"
)

// TestASecretWrittenWhereAReferenceBelongsIsNotRepeated: `secret_ref` takes a
// reference, and what is written there by mistake is the secret.
//
// The refusal quoted what it was given -- `secret_ref "…": only env:NAME is
// understood here` -- into the start-up log of the account app, the Login App
// and the directory alike, so the one mistake it exists to catch put a client
// secret where logs are kept (#276). A name that resolves to nothing is still
// said: a name is not a secret.
func TestASecretWrittenWhereAReferenceBelongsIsNotRepeated(t *testing.T) {
	x := require.New(t)

	for _, written := range []string{
		"8Q~the-client-secret-itself",
		"vault:kv/roster#client",
	} {
		_, err := account.EnvSecret(written)
		x.Error(err)
		x.NotContains(err.Error(), written, "what was written was repeated")
		x.Contains(err.Error(), "env:NAME")
	}

	_, err := account.EnvSecret("env:ROSTER_TEST_A_NAME_NOTHING_SETS")
	x.Error(err)
	x.Contains(err.Error(), "ROSTER_TEST_A_NAME_NOTHING_SETS")
}
