package ldap_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap"
)

// parent is the name an entry is under.
func parent(name string) string {
	_, rest, _ := strings.Cut(name, ",")

	return rest
}

// TestTheTreeListsEveryNodeItAnswers is the tree as a client walks it: from a
// name to what is under it, one level at a time, or all of it at once.
//
// `ou=teams` under the suffix answered by name and was in no listing, though
// the teams under it were: a one-level search of the suffix did not hold it,
// and a subtree search held `cn=nomads,ou=teams,o=contoso` with no entry
// above it. A client that draws a tree from one-level searches never reached
// the teams with no site; one that reads a subtree was handed an entry whose
// parent it had not been told of. And a subtree search of a unit left the
// unit out, where RFC 4511 § 4.5.1.2 has the scope be "the entry named by
// baseObject and all its subordinates".
//
// Every entry a search answers has its parent answered beside it, down from
// the base; is found under that parent by a one-level search; and a subtree
// search of a name holds the name.
func TestTheTreeListsEveryNodeItAnswers(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	const (
		teams  = "ou=teams,o=contoso"
		nomads = "cn=nomads,ou=teams,o=contoso"
	)

	t.Run("the suffix lists its ou=teams", func(t *testing.T) {
		x := require.New(t)
		x.ElementsMatch([]string{peopleDN, "ou=groups,o=contoso", "ou=sites,o=contoso", teams},
			dns(search(t, c, contosoDN, goldap.ScopeSingleLevel, "(objectClass=*)", "1.1")),
			"answered three units; right is four, since ou=teams answers by name and holds the teams with no site")
		x.Contains(dns(search(t, c, contosoDN, goldap.ScopeWholeSubtree, "(objectClass=organizationalUnit)", "1.1")), teams)
		x.Contains(dns(search(t, c, "", goldap.ScopeWholeSubtree, "(ou=teams)", "1.1")), teams)
	})

	t.Run("every entry is under one that is listed, and is listed under it", func(t *testing.T) {
		x := require.New(t)
		whole := dns(search(t, c, contosoDN, goldap.ScopeWholeSubtree, "(objectClass=*)", "1.1"))
		x.Contains(whole, nomads)
		for _, name := range whole {
			if name == contosoDN {
				continue
			}
			x.Contains(whole, parent(name), "%s is in the tree and what it is under is not", name)
			x.Contains(dns(search(t, c, parent(name), goldap.ScopeSingleLevel, "(objectClass=*)", "1.1")), name,
				"%s is not among the children of %s", name, parent(name))
		}
	})

	t.Run("a subtree holds its base", func(t *testing.T) {
		x := require.New(t)
		for _, base := range []string{
			contosoDN, peopleDN, "ou=groups,o=contoso", teams, "ou=sites,o=contoso",
			"ou=seoul,ou=sites,o=contoso", "ou=teams,ou=seoul,ou=sites,o=contoso", kimDN, nomads,
		} {
			got := dns(search(t, c, base, goldap.ScopeWholeSubtree, "(objectClass=*)", "1.1"))
			x.Contains(got, base, "a subtree search of %s left it out", base)
			for _, name := range got {
				x.True(name == base || strings.HasSuffix(name, ","+base), "%s is not under %s", name, base)
			}
			// And in pages it is the same entries, each once.
			_, paged := pages(t, c, base, goldap.ScopeWholeSubtree, "(objectClass=*)", 2)
			x.ElementsMatch(got, paged, base)
		}
	})

	t.Run("a group with nobody in it has no member, and is still a group", func(t *testing.T) {
		// `groupOfNames` requires a `member`, and this tree does not invent
		// one: `docs/ldap.md` § A group, a team, a site says what a client
		// that checks the schema sees.
		x := require.New(t)
		d.group(t, d.contoso, "empty", "Nobody yet")
		e := one(t, c, "cn=empty,ou=groups,o=contoso")
		x.ElementsMatch([]string{"top", "groupOfNames"}, e.GetAttributeValues("objectClass"))
		x.Empty(e.GetAttributeValues("member"))
	})
}

// methodsOf is every method a text names, in the order it names them.
func methodsOf(text string) []string {
	return regexp.MustCompile(`/roster\.[A-Za-z]+/[A-Za-z]+`).FindAllString(text, -1)
}

// TestTheKeyOfTheseTestsIsTheManualsList holds the role this file's directory
// is given to the list `docs/ldap.md` § *The key this process holds* tells an
// operator to install. The list is written where a role is: there, in the
// rig's seed and in two test files, and one of those had fallen two methods
// behind the others (#281).
func TestTheKeyOfTheseTestsIsTheManualsList(t *testing.T) {
	x := require.New(t)
	page, err := os.ReadFile("../docs/ldap.md")
	x.NoError(err)
	_, after, ok := strings.Cut(string(page), "\n## The key this process holds\n")
	x.True(ok, "docs/ldap.md has no section called The key this process holds")
	_, block, ok := strings.Cut(after, "\n```\n")
	x.True(ok)
	block, _, ok = strings.Cut(block, "\n```\n")
	x.True(ok)

	x.ElementsMatch(methodsOf(block), reads, "the role these tests give the directory is not the one the manual tells an operator to install")
}
