package front_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/server/front"
)

// TestAHostIsStoredAsItIsCompared is the one promise [front.Hostname] makes,
// and it is made to two callers at once: `Host.Add` refuses a name that is not
// already what this answers, and `WhoseHost` asks this what a browser arrived
// at. A form the two do not agree on is a row that is written, listed, and
// never matched -- and what finds out is a sign-in page saying nobody is there.
func TestAHostIsStoredAsItIsCompared(t *testing.T) {
	// An address literal has more than one spelling, and the one a request
	// arrives as is the bracketed one -- a `Host` header cannot carry an IPv6
	// address any other way. So the bracketed and the bare form have to
	// normalise to one name; storing the other spelling is storing a name
	// nothing ever asks for.
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"contoso.example.com", "contoso.example.com"},
		{"CONTOSO.Example.com", "contoso.example.com"},
		{"  contoso.example.com  ", "contoso.example.com"},
		{"contoso.example.com:8443", "contoso.example.com"},

		{"[::1]", "::1"},
		{"[::1]:8443", "::1"},
		{"::1", "::1"},
		{"[fe80::1]:8443", "fe80::1"},
		{"fe80::1", "fe80::1"},
		{"[2001:DB8::7]", "2001:db8::7"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			x := require.New(t)

			got := front.Hostname(tc.in)
			x.Equal(tc.want, got)

			// And it settles, because `Host.Add` compares its answer to what it
			// was given: a function that moved twice would refuse the very name
			// it had just told the operator to write.
			x.Equal(got, front.Hostname(got), "what it answers is what it stores")
		})
	}
}
