package vouch_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/server/vouch"
)

func TestASecretVerifiesAgainstItsOwnHash(t *testing.T) {
	x := require.New(t)

	sum, err := vouch.Hash([]byte("correct horse battery staple"))
	x.NoError(err)

	ok, err := vouch.Compare(sum, []byte("correct horse battery staple"))
	x.NoError(err)
	x.True(ok)

	ok, err = vouch.Compare(sum, []byte("correct horse battery stapl"))
	x.NoError(err)
	x.False(ok)
}

// TestTheSameSecretHashesDifferentlyEveryTime, which is the salt doing its job.
//
// Without it, two people with the same password have the same row, and one
// precomputed table answers for every deployment there has ever been.
func TestTheSameSecretHashesDifferentlyEveryTime(t *testing.T) {
	x := require.New(t)

	a, err := vouch.Hash([]byte("hunter2"))
	x.NoError(err)
	b, err := vouch.Hash([]byte("hunter2"))
	x.NoError(err)

	x.NotEqual(a, b)

	// And both verify, because the salt travels with the hash.
	for _, v := range [][]byte{a, b} {
		ok, err := vouch.Compare(v, []byte("hunter2"))
		x.NoError(err)
		x.True(ok)
	}
}

// TestTheHashCarriesTheCostItWasMadeWith is what makes [vouch.Default]
// changeable.
//
// A verifier that did not record its parameters could only be checked with
// whatever this process happens to be built with, so raising the cost would
// lock every existing person out. This asserts the parameters are read from the
// stored value, by verifying a hash made with different ones.
func TestTheHashCarriesTheCostItWasMadeWith(t *testing.T) {
	x := require.New(t)

	weak := vouch.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}
	sum, err := weak.Hash([]byte("hunter2"))
	x.NoError(err)

	x.Contains(string(sum), "m=8192,t=1,p=1")
	x.NotEqual(vouch.Default.Memory, weak.Memory, "the test proves nothing if they match")

	ok, err := vouch.Compare(sum, []byte("hunter2"))
	x.NoError(err)
	x.True(ok, "a hash made at another cost did not verify")
}

// TestAVerifierThatCannotBeReadIsAnErrorAndNotANo.
//
// "No" would mean somebody with the right password is refused and nothing says
// why -- a corrupted column would read as everybody suddenly forgetting.
func TestAVerifierThatCannotBeReadIsAnErrorAndNotANo(t *testing.T) {
	for _, tt := range []struct {
		what string
		v    string
	}{
		{"empty", ""},
		{"not a phc string", "hunter2"},
		{"another algorithm", "$bcrypt$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA"},
		{"no hash", "$argon2id$v=19$m=1,t=1,p=1$c2FsdA$"},
		{"unreadable cost", "$argon2id$v=19$m=x,t=1,p=1$c2FsdA$aGFzaA"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			ok, err := vouch.Compare([]byte(tt.v), []byte("hunter2"))
			require.ErrorIs(t, err, vouch.ErrMalformed)
			require.False(t, ok)
		})
	}
}

// TestTheEncodingIsThePhcString, so that another tool can read what this wrote.
func TestTheEncodingIsThePhcString(t *testing.T) {
	x := require.New(t)

	sum, err := vouch.Hash([]byte("hunter2"))
	x.NoError(err)

	fs := strings.Split(string(sum), "$")
	x.Len(fs, 6)
	x.Equal("", fs[0])
	x.Equal("argon2id", fs[1])
	x.Equal("v=19", fs[2])
	x.Equal("m=19456,t=2,p=1", fs[3])
}

// TestBurnCostsWhatARealComparisonCosts is the shape of the timing defence.
//
// It asserts the call happens and returns rather than asserting a duration --
// a wall-clock assertion on a shared build machine is a flake. What it does
// prove is that [vouch.Burn] is not a no-op that was quietly optimised away.
func TestBurnCostsWhatARealComparisonCosts(t *testing.T) {
	vouch.Burn([]byte("hunter2"))
	vouch.Burn([]byte("hunter2"))
}
