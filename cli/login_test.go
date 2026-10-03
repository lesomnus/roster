package cli

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
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
		got, err := loginApp(t.Context(), c, listening(t), nil)
		x.NoError(err)
		x.Empty(got.Key, "a key that is not written yet is not a deployment that fails to start")
	})

	t.Run("and the next start finds it", func(t *testing.T) {
		x := require.New(t)

		x.NoError(os.WriteFile(there, []byte("rk_written\n"), 0o600))

		c := &cmd.Config{Login: cmd.LoginConfig{Key: "file:" + there}}
		got, err := loginApp(t.Context(), c, listening(t), nil)
		x.NoError(err)
		x.Equal("rk_written", got.Key, "the token and not the reference: the app is handed the thing")
	})

	// Everything else is a deployment configured wrong, and those still stop it:
	// only *not there* is forgiven, for the reason the comment above gives.
	t.Run("an env reference naming nothing still refuses", func(t *testing.T) {
		x := require.New(t)

		c := &cmd.Config{Login: cmd.LoginConfig{Key: "env:NOTHING_SET_HERE"}}
		_, err := loginApp(t.Context(), c, listening(t), nil)
		x.Error(err)
		x.ErrorContains(err, "login.key")
	})

	// And the shape that replaces the file for a deployment that runs the app
	// in this process: nothing named, a control plane to mint on, and the key
	// is made at start with the nominations `provision` would have written.
	t.Run("and nothing named, with a control plane here, is a key made at start", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()
		s := deployment(t)
		contoso := tenantCalled(t, s, "contoso")
		answersAt(t, s, contoso, "contoso.example")

		c := &cmd.Config{Login: cmd.LoginConfig{Addr: ":0"}}
		got, err := loginApp(ctx, c, listening(t), s)
		x.NoError(err)
		x.True(strings.HasPrefix(got.Key, keys.PrefixDeployment), "not a deployment key: %q", got.Key)

		// The per-customer half too: the tenant behind the name nominated a
		// holder for this app's key.
		borrower, err := cmd.HolderNamed(ctx, s.Control, provisioned)
		x.NoError(err)
		n, err := s.Ungated.Nomination().Get(ctx, rstr.NominationGetRequest_builder{
			Ref: rstr.NominationRef_builder{Borrower: rstr.NominationRefByBorrower_builder{
				Tenant:     rstr.TenantRef_builder{Id: contoso}.Build(),
				BorrowerId: borrower.Bytes(),
			}.Build()}.Build(),
			Select: rstr.NominationSelect_builder{ActsAs: rstr.HolderSelect_builder{}.Build()}.Build(),
		}.Build())
		x.NoError(err, "the tenant was not nominated in")
		x.NotEmpty(n.GetActsAs().GetId())

		// And without the plane an `rk_` lives in, nothing is made and nothing
		// refuses: the app stays off, as a missing file leaves it.
		got, err = loginApp(ctx, c, listening(t), nil)
		x.NoError(err)
		x.Empty(got.Key)
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
