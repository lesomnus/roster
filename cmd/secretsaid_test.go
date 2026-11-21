package cmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/server/vouch"
)

// TestAKeyWrittenWithoutItsNameIsNotSaidBack: `vouch.keys` written without its
// name -- the bare output of `openssl rand -base64 32`, which is the likeliest
// way to get it wrong -- was refused with the whole value in the sentence, and
// that sentence is the container's log. The refusal says the form, and not
// what was written. (The flag's half, `--key` of the consumers, is in `cli`.)
func TestAKeyWrittenWithoutItsNameIsNotSaidBack(t *testing.T) {
	const bare = "XfYoVszmos0DoMNUOSy1vJfaOLVOrq7x6QiNlqRr0G8="

	_, err := vouch.NewKeyring([]string{bare})
	require.Error(t, err)
	require.Contains(t, err.Error(), "name:base64", "the form is said")
	require.NotContains(t, err.Error(), bare)
	require.NotContains(t, err.Error(), bare[:12], "nor any of it")
}

// TestWhatRosterConfigPrintsHoldsNoSecret: `roster config` is what somebody
// pastes into a ticket, and it redacts by a key's name. It printed in clear
// `login.hydra.header`, whose values exist to carry a proxy's credential. The
// file is one with twenty-four marked secrets, each written where a key takes
// one; none of them is printed but three, which the test says why of.
func TestWhatRosterConfigPrintsHoldsNoSecret(t *testing.T) {
	x := require.New(t)

	file := filepath.Join(t.TempDir(), "roster.yaml")
	x.NoError(os.WriteFile(file, []byte(`
db: { driver: pgx, dsn: "postgres://roster:DBPASSWORD1@db.internal:5432/roster?sslmode=disable", dev_dsn: "postgres://dev:DEVPASSWORD2@db.internal:5432/dev" }
watch: { broker: postgres, dsn: "postgres://w:WATCHPASSWORD3@db.internal/roster" }
spin: { lease: postgres, dsn: "postgres://s:SPINPASSWORD4@db.internal/roster" }
control:
  db: { driver: pgx, dsn: "postgres://c:CONTROLPASSWORD5@db.internal/control" }
  watch: { dsn: "postgres://cw:CWATCHPASSWORD6@db.internal/control" }
client:
  addr: "dns:///x:1"
  auth: { scheme: bearer, credential: "rk_CLIENTCREDENTIAL7" }
vouch:
  keys: ["one:VOUCHKEYSECRET8AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="]
audit:
  digest: { key: "base64:DIGESTKEYSECRET9AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", previous: ["base64:DIGESTPREVSECRET10AAAAAAAAAAAAAAAAAAAAAAAAAAAA="] }
  checkpoints: { key: "-----BEGIN PRIVATE KEY-----\nCHECKPOINTKEYSECRET11\n-----END PRIVATE KEY-----" }
  keys: { identity: "AGE-SECRET-KEY-1IDENTITYSECRET12" }
account:
  key: "rk_ACCOUNTKEYLITERAL13"
  keys: { acme: "rt_ACCOUNTKEYSLITERAL14" }
  seal: ["SEALLITERAL15AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="]
  mail: { addr: "smtp:587", username: "MAILUSER16", password: "MAILPASSWORDLITERAL17" }
ldap:
  key: "rk_LDAPKEYLITERAL18"
  keys: { acme: "rt_LDAPKEYSLITERAL19" }
  tls: { cert: /x/cert.pem, key: /x/TLSKEYPATH20.pem }
login:
  key: "rk_LOGINKEYLITERAL21"
  seal: ["LOGINSEALLITERAL22AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="]
  hydra: { admin: "http://admin:HYDRAURLPASSWORD23@hydra:4445", header: { Authorization: "Bearer HYDRAHEADERSECRET24" } }
`), 0o600))

	var out bytes.Buffer
	c := cmd.Config{}
	k := cli.Cmd(&c)
	k.Writer = &out
	x.NoError(k.Run(t.Context(), []string{"--config", file, "config"}))
	printed := out.String()

	for _, v := range []string{
		"DBPASSWORD1", "DEVPASSWORD2", "WATCHPASSWORD3", "SPINPASSWORD4", "CONTROLPASSWORD5", "CWATCHPASSWORD6",
		"CLIENTCREDENTIAL7", "VOUCHKEYSECRET8", "DIGESTKEYSECRET9", "CHECKPOINTKEYSECRET11", "IDENTITYSECRET12",
		"ACCOUNTKEYLITERAL13", "ACCOUNTKEYSLITERAL14", "SEALLITERAL15", "MAILPASSWORDLITERAL17",
		"LDAPKEYLITERAL18", "LDAPKEYSLITERAL19", "TLSKEYPATH20", "LOGINKEYLITERAL21", "LOGINSEALLITERAL22",
		"HYDRAHEADERSECRET24",
	} {
		x.NotContains(printed, v, "printed in clear")
	}

	// Printed, and why. `account.mail.username` is written as itself, which
	// the manual allows: a username is a reference only where a provider
	// makes it a credential. `audit.digest.previous` is payday's field, and
	// the loader redacts by a key's name or a tag on it, neither of which
	// roster has; the manual says to give an old key as a file wherever this
	// may be printed.
	//
	// And `HYDRAURLPASSWORD23`, in `login.hydra.admin`: an address carries no
	// credential -- one for Hydra's admin API goes in `login.hydra.header` --
	// so there is nothing in it to redact, and `roster config` prints a file
	// as it is written. A file that holds one does not start
	// (`TestAnAddressCarriesNoCredential`). Where Hydra is goes on being
	// printed.
	x.Contains(printed, "MAILUSER16")
	x.True(strings.Contains(printed, "DIGESTPREVSECRET10"), "payday's to tag; when it is, this goes in the list above")
	x.Contains(printed, "hydra:4445")
}
