package cli

import (
	"testing"

	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/cfg/ref"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestAFrontDoorsMailIsWhatItsBlockSays: `account.mail` left out is a door
// that cannot send, which is what it was; and what is written wrong is refused
// at start -- a reference to nothing, a sender that is not an address, the
// clear to a host nobody named -- rather than at the first message somebody
// waits for.
//
// The password is a secret of the loader's and the username a value that may
// be kept somewhere else, because some providers' usernames are credentials
// too: each is what it says, the thing itself or what its reference names.
// A password written as itself is the loader's to take and to print as
// `<redacted>`, and no longer roster's to refuse.
//
// Red with the block's errors not named for it, and with the username passed
// on as it was written.
func TestAFrontDoorsMailIsWhatItsBlockSays(t *testing.T) {
	x := require.New(t)

	send, err := mailOf(cmd.MailConfig{})
	x.NoError(err)
	x.Nil(send, "a door with no server was handed a deliverer")

	ok := cmd.MailConfig{Addr: "smtp.example.com:587", From: "Contoso <no-reply@contoso.example>"}
	send, err = mailOf(ok)
	x.NoError(err)
	x.NotNil(send)

	t.Setenv("ROSTER_TEST_MAIL_PASSWORD", "hunter2")
	withPassword := ok
	withPassword.Username, withPassword.Password = mailUsername(t, "apikey"), mailPassword(t, "${env:ROSTER_TEST_MAIL_PASSWORD}")
	_, err = mailOf(withPassword)
	x.NoError(err)
	mc, err := mailConfigOf(withPassword)
	x.NoError(err)
	x.Equal("apikey", mc.Username, "a username written as itself was not")
	x.Equal("hunter2", mc.Password)

	// Written as itself, as the loader allows a secret to be.
	mc, err = mailConfigOf(cmd.MailConfig{Addr: ok.Addr, From: ok.From, Password: mailPassword(t, "hunter2")})
	x.NoError(err)
	x.Equal("hunter2", mc.Password)

	// SES's, say: derived from an access key, and kept where the password is.
	t.Setenv("ROSTER_TEST_MAIL_USER", "AKIAEXAMPLE")
	withUser := withPassword
	withUser.Username = mailUsername(t, "${env:ROSTER_TEST_MAIL_USER}")
	mc, err = mailConfigOf(withUser)
	x.NoError(err)
	x.Equal("AKIAEXAMPLE", mc.Username, "a username's reference was sent as the username")

	for name, c := range map[string]struct {
		mail cmd.MailConfig
		says string
	}{
		"a password to nothing":   {cmd.MailConfig{Addr: ok.Addr, From: ok.From, Username: mailUsername(t, "apikey"), Password: mailPassword(t, "${file:/nowhere/password}")}, "account.mail.password"},
		"a username to nothing":   {cmd.MailConfig{Addr: ok.Addr, From: ok.From, Username: mailUsername(t, "${file:/nowhere/username}"), Password: withPassword.Password}, "account.mail.username"},
		"no sender":               {cmd.MailConfig{Addr: ok.Addr}, "account.mail.from"},
		"the clear to a provider": {cmd.MailConfig{Addr: "smtp.example.com:25", From: ok.From, Tls: "none"}, "account.mail.tls"},
		"a mode there is not":     {cmd.MailConfig{Addr: ok.Addr, From: ok.From, Tls: "ssl"}, "account.mail.tls"},
	} {
		_, err := mailOf(c.mail)
		x.ErrorContains(err, c.says, name)
	}

	// And a sink named in development is the clear it asked for.
	_, err = mailOf(cmd.MailConfig{Addr: "mailpit:1025", From: ok.From, Tls: "none", PlaintextHosts: []string{"mailpit"}})
	x.NoError(err)
}

// mailUsername is `account.mail.username` as a variable gives it.
func mailUsername(t *testing.T, v string) ref.String {
	t.Helper()

	var u ref.String
	require.NoError(t, u.UnmarshalText([]byte(v)))

	return u
}

// mailPassword is `account.mail.password` as a variable gives it.
func mailPassword(t *testing.T, v string) cfg.Secret {
	t.Helper()

	var p cfg.Secret
	require.NoError(t, p.UnmarshalText([]byte(v)))

	return p
}
