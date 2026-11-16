package mail_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/mail"
)

// TestAMaskedAddressIsOneWordOfALog: what `Mask` answers is a domain, or
// nothing, and never more than one line.
//
// It answered everything after the first `@` of what it was given, and what it
// is given is what somebody typed into a form anybody reaches: an address that
// was not one, with a line break in it, wrote a line of its own into the log
// of a front door (#277).
func TestAMaskedAddressIsOneWordOfALog(t *testing.T) {
	x := require.New(t)

	x.Equal("…@contoso.com", mail.Mask("erin@contoso.com"))
	x.Equal("…@contoso.com", mail.Mask("Erin <erin@contoso.com>"))

	for _, v := range []string{
		"x@y\naccount: mail to somebody@else.example: sent",
		"x@y\r\nanother line",
		"x@y z",
		"x@" + strings.Repeat("a", 400) + ".example",
		"nothing of the kind",
	} {
		got := mail.Mask(v)
		x.NotContains(got, "\n", "%q", v)
		x.NotContains(got, "\r", "%q", v)
		x.NotContains(got, " ", "%q", v)
		x.LessOrEqual(len(got), 260, "%q", v)
	}
}
