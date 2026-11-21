package cmd_test

import (
	"context"
	"github.com/lesomnus/roster/cli"
	"os"
	"regexp"
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
// passwords. `TestTheDirectorysKeyIsTheManualsList` holds it to that list.
var directoryReads = []string{
	"/roster.TenantService/Get",
	"/roster.HolderService/Get", "/roster.HolderService/List", "/roster.HolderService/Search",
	"/roster.EmailService/Get", "/roster.EmailService/List", "/roster.PhoneService/List", "/roster.RelationService/List",
	"/roster.GroupService/Get", "/roster.GroupService/List", "/roster.GroupMembershipService/List",
	"/roster.SiteService/Get", "/roster.SiteService/List",
	"/roster.TeamService/Get", "/roster.TeamService/List", "/roster.TeamMembershipService/List",
}

// directoryVerify is the one method beside them, for a deployment that binds
// with passwords.
const directoryVerify = "/roster.VouchService/Verify"

// TestTheDirectorysKeyIsTheManualsList is the list of what a directory's key
// holds, which is written wherever a role is: in `docs/ldap.md`, which an
// operator installs from; in the rig's seed (`docker/customer.sh`); and in the
// test files that stand a directory up.
//
// Nothing held them to each other, and this file's had fallen behind by the
// two newest methods -- a person's numbers and who they report to -- so the
// directory `roster ldap serve` was tested with was not the one the manual
// describes (#281). The manual's is the list, and the others are it.
func TestTheDirectorysKeyIsTheManualsList(t *testing.T) {
	x := require.New(t)
	methods := regexp.MustCompile(`/roster\.[A-Za-z]+/[A-Za-z]+`)

	page, err := os.ReadFile("../docs/ldap.md")
	x.NoError(err)
	_, after, ok := strings.Cut(string(page), "\n## The key this process holds\n")
	x.True(ok, "docs/ldap.md has no section called The key this process holds")
	_, block, ok := strings.Cut(after, "\n```\n")
	x.True(ok)
	block, _, ok = strings.Cut(block, "\n```\n")
	x.True(ok)
	manual := methods.FindAllString(block, -1)
	x.NotEmpty(manual)

	x.ElementsMatch(manual, append([]string{directoryVerify}, directoryReads...),
		"the role this file gives the directory is not the one the manual tells an operator to install")

	seed, err := os.ReadFile("../docker/customer.sh")
	x.NoError(err)
	var installed []string
	for _, line := range strings.Split(string(seed), "\n") {
		if strings.Contains(line, "--role") && strings.HasSuffix(strings.TrimSpace(line), "directory >/dev/null") {
			installed = methods.FindAllString(line, -1)
		}
	}
	x.ElementsMatch(manual, installed, "docker/customer.sh installs the directory with another role than the manual's")
}

// TestLdapServeIsToldEverything is `roster ldap serve` from a shell: refused
// with a sentence for each thing it was not told, and, told everything, a
// directory a client binds to with an app password and searches.
func TestLdapServeIsToldEverything(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, directoryReads...)
	ctx := t.Context()

	// Through the whole tree, because the flags are bound to `ldap:` and it is
	// the load on the root that applies them. `--config=` reads no file, so what
	// the directory is told is the command line and nothing a checkout left.
	serve := func(args ...string) error {
		return cli.Cmd(&cmd.Config{}).Run(ctx, append([]string{"--config=", "ldap", "serve"}, args...))
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
	x.ErrorContains(serve("--roster", roster, "--insecure", "--key", key, "--base", "other=o=other"), "no key or nomination for it")
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
	t.Setenv(cli.LdapKeyPrefix+"NEWCO", b.Hers.Client.Auth.Credential)
	l, addr := listening(t)
	run, cancel := context.WithCancel(cmd.Held(ctx, l))
	done := make(chan error, 1)
	go func() {
		done <- cli.Cmd(&cmd.Config{}).Run(run, []string{"--config=", "ldap", "serve", "--roster", roster, "--insecure", "--listen", addr, "--base", "newco=dc=newco,dc=example"})
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
	// `admin` among them: every tenant is made with the holder that
	// administers it (`server/core/tenant.go`), who is a person. And alice,
	// whom this directory's key hangs off: a tenant's own key does not say
	// whose it is, so its holder is listed like anybody (`docs/ldap.md` § A
	// person).
	x.ElementsMatch([]string{"admin", "alice", "bob"}, uids)

	cancel()
	x.NoError(<-done, "serve did not stop cleanly when told to")
}
