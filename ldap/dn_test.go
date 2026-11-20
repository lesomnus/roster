package ldap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAValueIsReadBackAsItWasWritten holds the two halves of a name to one
// rule about white space. Writing escaped a space at either end of a value,
// the space and nothing else, which is what RFC 4514 § 2.4 says. Reading
// then took off every kind of white space Unicode has from either end of the
// whole name, and every space from the end of each value whether or not it
// had been written escaped. So a value that ended in a tab was written as it
// was and read back without it, and one that ended in a space was written
// escaped and read back without it all the same.
//
// Nothing roster writes into a name can hold either today: a name's values
// are aliases, and an alias is held to a grammar. That is a rule somewhere
// else, and the day it is not the rule a name read back is another name.
//
// Reading takes off what writing would have escaped and no more: the space,
// where it was not escaped.
func TestAValueIsReadBackAsItWasWritten(t *testing.T) {
	for _, v := range []string{
		"kim", "kim\t", "kim\f", "kim\n", "\tkim", "kim ", "kim ",
		"kim ", "kim  ", " kim", "  kim", " ", "  ", "a,b ", `a\ `, "#kim", "",
	} {
		written := dn{{attr: "uid", value: v}, {attr: "ou", value: "people"}, {attr: "o", value: v}}.String()
		read, err := parseDN(written)
		require.NoError(t, err, "%q, written as %q", v, written)
		require.Len(t, read, 3, "%q, written as %q", v, written)
		require.Equal(t, v, read[0].value, "written as %q", written)
		require.Equal(t, v, read[2].value, "the last value of %q", written)
	}

	t.Run("and a space nobody escaped is still nobody's", func(t *testing.T) {
		x := require.New(t)
		read, err := parseDN("  UID = kim , OU = people , O = contoso  ")
		x.NoError(err)
		x.Equal(dn{{attr: "uid", value: "kim"}, {attr: "ou", value: "people"}, {attr: "o", value: "contoso"}}, read)

		none, err := parseDN("   ")
		x.NoError(err)
		x.Empty(none)
	})

	t.Run("and one that was escaped is the value's", func(t *testing.T) {
		x := require.New(t)
		read, err := parseDN(`uid=kim\ ,o=\ contoso\20`)
		x.NoError(err)
		x.Equal(dn{{attr: "uid", value: "kim "}, {attr: "o", value: " contoso "}}, read)

		// What the fuzzer found: a tab written as its hex pair.
		read, err = parseDN(`0=0\09`)
		x.NoError(err)
		x.Equal("0\t", read[0].value)
		again, err := parseDN(read.String())
		x.NoError(err)
		x.Equal(read, again)
	})
}
