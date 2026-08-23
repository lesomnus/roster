package vouch_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/server/vouch"
	"github.com/lesomnus/roster/server/vouch/vouchtest"
)

// TestAnAssertionIsCheckedAgainstTheKeyThatRegistered.
//
// The whole of what roster does for this kind: keep the public half, and check
// that the thing presented was signed by the private half over the challenge
// the app chose.
func TestAnAssertionIsCheckedAgainstTheKeyThatRegistered(t *testing.T) {
	x := require.New(t)

	a := vouchtest.New(t)

	v, err := vouch.Register(a.Register(t, vouchtest.Challenge(t)))
	x.NoError(err)
	x.NotEmpty(v.Stored)
	x.Zero(v.Count, "a fresh authenticator reported a counter it has not reached")

	c := vouchtest.Challenge(t)

	ok, at, err := vouch.WebAuthn{}.Compare(v.Stored, a.Assert(t, c), v.Count)
	x.NoError(err)
	x.True(ok, "the key that registered could not assert")
	x.Equal(int64(1), at)

	t.Run("and not against another key", func(t *testing.T) {
		x := require.New(t)

		// Somebody else's authenticator, answering the same challenge. The
		// signature is real and is over the right bytes; it is the wrong key.
		other := vouchtest.New(t)

		ok, _, err := vouch.WebAuthn{}.Compare(v.Stored, other.Assert(t, c), at)
		x.NoError(err)
		x.False(ok, "an assertion from a different authenticator was accepted")
	})

	t.Run("and not for a challenge this app did not send", func(t *testing.T) {
		x := require.New(t)

		// The right key and a real signature over a challenge somebody else
		// chose. This is the shape of an assertion collected at another site
		// and offered here -- the envelope says what **this** app is expecting,
		// and the browser's own record of the ceremony says otherwise.
		ok, _, err := vouch.WebAuthn{}.Compare(v.Stored, a.AssertFor(t, vouchtest.Challenge(t), vouchtest.Challenge(t)), at)
		x.NoError(err)
		x.False(ok, "an assertion made for another challenge was accepted")
	})
}

// TestTheSameAssertionDoesNotWorkTwice, which is the reason verification is
// roster's at all.
//
// D20: *it is state that has to move forward exactly once per assertion, and
// state belongs to whoever holds the row.* An app that checked assertions
// itself would have to keep this counter, and a counter kept in two places is
// two answers to whether a signature has been seen.
func TestTheSameAssertionDoesNotWorkTwice(t *testing.T) {
	x := require.New(t)

	a := vouchtest.New(t)

	v, err := vouch.Register(a.Register(t, vouchtest.Challenge(t)))
	x.NoError(err)

	c := vouchtest.Challenge(t)
	once := a.Assert(t, c)

	ok, at, err := vouch.WebAuthn{}.Compare(v.Stored, once, v.Count)
	x.NoError(err)
	x.True(ok)

	// The identical bytes, against the counter the first one wrote.
	ok, _, err = vouch.WebAuthn{}.Compare(v.Stored, once, at)
	x.NoError(err)
	x.False(ok, "a captured assertion worked a second time")
}

// TestAnAuthenticatorThatCountsNothingStillWorks.
//
// A counter is optional in the specification and plenty of keys report zero
// forever. Refusing that would refuse a large share of the hardware people own
// -- so zero on both sides is allowed, and what is refused is a counter that
// **moved backwards** on a device that reports one.
func TestAnAuthenticatorThatCountsNothingStillWorks(t *testing.T) {
	x := require.New(t)

	a := vouchtest.New(t)

	v, err := vouch.Register(a.Register(t, vouchtest.Challenge(t)))
	x.NoError(err)

	// Two assertions, both reporting zero, which is what such a key does.
	for range 2 {
		ok, at, err := vouch.WebAuthn{}.Compare(v.Stored, a.AssertAt(t, vouchtest.Challenge(t), 0), 0)
		x.NoError(err)
		x.True(ok, "a key that counts nothing was refused")
		x.Zero(at)
	}
}
