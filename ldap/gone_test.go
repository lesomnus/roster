package ldap_test

import (
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap"
	rstr "github.com/lesomnus/roster/rstr"
)

// teamNamed is a team of contoso's by its alias: the deployment keeps the
// identifiers of its people and not of where they are.
func (d *deployment) teamNamed(t *testing.T, alias string) []byte {
	t.Helper()
	vs, err := d.s.Ungated.Team().List(t.Context(), rstr.TeamListRequest_builder{Size: 100}.Build())
	require.NoError(t, err)
	for _, v := range vs.GetItems() {
		if v.GetAlias() == alias {
			return v.GetId()
		}
	}
	require.FailNow(t, "no team is called "+alias)

	return nil
}

// TestSomebodyIsInTheDirectoryWhateverBecameOfAGroupTheyWereIn is an erase of
// a group and of a team with people still in them, which is what an erase is:
// it stamps the row it names and leaves the places in it hanging, on purpose
// (`docs/usage/rules.md`, *What is gone grants nothing*).
//
// `memberOf` read the group of each place and handed the `NotFound` of an
// erased one up, and a `NotFound` on its way out of a search is an empty
// answer. So lee, read by name once a group they were in was erased, was
// success and no entry; `(uid=lee)`, everybody and the whole suffix left them
// out wherever `memberOf` was read, which is wherever no attribute was named;
// and `(memberOf=<payroll>)` answered nobody while payroll's own `member`
// still named them. `GroupService/Erase`, `TeamService/Erase` and a tenant's
// directory deleting a group over SCIM all reach it, so the first group a
// tenant deleted took its members out of every client's view.
//
// A place in something erased is skipped, as a group already skips a member
// who is gone. Right is the person, with the groups they are still in.
func TestSomebodyIsInTheDirectoryWhateverBecameOfAGroupTheyWereIn(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	ctx := t.Context()
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	const (
		payroll = "cn=payroll,ou=groups,o=contoso"
		nomads  = "cn=nomads,ou=teams,o=contoso"
	)

	leavers := d.group(t, d.contoso, "leavers", "People leaving")
	d.inGroup(t, d.lee, leavers)
	_, err := d.s.Ungated.Group().Erase(ctx, rstr.GroupRef_builder{Id: leavers}.Build())
	x.NoError(err)

	t.Run("somebody whose group was erased is read by name, in the groups they are still in", func(t *testing.T) {
		x := require.New(t)
		got := search(t, c, leeDN, goldap.ScopeBaseObject, "(objectClass=*)", "memberOf")
		x.Equal([]string{leeDN}, dns(got), "answered success and no entry; right is lee")
		x.ElementsMatch([]string{payroll, nomads}, got[0].GetAttributeValues("memberOf"))

		got = search(t, c, leeDN, goldap.ScopeBaseObject, "(objectClass=*)")
		x.Equal([]string{leeDN}, dns(got), "with no attribute named, which reads memberOf")
	})

	t.Run("and is found by every search that found them before", func(t *testing.T) {
		x := require.New(t)
		x.Equal([]string{leeDN}, dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(uid=lee)")))
		x.Contains(dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(objectClass=inetOrgPerson)")), leeDN)
		x.Contains(dns(search(t, c, contosoDN, goldap.ScopeWholeSubtree, "(objectClass=*)")), leeDN)
		x.Equal([]string{leeDN}, dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(memberOf="+payroll+")", "uid")),
			"payroll's own member names lee, and memberOf is the same truth from the other end")
		x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(memberOf=cn=leavers,ou=groups,o=contoso)", "uid"),
			"an erased group has nobody in it")
	})

	t.Run("a team erased is the same", func(t *testing.T) {
		x := require.New(t)
		_, err := d.s.Ungated.Team().Erase(ctx, rstr.TeamRef_builder{Id: d.teamNamed(t, "platform")}.Build())
		x.NoError(err)

		got := search(t, c, kimDN, goldap.ScopeBaseObject, "(objectClass=*)")
		x.Equal([]string{kimDN}, dns(got), "answered success and no entry; right is kim")
		x.Empty(got[0].GetAttributeValues("memberOf"), "the team is gone, and so is her place in it")

		got = search(t, c, peopleDN, goldap.ScopeSingleLevel, "(mail=kim@contoso.example)", "cn", "memberOf")
		x.Equal([]string{kimDN}, dns(got))
	})
}
