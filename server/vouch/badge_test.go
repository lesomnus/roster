package vouch_test

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/server/vouch"
)

// TestAPinIsSealedAndARotatedKeyringStillOpensIt: the row is a hash under a
// seal, so a copy of the database alone checks nothing, and a deployment that
// rolled its keyring forward goes on checking what the older key sealed.
func TestAPinIsSealedAndARotatedKeyringStillOpensIt(t *testing.T) {
	x := require.New(t)

	old := keyring(t)
	stored, err := vouch.SealPin(old, []byte("2580"))
	x.NoError(err)
	x.True(strings.HasPrefix(string(stored), "one."), "the verifier is not sealed")
	x.NotContains(string(stored), "argon2id", "the hash is readable without the key")

	ok, err := vouch.ComparePin(old, stored, []byte("2580"))
	x.NoError(err)
	x.True(ok)
	ok, err = vouch.ComparePin(old, stored, []byte("2581"))
	x.NoError(err)
	x.False(ok)

	fresh := make([]byte, 32)
	_, err = rand.Read(fresh)
	x.NoError(err)
	both, err := vouch.NewKeyring([]string{
		"two:" + base64.StdEncoding.EncodeToString(fresh),
		"one:" + base64.StdEncoding.EncodeToString(old.By["one"]),
	})
	x.NoError(err)

	ok, err = vouch.ComparePin(both, stored, []byte("2580"))
	x.NoError(err, "a PIN sealed before the rotation stopped opening")
	x.True(ok)

	now, err := vouch.SealPin(both, []byte("2580"))
	x.NoError(err)
	x.True(strings.HasPrefix(string(now), "two."), "a new PIN took the old key")

	// And a keyring without the key that sealed it is an error, not a no.
	_, err = vouch.ComparePin(keyring(t, "three"), stored, []byte("2580"))
	x.ErrorIs(err, vouch.ErrWrongKey)

	_, err = vouch.SealPin(vouch.Keyring{}, []byte("2580"))
	x.ErrorIs(err, vouch.ErrNoKey)
}
