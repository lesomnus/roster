package cmd_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestAMintedSecretIsAloneOnStandardOutput: what `roster sign-in` and `roster
// init` mint is the one thing on standard output, and everything said to the
// person beside it is on standard error -- the shape every other command that
// mints answers in, so that `$(roster …)` and `> file` are the secret.
//
// Both printed through standard output for everything. `roster sign-in … >
// key` left the address and the code to type in the file and nothing on the
// terminal, so nobody could approve it, where `cli/signin.go` says the output
// is the key and nothing else; and `init`'s password was line four of ten.
// Answered: the prose with the secret; right is the secret alone.
func TestAMintedSecretIsAloneOnStandardOutput(t *testing.T) {
	t.Run("init", func(t *testing.T) {
		x := require.New(t)
		c := seedbed(t)

		out, said, err := boxRun(t, &c, "", "init")
		x.NoError(err, said)
		password := strings.TrimSuffix(out, "\n")
		x.NotEmpty(password)
		x.NotContains(password, "\n", "standard output is the password and nothing else")
		x.NotContains(password, " ")
		x.Contains(said, "/roster.*/*", "what was made is said beside it")
		x.Contains(said, "there are no customers yet")
		x.NotContains(said, password, "the password is said once, where a script reads it")

		s, err := cmd.Build(t.Context(), c)
		x.NoError(err)
		defer s.Close()
		x.NotNil(signIn(t, s, "admin", password), "what init printed alone is the password")

		t.Run("and one it was given is printed nowhere", func(t *testing.T) {
			x := require.New(t)
			c := seedbed(t)

			out, said, err := boxRun(t, &c, "a password somebody chose\n", "init", "--password-stdin")
			x.NoError(err, said)
			x.Empty(out)
			x.NotContains(said, "a password somebody chose")
		})
	})

	t.Run("sign-in", func(t *testing.T) {
		x := require.New(t)

		const key = "rt_the-key-this-terminal-was-handed"
		door := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "application/json")
			switch r.URL.Path {
			case "/device/begin":
				json.NewEncoder(w).Encode(map[string]any{
					"device_code": "dc", "user_code": "WDJB-MJHT",
					"verification_uri":          "https://account.example.test/device",
					"verification_uri_complete": "https://account.example.test/device?code=WDJB-MJHT",
					"expires_in":                60, "interval": 1,
				})
			case "/device/poll":
				json.NewEncoder(w).Encode(map[string]any{"token": key})
			default:
				http.NotFound(w, r)
			}
		}))
		defer door.Close()

		c := cmd.Config{}
		out, said, err := boxRun(t, &c, "", "sign-in", "--at", door.URL, "--allow", "/roster.MeService/Get")
		x.NoError(err, said)
		x.Equal(key+"\n", out, "standard output is the key and nothing else")
		x.Contains(said, "WDJB-MJHT", "the code to type is said where the person sees it")
		x.Contains(said, "only time it is shown")
		x.NotContains(said, key)
	})
}
