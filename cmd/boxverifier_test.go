package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// keyIdSaid is the identifier a mint says of the key it printed, on standard
// error: `key <id> for …`.
func keyIdSaid(t *testing.T, said string) string {
	t.Helper()

	for _, f := range strings.Fields(said) {
		if _, err := pdid.Parse(f); err == nil {
			return f
		}
	}
	t.Fatalf("no key identifier in: %s", said)

	return ""
}

// TestTheBoxAnswersNoVerifier: what a command at the box prints holds no
// column declared secret -- a password's verifier, a key's hash -- on either
// plane and in no format, as no port answers one.
//
// The pipe a command at the box reaches was `Ungated` with the access record
// in front of it and nothing else, and the layer that clears a `secret` column
// is on the walled stack alone: so `roster --as admin credential list -o json`
// printed the argon2id verifier of every password on the page, and `api-key
// get` with a select the hash of a key, and neither is a read of anybody's
// personal data, so neither was a row of the access record. Answered: the
// verifier; right is none, as at every port (`TestTheAdminListenerAnswersNoVerifier`).
// The commands that check one (`vouch`, `keys`) read it in process, and still do.
func TestTheBoxAnswersNoVerifier(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	_, _, err := boxRun(t, at(b.c, "admin"), "", "holder", "add", "@newco/alice")
	x.NoError(err)
	_, _, err = boxRun(t, at(b.c, "admin"), "a-long-enough-password-for-alice\n", "vouch", "set", "--password-stdin", "@newco/alice")
	x.NoError(err)
	_, said, err := boxRun(t, at(b.c, "admin"), "", "key", "add", "--tenant", "newco", "--holder", "alice", "--allow", "/roster.MeService/Get")
	x.NoError(err)
	customers := keyIdSaid(t, said)
	_, said, err = boxRun(t, at(b.c, "admin"), "", "control", "key", "add", "--allow", "/roster.HolderService/Get", "loft")
	x.NoError(err)
	ours := keyIdSaid(t, said)

	for _, plane := range []struct {
		name   string
		prefix []string
		key    string
	}{
		{"the customers'", nil, customers},
		{"the control plane", []string{"control"}, ours},
	} {
		for _, format := range []string{"json", "protojson", "prototext"} {
			named := `"secret"`
			if format == "prototext" {
				named = "secret:"
			}

			for _, args := range [][]string{
				{"credential", "list", "-o", format},
				{"api-key", "get", "-o", format, plane.key, `{"select":{"secret":true,"alias":true}}`},
				{"api-key", "list", "-o", format},
			} {
				args := append(append([]string{}, plane.prefix...), args...)
				out, _, err := boxRun(t, at(b.c, "admin"), "", args...)
				x.NoError(err, "%s: %v", plane.name, args)
				x.NotEmpty(strings.TrimSpace(out), "%s: %v printed nothing, which proves nothing", plane.name, args)
				x.NotContains(out, named, "%s: %v printed a verifier", plane.name, args)
			}
		}
	}
}
