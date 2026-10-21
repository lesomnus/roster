package arrives

import (
	"net/url"
	"testing"
	"time"

	"github.com/lesomnus/payday/slug"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
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
		{"erin.hart@contoso.example", "erin-hart"},
		{"Erin.Hart@contoso.example", "erin-hart"},
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

// When a provider says it last authenticated somebody, read as a number --
// what OpenID Connect says it is -- or as the string some providers write, and
// nothing for anything else: a claim a sign-in does not depend on is never the
// reason one fails.
func TestAnAuthTimeIsReadLoosely(t *testing.T) {
	x := require.New(t)

	x.Equal(time.Unix(1700000000, 0), authTime(float64(1700000000)))
	x.Equal(time.Unix(1700000000, 0), authTime("1700000000"))
	for _, v := range []any{nil, "", "yesterday", float64(0), float64(-1), map[string]any{}, true} {
		x.True(authTime(v).IsZero(), "%#v", v)
	}
}

// What asks a provider to authenticate somebody again: OpenID Connect's
// `max_age=0` everywhere, and `prompt=login` only where Microsoft's sign-in
// documents it -- a provider that does not know a prompt may refuse the whole
// request over it.
func TestAskingAgainIsMaxAgeAndAtMicrosoftAPrompt(t *testing.T) {
	x := require.New(t)
	cfg := &oauth2.Config{Endpoint: oauth2.Endpoint{AuthURL: "https://idp.example.test/authorize"}}

	asked := func(v *verifier) url.Values {
		t.Helper()
		u, err := url.Parse(cfg.AuthCodeURL("state", askAgain(v)...))
		x.NoError(err)

		return u.Query()
	}

	q := asked(&verifier{})
	x.Equal("0", q.Get("max_age"))
	x.False(q.Has("prompt"), "a prompt went to a provider that may not know it")

	q = asked(&verifier{microsoft: true})
	x.Equal("0", q.Get("max_age"))
	x.Equal("login", q.Get("prompt"))
}
