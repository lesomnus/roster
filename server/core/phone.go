package core

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lesomnus/payday/pderr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// corePhone is the layer over the generated `PhoneService`: a number is kept
// as it was said, and that is held to being something a person could dial.
//
// # And whose account it is written into
//
// A number is somebody's as an address is: it says who they are to whoever
// reads it, and it is where a code is sent the day one is. So it is written
// onto somebody, and taken from them, by whoever may write a way into their
// account -- [Core.mayWriteAWayIn], [Core.mayTakeAWayIn] -- and by them
// (`phone.proto`). It was asked nothing, on the grounds that nothing sends to
// a number yet; the rule about a kind of row is better there before the first
// thing that depends on it than after.
//
// What it does not have yet is what the sending needs: when a number was
// checked, and one form to compare two by. Those arrive with it.
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
	if err := s.mayWriteAWayIn(ctx, "holder", req.GetHolder()); err != nil {
		return nil, err
	}

	return s.PhoneServiceServer.Add(ctx, req)
}

// Patch is what a number is, and not whose row it is: [coreEmail.Patch], of a
// number. No port serves it, and the servers' own writes are values too.
func (s corePhone) Patch(ctx context.Context, req *app.PhonePatchRequest) (*app.Phone, error) {
	if req.HasNumber() {
		if err := isPhone(req.GetNumber()); err != nil {
			return nil, err
		}
	}
	if k := req.GetKind(); k != "" && !slices.Contains(PhoneKinds, k) {
		return nil, pderr.Invalidf("kind", "%q: one of %s, or nothing", k, strings.Join(PhoneKinds, ", "))
	}

	return s.PhoneServiceServer.Patch(ctx, req)
}

// Erase takes a number away from somebody: [coreEmail.Erase], of a number.
func (s corePhone) Erase(ctx context.Context, req *app.PhoneRef) (*app.PhoneEraseResponse, error) {
	v, err := s.PhoneServiceServer.Get(ctx, app.PhoneGetRequest_builder{
		Ref:    req,
		Select: app.PhoneSelect_builder{Holder: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return s.PhoneServiceServer.Erase(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	if err := s.mayTakeAWayIn(ctx, "ref", v.GetHolder().GetId()); err != nil {
		return nil, err
	}

	return s.PhoneServiceServer.Erase(ctx, req)
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
