package arrives

import (
	"encoding/json"
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

// What each provider is asked for beside the challenge and the nonce, which is
// what it documents: OpenID Connect's `max_age=0` for an ordinary provider;
// Microsoft's `prompt=login` with `auth_time` requested explicitly, avoiding
// the expired-token response some Entra tenants give `max_age=0`; and at
// Google, `auth_time` on every round trip, again or not.
func TestEachProviderIsAskedWhatItDocuments(t *testing.T) {
	cfg := &oauth2.Config{Endpoint: oauth2.Endpoint{AuthURL: "https://idp.example.test/authorize"}}

	for _, c := range []struct {
		name  string
		v     *verifier
		again bool
		want  url.Values
	}{
		{"an OpenID provider", &verifier{}, false, url.Values{}},
		{"an OpenID provider, again", &verifier{}, true, url.Values{"max_age": {"0"}}},
		{"Microsoft", &verifier{microsoft: true}, false, url.Values{}},
		{"Microsoft, again", &verifier{microsoft: true}, true, url.Values{"prompt": {"login"}, "claims": {authTimeClaims}}},
		{"Google", &verifier{google: true}, false, url.Values{"claims": {authTimeClaims}}},
		{"Google, again", &verifier{google: true}, true, url.Values{"claims": {authTimeClaims}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := require.New(t)
			u, err := url.Parse(cfg.AuthCodeURL("state", asking(c.v, c.again)...))
			x.NoError(err)

			got := u.Query()
			for _, k := range []string{"client_id", "response_type", "state"} {
				got.Del(k)
			}
			x.Equal(c.want, got)
		})
	}

	// And what Google is asked for is what its guide says to send: the
	// `auth_time` of the ID token, essential.
	var claims struct {
		IdToken map[string]struct {
			Essential bool `json:"essential"`
		} `json:"id_token"`
	}
	require.NoError(t, json.Unmarshal([]byte(authTimeClaims), &claims))
	require.True(t, claims.IdToken["auth_time"].Essential)
}

// Google's sign-in is its host over https, and nothing that only looks like it
// -- and, told its Google is elsewhere, that and nothing else.
func TestGoogleIsGooglesHost(t *testing.T) {
	x := require.New(t)

	p := &Providers{}
	x.True(p.google("https://accounts.google.com"))
	x.True(p.google("https://accounts.google.com/"))
	x.True(p.google("https://Accounts.Google.com"))

	for _, issuer := range []string{
		"http://accounts.google.com",
		"https://accounts.google.com.example.test",
		"https://example.test/accounts.google.com",
		"https://google.com",
		"accounts.google.com",
		"",
	} {
		x.False(p.google(issuer), issuer)
	}

	p.Google = "http://127.0.0.1:8080"
	x.True(p.google("http://127.0.0.1:8080"))
	x.False(p.google("https://accounts.google.com"), "a front door told its Google is elsewhere took Google's own for it too")
}
