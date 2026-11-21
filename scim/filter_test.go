package scim

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// allocated is how many bytes f allocates, the least of a few runs.
func allocated(f func()) uint64 {
	var least uint64
	for i := range 3 {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		f()
		runtime.ReadMemStats(&after)
		if n := after.TotalAlloc - before.TotalAlloc; i == 0 || n < least {
			least = n
		}
	}

	return least
}

// TestAFilterIsReadInOnePass: reading `attribute eq "value"` costs what it is
// long.
//
// It copied what was left of the string, and parsed it, for every ` eq ` in
// it: a PATCH path that said ` eq ` 230,400 times was read in 32 s, and a
// megabyte of filter in 44 s, inside a turn. Nothing is refused that was
// taken: the quoted value is the end of the string, so where it begins is
// found once, from the end, and asked once. What is counted is the bytes the
// reading allocates, which grow with the square of the length where it reads
// the rest again at each ` eq `: ten times as long is about ten times as
// much, and not a hundred.
func TestAFilterIsReadInOnePass(t *testing.T) {
	x := require.New(t)

	path := func(n int) string { return strings.Repeat("value eq ", n) + `"x"` }
	read := func(n int) uint64 {
		v := path(n)

		return allocated(func() {
			attr, value, err := splitEq(v)
			x.NoError(err)
			x.Equal("x", value)
			x.Equal(strings.TrimSuffix(strings.Repeat("value eq ", n), " eq "), attr)
		})
	}

	short, long := read(1000), read(10000)
	x.Less(long, 30*max(short, 1), "a path ten times as long allocated %d bytes to the other's %d", long, short)
}

// TestWhatAFilterTakesIsWhatItTook: the answers of the reader that read the
// rest again at each ` eq `, for every filter its tests, the reader's probe
// and the corners of its grammar name. One pass gives each of them.
func TestWhatAFilterTakesIsWhatItTook(t *testing.T) {
	for _, tc := range []struct {
		filter, attr, value string
		taken               bool
	}{
		{"userName eq \"erin@contoso.example\"", "userName", "erin@contoso.example", true},
		{"USERNAME EQ \"ERIN@CONTOSO.EXAMPLE\"", "USERNAME", "ERIN@CONTOSO.EXAMPLE", true},
		{"userName  eq  \"erin@contoso.example\"", "userName", "erin@contoso.example", true},
		{"externalId eq \"0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0\"", "externalId", "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0", true},
		{"emails.value eq \"erin@contoso.example\"", "emails.value", "erin@contoso.example", true},
		{"emails[type eq \"work\"].value eq \"erin@contoso.example\"", "emails[type eq \"work\"].value", "erin@contoso.example", true},
		{"userName eq \"\"", "userName", "", true},
		{"userName eq \"a\\\"b\"", "userName", "a\"b", true},
		{"userName eq \"a eq b\"", "userName", "a eq b", true},
		{"userName eq \"erin@contoso.example\" or userName eq \"nobody@contoso.example\"", "userName eq \"erin@contoso.example\" or userName", "nobody@contoso.example", true},
		{"userName eq \"erin@contoso.example\" and active eq true", "", "", false},
		{"not (userName eq \"erin@contoso.example\")", "", "", false},
		{"(userName eq \"erin@contoso.example\")", "", "", false},
		{"userName ne \"erin@contoso.example\"", "", "", false},
		{"userName co \"erin\"", "", "", false},
		{"userName sw \"erin\"", "", "", false},
		{"userName pr", "", "", false},
		{"userName eq 'erin@contoso.example'", "", "", false},
		{"userName eq erin@contoso.example", "", "", false},
		{"urn:ietf:params:scim:schemas:core:2.0:User:userName eq \"erin@contoso.example\"", "urn:ietf:params:scim:schemas:core:2.0:User:userName", "erin@contoso.example", true},
		{"title eq \"x\"", "title", "x", true},
		{"meta.lastModified gt \"2011-05-13T04:42:34Z\"", "", "", false},
		{"active eq true", "", "", false},
		{" ", "", "", false},
		{"", "", "", false},
		{"displayName eq \"Ops\" or displayName eq \"x\"", "displayName eq \"Ops\" or displayName", "x", true},
		{"displayName co \"O\"", "", "", false},
		{"type eq \"work\"", "type", "work", true},
		{"value eq \"0f1e2d3c\"", "value", "0f1e2d3c", true},
		{"a eq null", "a", "", true},
		{"a eq \"x\" eq \"y\"", "a eq \"x\"", "y", true},
		{"a eq \"\\\" eq \\\"b\"", "a", "\" eq \"b", true},
		{"a eq \"x\\\\\"", "a", "x\\", true},
		{"a eq \"x\\\\\\\"", "", "", false},
		{"eq \"x\"", "", "", false},
		{" eq \"x\"", "", "", false},
		{"a eq\"x\"", "", "", false},
		{"a\teq \"x\"", "", "", false},
		{"a eq\t\"x\"", "", "", false},
		{"a eq  \t \"x\"\t", "a", "x", true},
		{"a eq \"line\\nbreak\"", "a", "line\nbreak", true},
		{"a eq \"é\"", "a", "é", true},
		{"a eq \"bad \\q escape\"", "", "", false},
		{"a eq \"unterminated", "", "", false},
		{"a eq \"x\" \"y\"", "", "", false},
		{"a eq eq \"x\"", "", "", false},
		{"a eq \"x\" eq", "", "", false},
		{"a EQ b eq \"c\"", "a EQ b", "c", true},
		{"a eq \"x\"y\"", "", "", false},
		{"a eq \"é\"", "a", "é", true},
		{"\"a\" eq \"b\"", "\"a\"", "b", true},
		{"a eq \"\" ", "a", "", true},
	} {
		attr, value, err := splitEq(tc.filter)
		require.Equal(t, tc.taken, err == nil, "%q: %v", tc.filter, err)
		require.Equal(t, tc.attr, attr, "%q", tc.filter)
		require.Equal(t, tc.value, value, "%q", tc.filter)
	}
}
