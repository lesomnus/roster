package front_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/server/front"
)

// TestAnAddressIsAPlainMailboxAtAName: what [front.NotAddress] refuses, one of
// each kind, and what it leaves alone.
//
// Nothing asked this of an address. It was lowered and trimmed, and whatever
// was left was a row: two words with no `@`, a name and an address in one
// string, the same mailbox with brackets round it or a dot after it. The mail
// sender reads the bracketed form as the plain one, so those were three names
// for one place a link is delivered to. Each kind is refused here by a
// sentence that says which it is.
func TestAnAddressIsAPlainMailboxAtAName(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		why  string
	}{
		{"two words and no @", "not an address", "no @"},
		{"a second @", "a@b@c.example", "plain address"},
		{"a line break inside", "x\ny@contoso.example", "plain address"},
		{"a tab inside", "x\ty@contoso.example", "plain address"},
		{"a space inside", "x y@contoso.example", "plain address"},
		{"a name and an address", `"ceo@contoso.example" <plain@elsewhere.example>`, "plain address"},
		{"brackets round it", "<plain@contoso.example>", "plain address"},
		{"a quoted mailbox", `"plain"@contoso.example`, "plain address"},
		{"a comment", "plain(the plain one)@contoso.example", "plain address"},
		{"a dot after the name", "plain@contoso.example.", "ends in a dot"},
		{"a dot before the @", "plain.@contoso.example", "a dot begins or ends"},
		{"a dot first", ".plain@contoso.example", "a dot begins or ends"},
		{"two dots together", "pl..ain@contoso.example", "a dot begins or ends"},
		{"nothing before the @", "@contoso.example", "nothing before the @"},
		{"nothing after the @", "plain@", "nothing after the @"},
		{"a composed letter before the @", "pláin@contoso.example", "outside ASCII"},
		{"the same letter decomposed", "pláin@contoso.example", "outside ASCII"},
		{"a full-width letter before the @", "ｐlain@contoso.example", "outside ASCII"},
		{"a name in its own letters", "plain@bücher.example", "A-label"},
		{"an address literal", "plain@[192.0.2.1]", "address literal"},
		{"a bare address", "plain@192.0.2.1", "address literal"},
		{"an underscore in the name", "plain@con_toso.example", "not a hostname"},
		{"an empty label", "plain@contoso..example", "not a hostname"},
		{"more than 64 bytes before the @", strings.Repeat("a", front.MaxLocal+1) + "@contoso.example", "64 bytes"},
		{"more than 254 bytes", "plain@" + strings.Repeat("a.", 125) + "example", "254 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Contains(t, front.NotAddress(tc.in), tc.why, "%q", tc.in)
		})
	}

	for _, ok := range []string{
		"plain@contoso.example",
		"first.last@contoso.example",
		"o'brien+billing@contoso.example",
		"a_b-c=d/e@sub.contoso.example",
		"someone@localhost",
		"plain@xn--bcher-kva.example",
		strings.Repeat("a", front.MaxLocal) + "@contoso.example",
	} {
		require.Empty(t, front.NotAddress(ok), "%q is an address", ok)
	}

	// The longest there is: 254 bytes, 64 of them before the `@`.
	longest := strings.Repeat("a", front.MaxLocal) + "@" + strings.Repeat("b", 63) + "." +
		strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	require.Len(t, longest, front.MaxAddress)
	require.Empty(t, front.NotAddress(longest))
}
