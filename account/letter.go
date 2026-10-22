package account

import (
	"fmt"
	"os"

	"github.com/lesomnus/roster/mail"
)

// Letter is the message a link goes out in, for [Config.Mail]'s deliverer to
// send as the body: what it is for, the link on a line of its own, and what to
// do with a message nobody asked for. Plain text in this app's language, and
// nothing in it to follow but the link -- a message whose one link is the flow
// is one a person can act on without trusting the rest of it.
func Letter(subject, link string) string {
	return subject + "\n\n" +
		"Open this link to go on:\n\n" +
		link + "\n\n" +
		"If you did not ask for this, ignore this message. Nothing changes unless the link is opened.\n"
}

// unsent says a message did not go, as a log may carry it: the address cut to
// its domain, and what went wrong with any address or link in it cut out. A
// log is read by more people than the mailbox, and the link in particular is
// the flow itself.
func unsent(to string, err error) {
	fmt.Fprintf(os.Stderr, "account: mail to %s: %s\n", mail.Mask(to), mail.Redact(err.Error()))
}
