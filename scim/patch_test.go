package scim

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBothDialectsOfAPatchReadTheSame: Entra's PATCH as it wrote it for years
// -- capitalised ops, booleans as strings, several attributes in one operation
// with no path -- and the RFC's are one reading here.
func TestBothDialectsOfAPatchReadTheSame(t *testing.T) {
	for _, tc := range []struct {
		desc string
		body string
	}{
		{"the RFC's", `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[
			{"op":"replace","path":"active","value":false},
			{"op":"replace","path":"displayName","value":"Erin K."},
			{"op":"replace","path":"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:department","value":"Platform"}]}`},
		{"Entra's", `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[
			{"op":"Replace","path":"active","value":"False"},
			{"op":"Replace","path":"displayName","value":"Erin K."},
			{"op":"Add","path":"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:department","value":"Platform"}]}`},
		{"one operation, no path", `{"Operations":[{"op":"replace","value":{
			"active":"False","displayName":"Erin K.",
			"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User":{"department":"Platform"}}}]}`},
		{"and the extension's attribute flattened", `{"operations":[{"op":"Replace","value":{
			"active":false,"name":{"formatted":"Erin K."},
			"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:department":"Platform"}}]}`},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)
			c, err := parsePatch([]byte(tc.body))
			x.NoError(err)
			x.NotNil(c.active)
			x.False(*c.active)
			x.Equal("Erin K.", *c.display)
			x.Equal("Platform", *c.department)
		})
	}

	t.Run("a remove takes a value away, and what is not kept is said", func(t *testing.T) {
		x := require.New(t)
		c, err := parsePatch([]byte(`{"Operations":[
			{"op":"remove","path":"displayName"},
			{"op":"replace","path":"title","value":"Engineer"},
			{"op":"add","path":"phoneNumbers[type eq \"work\"].value","value":"+82 2 000"}]}`))
		x.NoError(err)
		x.Equal("", *c.display)
		x.Equal([]string{"title", `phoneNumbers[type eq "work"].value`}, c.ignored)
	})

	t.Run("and what is not an operation is refused", func(t *testing.T) {
		for _, body := range []string{
			`{"Operations":[]}`,
			`{"Operations":[{"op":"move","path":"active","value":true}]}`,
			`{"Operations":[{"op":"replace","path":"active","value":"maybe"}]}`,
			`{"Operations":[{"op":"remove"}]}`,
			`not json`,
		} {
			_, err := parsePatch([]byte(body))
			require.Error(t, err, body)
		}
	})
}

// TestTheFiltersADirectoryAsksAreRead: the lookups a directory makes before
// it creates, Entra's nested address path among them, and a value that says
// "eq" itself.
func TestTheFiltersADirectoryAsksAreRead(t *testing.T) {
	for _, tc := range []struct {
		filter, attr, value string
	}{
		{`userName eq "erin@contoso.example"`, byUserName, "erin@contoso.example"},
		{`UserName EQ "erin@contoso.example"`, byUserName, "erin@contoso.example"},
		{`externalId eq "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"`, byExternalId, "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"},
		{`emails[type eq "work"].value eq "erin@contoso.example"`, byEmail, "erin@contoso.example"},
		{`userName eq "a eq b"`, byUserName, "a eq b"},
	} {
		t.Run(tc.filter, func(t *testing.T) {
			x := require.New(t)
			attr, value, err := parseFilter(tc.filter)
			x.NoError(err)
			x.Equal(tc.attr, attr)
			x.Equal(tc.value, value)
		})
	}

	for _, filter := range []string{
		`displayName eq "Erin"`,
		`userName co "erin"`,
		`userName eq erin`,
		`userName eq "erin" and active eq true`,
	} {
		_, _, err := parseFilter(filter)
		require.Error(t, err, filter)
	}
}
