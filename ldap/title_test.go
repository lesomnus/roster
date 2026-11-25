package ldap_test

import (
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestAPersonsTitleIsPublished: `Profile.title` is a person's `title` (RFC
// 4519 §2.38), as their department is their `departmentNumber`, and somebody
// with none has no `title`.
//
// A profile had no title, and so a client of the directory that shows who
// somebody is had their department and nothing to say what they do (#415).
// A filter on it is what a filter on an attribute roster does not search is:
// everybody the bind may see is read, and the filter is evaluated over each,
// without regard to case.
func TestAPersonsTitleIsPublished(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	d.renamed(t, d.kim, rstr.Profile_builder{
		DisplayName: "Minji", Department: "Platform", EmployeeNo: "1001", Locale: "ko", Title: "Staff Engineer",
	}.Build())

	kim := one(t, c, kimDN)
	x.Equal([]string{"Staff Engineer"}, kim.GetAttributeValues("title"), "a title said nowhere in the entry")
	x.Equal("Platform", kim.GetAttributeValue("departmentNumber"))

	lee := one(t, c, leeDN)
	x.Empty(lee.GetAttributeValues("title"), "somebody with no title has one")

	asked := one(t, c, kimDN, "title")
	x.Equal([]string{"Staff Engineer"}, asked.GetAttributeValues("title"))
	x.Empty(asked.GetAttributeValues("cn"), "an attribute not asked for")

	for filter, want := range map[string][]string{
		"(title=Staff Engineer)":                       {kimDN},
		"(title=staff engineer)":                       {kimDN},
		"(title=*engineer)":                            {kimDN},
		"(title=*)":                                    {kimDN},
		"(title=Accountant)":                           {},
		"(&(objectClass=inetOrgPerson)(!(title=*)))":   {leeDN, adminDN},
		"(&(departmentNumber=Platform)(title=Staff*))": {kimDN},
	} {
		t.Run(filter, func(t *testing.T) {
			got := search(t, c, peopleDN, goldap.ScopeSingleLevel, filter, "1.1")
			require.ElementsMatch(t, want, dns(got))
		})
	}
}
