package prove

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAZoneIsLookedForFromTheInsideOut is the off-by-one in [candidates], which
// is the only arithmetic in this package.
//
// Where the zone cut is cannot be known from a name -- `foo.example.com` may be
// its own zone or a record in `example.com`'s, and both are ordinary -- so the
// closest `NS` that answers is the answer. What this pins is the two ends: the
// challenge label is never asked about, and the last label alone is never asked
// either, because a TLD's servers hold no TXT record this is looking for.
func TestAZoneIsLookedForFromTheInsideOut(t *testing.T) {
	for _, tt := range []struct {
		name string
		want []string
	}{
		{
			// The ordinary one, and the shape every call actually makes:
			// `Held` asks about `_roster-challenge.<name>`.
			name: Record + ".contoso.example.com",
			want: []string{"contoso.example.com", "example.com"},
		},
		{
			// A name several labels deep, where the cut could be anywhere.
			name: Record + ".eu.apps.contoso.example.com",
			want: []string{"eu.apps.contoso.example.com", "apps.contoso.example.com", "contoso.example.com", "example.com"},
		},
		{
			// Trailing dot, which a zone file has and a request may.
			name: Record + ".contoso.example.com.",
			want: []string{"contoso.example.com", "example.com"},
		},
		{
			// Nothing enclosing to ask about, so nothing to ask -- and the
			// caller answers *nothing says which nameservers hold this*
			// rather than asking the root.
			name: "example.com",
			want: nil,
		},
		{
			name: "localhost",
			want: nil,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, candidates(tt.name))
		})
	}
}

// TestATokenSaysWhatItIsAndIsNotGuessable is the two things the value carries.
func TestATokenSaysWhatItIsAndIsNotGuessable(t *testing.T) {
	x := require.New(t)

	a, err := Token()
	x.NoError(err)
	b, err := Token()
	x.NoError(err)

	// The prefix is part of the token rather than added on the way out, so that
	// *publish this string* is literal and the comparison is an equality.
	x.True(strings.HasPrefix(a, Prefix), "a token does not say what it is: %q", a)
	x.NotEqual(a, b, "two tokens are the same, so one claim proves another")

	// Long enough that a zone somebody else controls does not happen to say it.
	x.Greater(len(a)-len(Prefix), 40)
}

// TestWhatIsPublishedIsComparedAtTheChallengeRecord is [Held], which is the
// whole of what this package answers.
func TestWhatIsPublishedIsComparedAtTheChallengeRecord(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	const name = "contoso.example.com"
	token, err := Token()
	x.NoError(err)

	t.Run("nothing published is no, and not an error", func(t *testing.T) {
		x := require.New(t)

		ok, err := Held(ctx, Static{}, name, token)
		x.NoError(err)
		x.False(ok)
	})

	t.Run("and it is looked for under the challenge label, not the name", func(t *testing.T) {
		x := require.New(t)

		// A token on the name itself proves nothing: the label is what keeps a
		// claim off a name a tenant serves.
		ok, err := Held(ctx, Static{name: []string{token}}, name, token)
		x.NoError(err)
		x.False(ok)
	})

	t.Run("published beside other records, it is yes", func(t *testing.T) {
		x := require.New(t)

		// A zone holds more than one TXT record at a name -- an SPF line, another
		// vendor's verification -- so the answer is whether ours is among them
		// rather than whether it is the only one.
		ok, err := Held(ctx, Static{Record + "." + name: []string{
			"v=spf1 -all", "google-site-verification=whatever", token,
		}}, name, token)
		x.NoError(err)
		x.True(ok)
	})

	t.Run("and a lookup that could not be made is neither", func(t *testing.T) {
		x := require.New(t)

		// The third answer, and the caller needs it apart: telling somebody
		// their zone is wrong when the query never left is how an afternoon goes
		// on a correct zone file.
		_, err := Held(ctx, Refusing{Err: errors.New("no route")}, name, token)
		x.ErrorContains(err, "no route")
	})
}

// TestADeploymentSaysWhetherItCanAskAtAll is [Config.Asking]'s three answers.
func TestADeploymentSaysWhetherItCanAskAtAll(t *testing.T) {
	x := require.New(t)

	// Empty is the system's, which is the ordinary case.
	x.NotNil(Config{}.Asking())

	// A resolver of its own, for split horizon.
	x.NotNil(Config{Resolver: "192.0.2.1:53"}.Asking())

	// And `none` is a deployment that cannot ask -- an air gap. Nil rather than
	// a resolver that fails, so that `server/core` refuses a claim naming the
	// setting instead of timing out on a resolver that is not there.
	x.Nil(Config{Resolver: None}.Asking())

	// What a test hands over wins, and it is the one field not in the file.
	given := Static{}
	x.Equal(Resolver(given), Config{Resolver: None, Asks: given}.Asking())
}
