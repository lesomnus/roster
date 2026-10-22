package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestAFrontDoorsMailIsWhatItsBlockSays: `account.mail` left out is a door
// that cannot send, which is what it was; and what is written wrong is refused
// at start -- a password written as itself, a reference to nothing, a sender
// that is not an address, the clear to a host nobody named -- rather than at
// the first message somebody waits for.
//
// Red with the reference rule gone (a literal password accepted), and with
// the block's errors not named for it.
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
	withPassword.Username, withPassword.Password = "apikey", "env:ROSTER_TEST_MAIL_PASSWORD"
	_, err = mailOf(withPassword)
	x.NoError(err)

	for name, c := range map[string]struct {
		mail cmd.MailConfig
		says string
	}{
		"a password as itself":    {cmd.MailConfig{Addr: ok.Addr, From: ok.From, Username: "apikey", Password: "hunter2"}, "account.mail.password: a reference"},
		"a reference to nothing":  {cmd.MailConfig{Addr: ok.Addr, From: ok.From, Username: "apikey", Password: "env:NOTHING_SET_HERE"}, "account.mail.password"},
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
