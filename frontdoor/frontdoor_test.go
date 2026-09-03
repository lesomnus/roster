package frontdoor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/auth/authsession"

	rstr "github.com/lesomnus/roster/rstr"
)

// What this package does end to end is pinned where it is used --
// `examples/sso` drives both forms, the half session, the wrong second factor
// and the delegation through a real roster. What is here is what that cannot
// reach: a misconfiguration that has to be refused before anybody types a
// password, and a map whose failure mode is to grow forever.

func TestAnAppHasToSayWhatItAsksFor(t *testing.T) {
	ok := Config{
		Sessions:   authsession.New(authsession.NewMemStore()),
		Vouch:      rstr.NewVouchServiceClient(nil),
		Delegation: rstr.NewDelegationServiceClient(nil),
		Methods:    []string{rstr.MeService_Get_FullMethodName},
		Tenant:     func(ctx context.Context, host string) (string, error) { return "contoso", nil },
	}

	for _, tc := range []struct {
		name string
		with func(*Config)
		want string
	}{
		{"no sessions", func(c *Config) { c.Sessions = nil }, "Sessions"},
		{"no roster", func(c *Config) { c.Vouch = nil }, "Vouch"},
		{"no methods", func(c *Config) { c.Methods = nil }, "Methods"},
		{"no tenant", func(c *Config) { c.Tenant = nil }, "Tenant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := ok
			tc.with(&c)

			_, err := New(c)
			require.ErrorContains(t, err, tc.want)
		})
	}

	// An empty `Methods` is the one worth stating: it is not a nil check being
	// thorough. A delegation minted with nothing allows no method at all, so
	// the app would sign somebody in and then be refused on the first call it
	// made for them -- and the page would say the session cannot act, which is
	// a sentence about the person rather than about the deployment.
	c := ok
	c.Methods = []string{}
	_, err := New(c)
	require.ErrorContains(t, err, "opens no door")

	d, err := New(ok)
	require.NoError(t, err)
	require.Equal(t, HalfLife, d.c.Half, "zero takes the default rather than expiring immediately")
}
