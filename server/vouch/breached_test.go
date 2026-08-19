package vouch_test

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/server/vouch"
)

// corpus writes one in the format the well-known list is published in.
func corpus(t *testing.T, secrets ...string) string {
	t.Helper()
	x := require.New(t)

	lines := make([]string, 0, len(secrets))
	for _, v := range secrets {
		sum := sha1.Sum([]byte(v))
		// With a count after it, which the real one carries and this ignores.
		lines = append(lines, strings.ToUpper(hex.EncodeToString(sum[:]))+":12")
	}

	// Sorted, which is what the search needs and what the real one is.
	sort.Strings(lines)

	p := filepath.Join(t.TempDir(), "leaked.txt")
	x.NoError(os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600))

	return p
}

// TestASecretSomebodyHasAlreadyLostIsFound is item 5, and it is a **fact**
// rather than a policy — which is why it is roster's at all.
//
// Length, complexity and rotation are rules a deployment writes and are the
// caller's. *This one is in a corpus of leaks* can only be answered where the
// plaintext is, and roster is the only thing that sees it.
func TestASecretSomebodyHasAlreadyLostIsFound(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	// Enough that the binary search does more than one hop.
	known := []string{"password", "hunter2", "123456", "letmein", "qwerty", "dragon", "monkey"}
	at := corpus(t, known...)

	x.NoError(vouch.Sorted(at))

	in, err := vouch.BreachedIn(at)
	x.NoError(err)

	for _, v := range known {
		bad, err := in(ctx, []byte(v))
		x.NoError(err, v)
		x.True(bad, v)
	}

	for _, v := range []string{"correct horse battery staple", "", "PASSWORD", "hunter3"} {
		bad, err := in(ctx, []byte(v))
		x.NoError(err, v)
		x.False(bad, v)
	}
}

// TestACorpusThatIsNotSortedIsRefused is the failure that would otherwise be
// silent.
//
// A binary search over an unsorted file answers **no** to things that are in
// it, which is the wrong direction in the one feature whose whole job is to say
// yes — and nothing would say so until somebody looked. So the order is checked
// once at startup rather than trusted.
func TestACorpusThatIsNotSortedIsRefused(t *testing.T) {
	x := require.New(t)

	p := filepath.Join(t.TempDir(), "leaked.txt")
	x.NoError(os.WriteFile(p, []byte("FFFF\nAAAA\n"), 0o600))

	x.Error(vouch.Sorted(p))
}

// TestAnEmptyCorpusIsNotACorpus.
//
// A deployment that named a file has said the answer matters, and one that
// refuses nothing is one that would go on accepting what it said it would not.
func TestAnEmptyCorpusIsNotACorpus(t *testing.T) {
	x := require.New(t)

	p := filepath.Join(t.TempDir(), "leaked.txt")
	x.NoError(os.WriteFile(p, nil, 0o600))

	_, err := vouch.BreachedIn(p)
	x.Error(err)

	_, err = vouch.BreachedIn(filepath.Join(t.TempDir(), "nothing-here"))
	x.Error(err)
}

// TestACorpusLargerThanTheWindowIsStillFound is the half of this that a small
// file cannot exercise.
//
// The search halves until the window is small enough to read, so a corpus that
// fits in one window never halves at all — and every off-by-one in the halving
// would be invisible. This one is a few hundred kilobytes, which is a few
// dozen halvings short of the real corpus and enough for every branch.
func TestACorpusLargerThanTheWindowIsStillFound(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	secrets := make([]string, 0, 4000)
	for i := range 4000 {
		secrets = append(secrets, fmt.Sprintf("leaked-%04d", i))
	}

	at := corpus(t, secrets...)
	x.NoError(vouch.Sorted(at))

	st, err := os.Stat(at)
	x.NoError(err)
	x.Greater(st.Size(), int64(1<<14), "the file fits in one window, so nothing halved")

	in, err := vouch.BreachedIn(at)
	x.NoError(err)

	// Every one of them, so a miss anywhere in the file is a failure rather
	// than a coin toss.
	for _, v := range secrets {
		bad, err := in(ctx, []byte(v))
		x.NoError(err, v)
		x.True(bad, v)
	}

	for i := range 200 {
		v := fmt.Sprintf("not-leaked-%04d", i)
		bad, err := in(ctx, []byte(v))
		x.NoError(err, v)
		x.False(bad, v)
	}
}
