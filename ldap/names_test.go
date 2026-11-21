package ldap_test

import (
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap"
)

// TestANameIsComparedAsAName is a filter on an attribute whose values are
// names -- `memberOf`, `member`, `manager` -- written as a client writes a
// name: with a space after each comma, or in capitals.
//
// Equality compared the value as text. The plan parsed the name and read the
// right members, and the filter then dropped every one of them, so
// `(memberOf=cn=payroll, ou=groups, o=contoso)` answered nobody. Worse is its
// `not`: `(!(memberOf=cn=payroll, ou=groups, o=contoso))` answered everybody,
// payroll's own members among them, so a client that keeps a group out by a
// filter let that group in. The same spelling as a search base was always read
// as a name.
//
// A name is compared as a name, parsed on both sides, RDN by RDN
// (`distinguishedNameMatch`). And a value that is not a name at all is one the
// server cannot compare, which RFC 4511 § 4.5.1.7 calls undefined: it matches
// nobody and so does its `not`, where text answered everybody for the `not`.
func TestANameIsComparedAsAName(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	const (
		payroll = "cn=payroll,ou=groups,o=contoso"
		groups  = "ou=groups,o=contoso"
	)

	t.Run("a space after each comma is the same name", func(t *testing.T) {
		x := require.New(t)
		got := search(t, c, peopleDN, goldap.ScopeSingleLevel, "(memberOf=cn=payroll, ou=groups, o=contoso)", "1.1")
		x.Equal([]string{leeDN}, dns(got), "answered nobody; right is lee, who is in payroll")

		got = search(t, c, groups, goldap.ScopeSingleLevel, "(member=uid=lee, ou=people, o=contoso)", "1.1")
		x.Equal([]string{payroll}, dns(got), "answered no group; right is payroll")

		got = search(t, c, peopleDN, goldap.ScopeSingleLevel, "(manager=UID=Lee, OU=People, O=Contoso)", "1.1")
		x.Equal([]string{kimDN}, dns(got), "answered nobody; right is kim, who reports to lee")
	})

	t.Run("and a filter that keeps a group out keeps it out however the name is spelt", func(t *testing.T) {
		x := require.New(t)
		for _, name := range []string{payroll, "cn=payroll, ou=groups, o=contoso", "CN=Payroll,OU=Groups,O=Contoso", "cn = payroll , ou = groups , o = contoso"} {
			got := dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(!(memberOf="+name+"))", "1.1"))
			x.NotContains(got, leeDN, "(!(memberOf=%s)) answered lee, who is in payroll", name)
			x.Contains(got, kimDN, "(!(memberOf=%s))", name)
		}
	})

	t.Run("a value that is not a name matches nobody, and neither does its not", func(t *testing.T) {
		x := require.New(t)
		x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(memberOf=payroll)", "1.1"))
		x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(!(memberOf=payroll))", "1.1"),
			"answered everybody; a value the server cannot compare is undefined, and so is its not")
		x.Empty(search(t, c, groups, goldap.ScopeSingleLevel, "(!(member=lee))", "1.1"))
	})

	t.Run("another name is still another name", func(t *testing.T) {
		x := require.New(t)
		x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(memberOf=cn=payroll, ou=groups, o=fabrikam)", "1.1"))
		x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(memberOf=cn=payroll, ou=teams, o=contoso)", "1.1"))
		x.Empty(search(t, c, groups, goldap.ScopeSingleLevel, "(member=uid=kim, ou=people, o=contoso)", "1.1"))
	})
}
