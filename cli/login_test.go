package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
)

// One operator's clients are one comma list, and naming the operator twice is
// the way of writing it that reads right and silently is not.
//
// It cost a walk: `docker/login.sh` gave the tenant its second client with a
// second `--client`, the app started with `operators=1` and answered *the login
// is not working* for the first one, and the message it logged was about a
// client nobody could see was missing.
func TestAnOperatorsClientsAreOneList(t *testing.T) {
	x := require.New(t)

	got, err := clientsOf(nil, "NOTHING_", []string{"contoso=demo,behind"})
	x.NoError(err)
	x.Equal(map[string][]string{"contoso": {"demo", "behind"}}, got)

	_, err = clientsOf(nil, "NOTHING_", []string{"contoso=demo", "contoso=behind"})
	x.ErrorContains(err, "named twice")

	// Two operators is what a repeat is for, and still is.
	got, err = clientsOf(nil, "NOTHING_", []string{"contoso=demo", "fabrikam=other"})
	x.NoError(err)
	x.Equal(map[string][]string{"contoso": {"demo"}, "fabrikam": {"other"}}, got)

	// And a flag still layers over the block rather than adding to it, which is
	// a deployment narrowing what it was configured with.
	got, err = clientsOf(map[string][]string{"contoso": {"old"}}, "NOTHING_", []string{"contoso=demo"})
	x.NoError(err)
	x.Equal(map[string][]string{"contoso": {"demo"}}, got)
}

// TestAnOperatorWithNoKeyYetIsDroppedAndNotFatal: the state a first start has,
// and one that used to be a deployment that could not come up at all.
//
// `roster login provision` writes these files and skips an operator whose
// tenant does not exist -- a fresh volume has no customers. Refusing here put
// that back: the file the skipped operator would have had is missing, so the
// server would not start, so the tenant could never be made, so the file would
// never exist. `deploy/` hit it on its first run against an empty cluster.
func TestAnOperatorWithNoKeyYetIsDroppedAndNotFatal(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	there := filepath.Join(dir, "there.key")
	x.NoError(os.WriteFile(there, []byte("rt_there"), 0o600))

	keys, clients := minted(
		map[string]string{"there": "file:" + there, "notyet": "file:" + filepath.Join(dir, "notyet.key")},
		map[string][]string{"there": {"a"}, "notyet": {"b"}},
	)

	// Both halves, because `whole` refuses a client with no key -- and should,
	// since that is how a deployment finds out it wrote one and forgot the
	// other. What is dropped here is not that mistake.
	x.Equal(map[string]string{"there": "file:" + there}, keys)
	x.Equal(map[string][]string{"there": {"a"}}, clients)

	got, err := keysOf(keys, "NOTHING_", nil)
	x.NoError(err)
	x.NoError(whole(got, clients))
}

// TestOnlyAFileAndOnlyMissingIsForgiven: everything else is a deployment
// configured wrong, and those still stop it.
func TestOnlyAFileAndOnlyMissingIsForgiven(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	unreadable := filepath.Join(dir, "locked.key")
	x.NoError(os.WriteFile(unreadable, []byte("rt_x"), 0o000))

	for _, ref := range []string{"env:NOTHING_SET_HERE", "file:" + unreadable} {
		keys, clients := minted(
			map[string]string{"one": ref},
			map[string][]string{"one": {"a"}},
		)
		x.Len(keys, 1, "%s is not a key that has not been written yet", ref)

		_, err := keysOf(keys, "NOTHING_", nil)
		x.Error(err, "%s should stop the process", ref)
		_ = clients
	}
}
