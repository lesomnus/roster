package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAKeyWrittenWithoutItsNameIsNotSaidBack, the flag's half
// of the test of that name in `cmd`: `--tenant-key 'acme:rt_…'`, a
// colon for the equals sign, answered with the whole token in the sentence,
// into whatever keeps the process's standard error. It says the form.
func TestAKeyWrittenWithoutItsNameIsNotSaidBack(t *testing.T) {
	for _, given := range []string{"acme:rt_FLAGTOKENSECRET", "rt_FLAGTOKENSECRET"} {
		_, err := keysOf(nil, AccountKeyPrefix, []string{given})
		require.Error(t, err)
		require.Contains(t, err.Error(), "alias=token")
		require.NotContains(t, err.Error(), "FLAGTOKENSECRET")
	}
}
