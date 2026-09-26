package cli

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// Three tests used to be here and all three were about the two maps `login`
// was configured with: that a tenant's clients are one comma list rather than
// a repeated flag, that a tenant whose key file is not written yet is dropped
// rather than fatal, and that only a missing `file:` is forgiven.
//
// All three are gone with the maps (#36). This app holds **one** key -- an
// `rk_`, which is the control plane's and so needs no customer to exist -- and
// which tenant a flow is about comes from the redirect it named rather than
// from a client map. So there is no pair of halves to keep whole, and the
// first-start cycle those tests were written around cannot happen: there is no
// per-tenant file to be absent.
//
// What is left worth pinning is the one thing that replaced them.

// TestAFirstStartWithNoKeyYetStillComesUp is the same failure those three were
// about, in the one shape it still has.
//
// `roster login provision` writes the key beside the server -- an init
// container, a line in a unit -- so the very first `roster serve` can run before
// the file exists. Refusing there would be a deployment that cannot come up
// because it has not come up, which is what `deploy/` found on an empty cluster.
//
// `roster login serve` still refuses, and that asymmetry is the point: somebody
// typed that one, and a process whose only job is the Login App has nothing to
// do without a credential.
func TestAFirstStartWithNoKeyYetStillComesUp(t *testing.T) {
	dir := t.TempDir()
	there := filepath.Join(dir, "login-app.key")

	t.Run("a file that is not there yet leaves the app off", func(t *testing.T) {
		x := require.New(t)

		c := &cmd.Config{Login: cmd.LoginConfig{Key: "file:" + there}}
		got, err := loginApp(c, listening(t))
		x.NoError(err)
		x.Empty(got.Key, "a key that is not written yet is not a deployment that fails to start")
	})

	t.Run("and the next start finds it", func(t *testing.T) {
		x := require.New(t)

		x.NoError(os.WriteFile(there, []byte("rk_written\n"), 0o600))

		c := &cmd.Config{Login: cmd.LoginConfig{Key: "file:" + there}}
		got, err := loginApp(c, listening(t))
		x.NoError(err)
		x.Equal("rk_written", got.Key, "the token and not the reference: the app is handed the thing")
	})

	// Everything else is a deployment configured wrong, and those still stop it:
	// only *not there* is forgiven, for the reason the comment above gives.
	t.Run("an env reference naming nothing still refuses", func(t *testing.T) {
		x := require.New(t)

		c := &cmd.Config{Login: cmd.LoginConfig{Key: "env:NOTHING_SET_HERE"}}
		_, err := loginApp(c, listening(t))
		x.Error(err)
		x.ErrorContains(err, "login.key")
	})
}

// listening is a listener whose address the default `login.roster` is taken
// from, and nothing else: `loginApp` fills that in when a deployment left it
// unsaid.
func listening(t *testing.T) net.Listener {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { l.Close() })

	return l
}
