package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// One operator's clients are one comma list, and naming the operator twice is
// the way of writing it that reads right and silently is not.
//
// It cost a walk: `docker/login.sh` gave the tenant its second client with a
// second `--client`, the app started with `operators=1` and answered *the login
// is not working* for the first one, and the message it logged was about a
// client nobody could see was missing.
func TestAnOperatorsClientsAreOneList(t *testing.T) {
	x := require.New(t)

	got, err := clientsOf(nil, "NOTHING_", []string{"contoso=demo,behind"})
	x.NoError(err)
	x.Equal(map[string][]string{"contoso": {"demo", "behind"}}, got)

	_, err = clientsOf(nil, "NOTHING_", []string{"contoso=demo", "contoso=behind"})
	x.ErrorContains(err, "named twice")

	// Two operators is what a repeat is for, and still is.
	got, err = clientsOf(nil, "NOTHING_", []string{"contoso=demo", "fabrikam=other"})
	x.NoError(err)
	x.Equal(map[string][]string{"contoso": {"demo"}, "fabrikam": {"other"}}, got)

	// And a flag still layers over the block rather than adding to it, which is
	// a deployment narrowing what it was configured with.
	got, err = clientsOf(map[string][]string{"contoso": {"old"}}, "NOTHING_", []string{"contoso=demo"})
	x.NoError(err)
	x.Equal(map[string][]string{"contoso": {"demo"}}, got)
}
