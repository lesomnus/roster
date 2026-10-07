package vouch_test

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/server/vouch"
)

// TestAPassphraseNeverStartsWithADash, so `--password <it>` is a value.
//
// One in 64 did, and a command line built on xli read it as the next flag and
// refused the password. Five thousand draws would meet one about 78 times
// over; none may.
func TestAPassphraseNeverStartsWithADash(t *testing.T) {
	x := require.New(t)

	for range 5000 {
		s, err := vouch.Passphrase()
		x.NoError(err)
		x.NotEqual(byte('-'), s[0], s)

		b, err := base64.RawURLEncoding.DecodeString(s)
		x.NoError(err)
		x.Len(b, 32)
	}
}
