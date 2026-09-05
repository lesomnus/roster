package cmd_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// directoryReads is what a directory's key holds (`docs/ldap.md` § The key
// this process holds), without `Verify`: this deployment binds with app
// passwords.
var directoryReads = []string{
	"/roster.TenantService/Get",
	"/roster.HolderService/Get", "/roster.HolderService/List", "/roster.HolderService/Search",
	"/roster.EmailService/Get", "/roster.EmailService/List",
	"/roster.GroupService/Get", "/roster.GroupService/List", "/roster.GroupMembershipService/List",
	"/roster.SiteService/Get", "/roster.SiteService/List",
	"/roster.TeamService/Get", "/roster.TeamService/List", "/roster.TeamMembershipService/List",
}

// TestLdapServeIsToldEverything is `roster ldap serve` from a shell: refused
// with a sentence for each thing it was not told, and, told everything, a
// directory a client binds to with an app password and searches.
func TestLdapServeIsToldEverything(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, directoryReads...)
	ctx := t.Context()

	serve := func(args ...string) error {
		return cmd.NewCmdLdap(&cmd.Config{}).Run(ctx, append([]string{"serve"}, args...))
	}
	roster := b.Hers.Client.Addr
	key := "newco=" + b.Hers.Client.Auth.Credential

	x.ErrorContains(serve("--insecure", "--key", key), "--roster")
	x.ErrorContains(serve("--roster", roster, "--insecure"), "--key")
	x.ErrorContains(serve("--roster", roster, "--insecure", "--key", "newco"), "alias=token")
	x.ErrorContains(serve("--roster", roster, "--insecure", "--key", key, "--bind", "maybe"), "--bind")
	x.ErrorContains(serve("--roster", roster, "--insecure", "--key", key, "--base", "newco"), "--base")
	x.ErrorContains(serve("--roster", roster, "--insecure", "--key", key, "--listen-tls", ":0"), "--tls")
	x.ErrorContains(serve("--roster", roster, "--insecure", "--key", key, "--require-tls"), "--tls")
	x.ErrorContains(serve("--roster", roster, "--insecure", "--key", key, "--base", "other=o=other"), "no key for it")
	x.ErrorContains(serve("--roster", roster, "--insecure", "--key", "other="+b.Hers.Client.Auth.Credential), "cannot see",
		"a key for one tenant was taken as another's")

	// bob's app password: a key on his own row, `Me.Get` and nothing else.
	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	_, err = b.Server.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.Bob.GetId()}.Build(), Alias: "nas", Secret: sum,
		Methods: []string{"/roster.MeService/Get"},
	}.Build())
	x.NoError(err)

	// Told everything, from the environment for the key the way a service
	// file would, on a port this test picked.
	t.Setenv(cmd.LdapKeyPrefix+"NEWCO", b.Hers.Client.Auth.Credential)
	addr := freePort(t)
	run, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- cmd.NewCmdLdap(&cmd.Config{}).Run(run, []string{"serve", "--roster", roster, "--insecure", "--listen", addr, "--base", "newco=dc=newco,dc=example"})
	}()
	t.Cleanup(cancel)

	var c *goldap.Conn
	x.Eventually(func() bool {
		var err error
		c, err = goldap.DialURL("ldap://" + addr)

		return err == nil
	}, 10*time.Second, 50*time.Millisecond, "the directory did not come up on %s", addr)
	defer c.Close()

	x.NoError(c.Bind("uid=bob,ou=people,dc=newco,dc=example", token))
	res, err := c.Search(goldap.NewSearchRequest("dc=newco,dc=example", goldap.ScopeWholeSubtree, goldap.NeverDerefAliases, 0, 0, false,
		"(objectClass=inetOrgPerson)", []string{"uid"}, nil))
	x.NoError(err)
	var uids []string
	for _, e := range res.Entries {
		uids = append(uids, e.GetAttributeValue("uid"))
		x.True(strings.HasSuffix(e.DN, ",ou=people,dc=newco,dc=example"), e.DN)
	}
	x.ElementsMatch([]string{"alice", "bob"}, uids)

	cancel()
	x.NoError(<-done, "serve did not stop cleanly when told to")
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := fmt.Sprintf("127.0.0.1:%d", l.Addr().(*net.TCPAddr).Port)
	require.NoError(t, l.Close())

	return addr
}
