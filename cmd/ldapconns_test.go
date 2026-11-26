package cmd_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/ldap/wire"
)

// TestHowManyConnectionsTheDirectoryHoldsIsAKeyOfTheFile: the directory held
// at most 1024 connections, and 256 from one address, as constants of
// `ldap/wire`, and a deployment whose every host keeps a connection of its
// own open (SSSD does) had no way past them but a second `roster ldap serve`.
// They are two keys of `ldap:`, `max_conns` and `max_conns_per_addr`, read at
// start: the defaults are what the constants were, and `roster config` shows
// them. Nought is the default too, in the file as in a `Config` made in Go, and
// never *no bound*; below nought is refused, naming the key. What happens at
// the bound is as it was.
func TestHowManyConnectionsTheDirectoryHoldsIsAKeyOfTheFile(t *testing.T) {
	t.Run("the defaults are what roster config shows", func(t *testing.T) {
		x := require.New(t)

		var out strings.Builder
		k := cli.Cmd(&cmd.Config{})
		k.Writer = &out
		x.NoError(k.Run(t.Context(), []string{"--config=", "config"}))

		x.Contains(out.String(), "max_conns: "+strconv.Itoa(wire.DefaultMaxConns))
		x.Contains(out.String(), "max_conns_per_addr: "+strconv.Itoa(wire.DefaultMaxConnsPerAddr))
		x.Equal(1024, wire.DefaultMaxConns)
		x.Equal(256, wire.DefaultMaxConnsPerAddr)
	})

	t.Run("below nought is refused by its key", func(t *testing.T) {
		for _, v := range []string{"-1", "-1024"} {
			for key, unsaid := range map[string]int{"max_conns": wire.DefaultMaxConns, "max_conns_per_addr": wire.DefaultMaxConnsPerAddr} {
				_, err := whichKeyRun(t, "ldap:\n  roster: 127.0.0.1:1\n  insecure: true\n  deployment_key: rk_x\n  "+key+": "+v+"\n",
					"ldap", "serve")
				require.Error(t, err, "ldap.%s: %s was taken", key, v)
				require.True(t, strings.HasPrefix(err.Error(), "ldap."+key+": "), "ldap.%s: %s was refused as %q", key, v, err)
				// The sentence says what leaving it out is, which `cmd` writes
				// as a number because it does not link the directory.
				require.Contains(t, err.Error(), "leave it out (or 0) for "+strconv.Itoa(unsaid))
			}
		}
	})

	b := cliUp(t, directoryReads...)
	t.Setenv(cli.LdapKeyPrefix+"NEWCO", b.Token)

	// serve is `roster ldap serve` over a file that says `block`, on a port of
	// its own, until the test ends.
	serve := func(t *testing.T, block string) string {
		t.Helper()

		path := filepath.Join(t.TempDir(), "roster.yaml")
		require.NoError(t, os.WriteFile(path, []byte("ldap:\n"+block), 0o600))

		l, addr := listening(t)
		run, cancel := context.WithCancel(cmd.Held(t.Context(), l))
		done := make(chan error, 1)
		go func() {
			done <- cli.Cmd(&cmd.Config{}).Run(run, []string{"--config", path, "ldap", "serve",
				"--roster", b.Hers.Client.Addr, "--insecure", "--listen", addr, "--base", "newco=dc=newco,dc=example"})
		}()
		t.Cleanup(func() {
			cancel()
			require.NoError(t, <-done, "the directory did not start, or did not stop cleanly when told to")
		})

		// Nothing asks whether it is up: the port is bound already, so a
		// connection made now waits to be accepted, and is accepted in the
		// order it was made. A connection asked to see whether it was up
		// would be one of the two.
		return addr
	}

	t.Run("a deployment that says two holds two, and closes the third", func(t *testing.T) {
		x := require.New(t)
		addr := serve(t, "  max_conns: 2\n")

		one := ldapConnsHeld(t, addr, "127.0.0.1")
		two := ldapConnsHeld(t, addr, "127.0.0.2")
		x.NoError(ldapConnsAsk(one), "the first of two was not answered")
		x.NoError(ldapConnsAsk(two), "the second of two was not answered")
		x.Error(ldapConnsAnswered(t, addr, "127.0.0.3"), "a third was answered, where the deployment said two")
	})

	t.Run("a file that says nought holds what it holds unsaid", func(t *testing.T) {
		x := require.New(t)
		addr := serve(t, "  max_conns: 0\n  max_conns_per_addr: 0\n")

		// More than a bound of two would, from one address: nought is not
		// read as none, nor as one.
		held := []*goldap.Conn{}
		for range 3 {
			held = append(held, ldapConnsHeld(t, addr, "127.0.0.1"))
		}
		for i, c := range held {
			x.NoError(ldapConnsAsk(c), "connection %d of three was not answered", i+1)
		}
	})

	t.Run("and from one address likewise", func(t *testing.T) {
		x := require.New(t)
		addr := serve(t, "  max_conns_per_addr: 2\n")

		one := ldapConnsHeld(t, addr, "127.0.0.1")
		two := ldapConnsHeld(t, addr, "127.0.0.1")
		x.NoError(ldapConnsAsk(one), "the first of two was not answered")
		x.NoError(ldapConnsAsk(two), "the second of two was not answered")
		x.Error(ldapConnsAnswered(t, addr, "127.0.0.1"), "a third from one address was answered, where the deployment said two")
		x.NoError(ldapConnsAnswered(t, addr, "127.0.0.2"), "another address was refused for what the first holds")
	})
}

// ldapConnsHeld is a connection to the directory at `addr` from the loopback
// address `from`, kept open until the test ends. Every address of 127/8 is
// this host's, so one test is several clients.
func ldapConnsHeld(t *testing.T, addr, from string) *goldap.Conn {
	t.Helper()

	c, err := goldap.DialURL("ldap://"+addr, goldap.DialWithDialer(&net.Dialer{
		LocalAddr: &net.TCPAddr{IP: net.ParseIP(from)},
		Timeout:   5 * time.Second,
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	return c
}

// ldapConnsAnswered is whether a connection from `from`, opened now and kept,
// is answered.
func ldapConnsAnswered(t *testing.T, addr, from string) error {
	t.Helper()

	return ldapConnsAsk(ldapConnsHeld(t, addr, from))
}

// ldapConnsAsk reads the root DSE, which a connection that has not bound is
// answered. Patiently, since the first asked may wait for the directory to
// start; one that is closed is told so at once.
func ldapConnsAsk(c *goldap.Conn) error {
	c.SetTimeout(30 * time.Second)
	_, err := c.Search(goldap.NewSearchRequest("", goldap.ScopeBaseObject, goldap.NeverDerefAliases, 0, 0, false,
		"(objectClass=*)", []string{"namingContexts"}, nil))

	return err
}
