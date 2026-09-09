package arrives

import (
	"testing"

	"github.com/lesomnus/payday/slug"
	"github.com/stretchr/testify/require"
)

// What a directory hands out, and what roster may call a row.
//
// The two do not overlap as much as they look like they do: an alias begins
// with a lowercase letter and holds lowercase letters, digits and single
// hyphens, and the commonest corporate address form -- `first.last` -- is not
// one. This derived the local part unchanged for a while, so the first person
// with a dot in their address reached the end of a whole sign-in and got a 500.
func TestAnAddressBecomesANameARowMayHave(t *testing.T) {
	for _, c := range []struct{ address, want string }{
		{"seunghyun.hwang@hday.dev", "seunghyun-hwang"},
		{"Seunghyun.Hwang@hday.dev", "seunghyun-hwang"},
		{"  erin@contoso.example  ", "erin"},
		{"erin+tag@contoso.example", "erin-tag"},
		{"erin_h@contoso.example", "erin-h"},
		{"erin-h@contoso.example", "erin-h"},
		{"e..r..in@contoso.example", "e-r-in"},
		{".erin.@contoso.example", "erin"},
		{"3rin@contoso.example", "rin"},
		{"erin3@contoso.example", "erin3"},
	} {
		t.Run(c.address, func(t *testing.T) {
			x := require.New(t)
			got := aliasOf(c.address)
			x.Equal(c.want, got)
			x.NoError(slug.Validate(got))
		})
	}
}

// And the ones there is no name in at all, where the answer is a name nobody
// chose rather than a refusal: payday's own answer to a row that needs one
// before anybody has an opinion about it.
func TestAnAddressWithNoNameInItGetsOneNobodyChose(t *testing.T) {
	for _, address := range []string{"123@contoso.example", "@contoso.example", "", "...@x", "한글@x"} {
		t.Run(address, func(t *testing.T) {
			x := require.New(t)
			got := aliasOf(address)
			x.NoError(slug.Validate(got), "%q became %q", address, got)
			x.Len(got, 7)
		})
	}
}

// Sixty-three is a DNS label, and a name that came from an address must not be
// the reason a row cannot be one.
func TestALongAddressIsCutToAnAlias(t *testing.T) {
	x := require.New(t)

	long := ""
	for range 40 {
		long += "ab."
	}
	got := aliasOf(long + "@contoso.example")
	x.NoError(slug.Validate(got))
	x.LessOrEqual(len(got), slug.AliasMaxLen)
}
