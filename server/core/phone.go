package core

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lesomnus/payday/pderr"

	app "github.com/lesomnus/roster/rstr"
)

// corePhone is the layer over the generated `PhoneService`: a number is kept
// as it was said, and that is held to being something a person could dial.
//
// # What it does not ask
//
// Whose account the number is written into. A number is where somebody is
// reached, and nothing here sends to one or finds anybody by one, so it is no
// way in and `mayWriteAWayIn` is not asked (`phone.proto`). The wall, and the
// role that names the write, are the whole of who may.
type corePhone struct {
	Core
	app.PhoneServiceServer
}

func (s Core) Phone() app.PhoneServiceServer { return corePhone{s, s.Next().Phone()} }

// maxPhone is the longest a number may be: a country code, a number, an
// extension and the words a directory writes between them fit well inside it.
const maxPhone = 64

// PhoneKinds is which of somebody's numbers one may say it is: SCIM's words
// for it (RFC 7643 §4.1.2), which a directory sends and an LDAP client asks
// by. Empty is not said.
var PhoneKinds = []string{"work", "mobile", "fax", "home", "pager", "other"}

func (s corePhone) Add(ctx context.Context, req *app.PhoneAddRequest) (*app.Phone, error) {
	if err := isPhone(req.GetNumber()); err != nil {
		return nil, err
	}
	if k := req.GetKind(); k != "" && !slices.Contains(PhoneKinds, k) {
		return nil, pderr.Invalidf("kind", "%q: one of %s, or nothing", k, strings.Join(PhoneKinds, ", "))
	}

	return s.PhoneServiceServer.Add(ctx, req)
}

// isPhone refuses what nobody dials: nothing, something not trimmed, something
// with no digit in it, or a line of text.
//
// Not a form. A number is kept as it was said (`Phone.number`), and what is
// asked here is only that it is one -- trimmed, because a row is found again
// by the number it holds, and `" 555 0100"` and `"555 0100"` would be two.
func isPhone(v string) error {
	switch {
	case v == "":
		return pderr.Invalidf("number", "the number")
	case v != strings.TrimSpace(v):
		return pderr.Invalidf("number", "%q is not trimmed; %q", v, strings.TrimSpace(v))
	case utf8.RuneCountInString(v) > maxPhone:
		return pderr.Invalidf("number", "at most %d characters, and a number is a good deal shorter", maxPhone)
	case strings.IndexFunc(v, unicode.IsControl) >= 0:
		return pderr.Invalidf("number", "a number has no control character in it")
	case strings.IndexFunc(v, unicode.IsDigit) < 0:
		return pderr.Invalidf("number", "%q has no digit in it", v)
	}

	return nil
}
