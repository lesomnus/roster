package vouch_test

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/server/vouch"
)

// keyring is one key, the way a deployment writes one.
func keyring(t *testing.T, name ...string) vouch.Keyring {
	t.Helper()

	n := "one"
	if len(name) > 0 {
		n = name[0]
	}

	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)

	k, err := vouch.NewKeyring([]string{n + ":" + base64.StdEncoding.EncodeToString(b)})
	require.NoError(t, err)

	return k
}

// TestASeedIsWrappedWithTheKeyThatMadeIt is the first secret roster holds that
// it has to be able to read back.
//
// Everything else here is a verifier: a copy of the database is a copy of
// things nobody can use. A TOTP seed is not that -- computing the code somebody
// is about to type means holding the seed -- so the row **is** the secret, and
// what the key buys is that a copy of the rows is not a copy of every second
// factor in the deployment.
func TestASeedIsWrappedWithTheKeyThatMadeIt(t *testing.T) {
	x := require.New(t)
	k := keyring(t)

	seed := []byte("12345678901234567890")

	stored, err := k.Wrap(seed)
	x.NoError(err)
	x.NotContains(string(stored), string(seed), "the seed is in the row")

	got, err := k.Unwrap(stored)
	x.NoError(err)
	x.Equal(seed, got)

	t.Run("and the same seed twice is two rows", func(t *testing.T) {
		x := require.New(t)

		other, err := k.Wrap(seed)
		x.NoError(err)
		x.NotEqual(string(stored), string(other), "the nonce is not being used")
	})

	// The name is in front and in the clear, because unwrapping has to know
	// which key before it can do anything. It is authenticated as well as
	// carried, so a ciphertext relabelled to point at a weaker key does not
	// open.
	t.Run("and it says which key made it", func(t *testing.T) {
		x := require.New(t)

		x.True(strings.HasPrefix(string(stored), "one."))
	})

	t.Run("and a key this deployment does not hold is told apart", func(t *testing.T) {
		x := require.New(t)

		_, err := keyring(t, "another").Unwrap(stored)
		x.ErrorIs(err, vouch.ErrWrongKey,
			"a rotated-away key reads as a corrupt row")
	})

	t.Run("and a relabelled ciphertext does not open", func(t *testing.T) {
		x := require.New(t)

		b := make([]byte, 32)
		_, err := rand.Read(b)
		x.NoError(err)

		two, err := vouch.NewKeyring([]string{
			"two:" + base64.StdEncoding.EncodeToString(b),
			"one:" + base64.StdEncoding.EncodeToString(k.By["one"]),
		})
		x.NoError(err)

		// The same bytes, claiming to be the other key's.
		_, rest, _ := strings.Cut(string(stored), ".")

		_, err = two.Unwrap([]byte("two." + rest))
		x.ErrorIs(err, vouch.ErrMalformed)
	})

	t.Run("and a deployment with no key refuses rather than storing one plain", func(t *testing.T) {
		x := require.New(t)

		_, err := vouch.Keyring{}.Wrap(seed)
		x.ErrorIs(err, vouch.ErrNoKey)
	})

	t.Run("and a key of the wrong length is a configuration mistake", func(t *testing.T) {
		x := require.New(t)

		_, err := vouch.NewKeyring([]string{"short:" + base64.StdEncoding.EncodeToString([]byte("too short"))})
		x.Error(err)

		_, err = vouch.NewKeyring([]string{"no-colon"})
		x.Error(err)
	})
}

// TestRotationRollsForwardAndDoesNotRewrite.
//
// New rows take the current key and old rows go on reading with whichever one
// made them. Nothing re-wraps in the background, deliberately: a sweep that
// rewrites every credential row is a sweep whose half-finished state is a
// deployment that cannot verify anybody.
func TestRotationRollsForwardAndDoesNotRewrite(t *testing.T) {
	x := require.New(t)

	old := keyring(t)
	seed := []byte("12345678901234567890")

	was, err := old.Wrap(seed)
	x.NoError(err)

	fresh := make([]byte, 32)
	_, err = rand.Read(fresh)
	x.NoError(err)

	// The new key first, the old one still there.
	both, err := vouch.NewKeyring([]string{
		"two:" + base64.StdEncoding.EncodeToString(fresh),
		"one:" + base64.StdEncoding.EncodeToString(old.By["one"]),
	})
	x.NoError(err)

	got, err := both.Unwrap(was)
	x.NoError(err, "a row written before the rotation stopped reading")
	x.Equal(seed, got)

	now, err := both.Wrap(seed)
	x.NoError(err)
	x.True(strings.HasPrefix(string(now), "two."), "a new row took the old key")
}

// TestACodeIsGoodForOneStepAndNoMore is D20's replay requirement.
//
// *A TOTP step that has been used must not work twice, and the only place that
// can be recorded is the row.* Without it a code watched over somebody's
// shoulder is good for the rest of its thirty seconds, which is most of what an
// attacker holding one needs.
func TestACodeIsGoodForOneStepAndNoMore(t *testing.T) {
	x := require.New(t)
	k := keyring(t)

	seed := []byte("12345678901234567890")
	stored, err := k.Wrap(seed)
	x.NoError(err)

	v := vouch.Totp{Keys: k}

	now := time.Now()
	code := vouch.CodeAt(seed, now.Unix()/30)

	ok, step, err := v.Compare(stored, []byte(code), 0)
	x.NoError(err)
	x.True(ok)
	x.Positive(step)

	t.Run("and the same code again is not accepted", func(t *testing.T) {
		x := require.New(t)

		ok, _, err := v.Compare(stored, []byte(code), step)
		x.NoError(err)
		x.False(ok, "a spent code worked twice")
	})

	// Somebody's phone is a few seconds off and the code they are reading is
	// the previous one. Exactly one step either way, and no knob: widening it
	// multiplies the guess space, and a deployment that widened it to fix
	// "codes keep failing" would be papering over a host whose clock is wrong.
	t.Run("and one step of drift is accepted", func(t *testing.T) {
		x := require.New(t)

		before := vouch.CodeAt(seed, now.Unix()/30-1)
		ok, _, err := v.Compare(stored, []byte(before), 0)
		x.NoError(err)
		x.True(ok)
	})

	t.Run("and two steps is not", func(t *testing.T) {
		x := require.New(t)

		far := vouch.CodeAt(seed, now.Unix()/30-2)
		ok, _, err := v.Compare(stored, []byte(far), 0)
		x.NoError(err)
		x.False(ok)
	})

	t.Run("and a code of the wrong shape is not", func(t *testing.T) {
		x := require.New(t)

		for _, c := range []string{"", "12345", "1234567", "abcdef"} {
			ok, _, err := v.Compare(stored, []byte(c), 0)
			x.NoError(err, c)
			x.False(ok, c)
		}
	})
}

// TestTheUriIsWhatAnAppCanScan pins the interoperability facts, which are the
// ones only ever discovered in front of a phone that will not scan.
func TestTheUriIsWhatAnAppCanScan(t *testing.T) {
	x := require.New(t)

	seed := []byte("12345678901234567890")
	raw := vouch.TotpUri("roster", "erin", seed)

	u, err := url.Parse(raw)
	x.NoError(err)
	x.Equal("otpauth", u.Scheme)
	x.Equal("totp", u.Host)

	q := u.Query()
	x.Equal("SHA1", q.Get("algorithm"), "an app that only speaks SHA1 is most of them")
	x.Equal("6", q.Get("digits"))
	x.Equal("30", q.Get("period"))
	x.Equal("roster", q.Get("issuer"))

	// Unpadded, uppercase base32. Padding is legal and some apps refuse it.
	secret := q.Get("secret")
	x.NotContains(secret, "=", "the seed is padded")
	x.Equal(strings.ToUpper(secret), secret)

	got, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	x.NoError(err)
	x.Equal(seed, got)
}
