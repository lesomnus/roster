package scim

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rstr "github.com/lesomnus/roster/rstr"
)

// A person's numbers (#252): `phoneNumbers`, kept a row each (`Phone`).
//
// # Why a directory writes these and not an address
//
// A number is where somebody is reached and no way into their account --
// nothing here sends to one or finds anybody by one -- so the key this
// endpoint forwards may write it onto somebody who exists, and replace it
// when it changes. An address it says changed is kept and not followed
// (`HolderService.Tell`), for being the opposite.
//
// # One number a kind, as a directory says them
//
// A directory names a number by what it is -- `phoneNumbers[type eq
// "work"].value`, Entra's -- and says one for each. So a number said for a
// kind replaces whatever that kind held, and a kind it does not mention is
// left, for [changes]'s reason: the directory owns what it sends.

// phone is one number, and which of somebody's it is.
type phone struct {
	kind   string
	number string
}

// phoneKinds is SCIM's words for which number one is (RFC 7643 §4.1.2), which
// are what roster keeps (`core.PhoneKinds`). Named here because a consumer
// does not import the server.
var phoneKinds = []string{"work", "mobile", "fax", "home", "pager", "other"}

// maxPhone is the longest number roster keeps (`core.maxPhone`).
const maxPhone = 64

// kindOf is a `type` as roster keeps one: one of [phoneKinds], `other` for a
// word that is none of them, and nothing for nothing.
func kindOf(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case v == "":
		return ""
	case slices.Contains(phoneKinds, v):
		return v
	default:
		return "other"
	}
}

// dialled is a number as roster keeps one -- trimmed -- and whether it is one
// roster would keep at all. A directory's field holds what somebody typed
// into it, and one that is not a number is said in the log rather than
// refused: a refusal would be the directory failing this person every cycle
// for a field nothing reads.
func dialled(v string) (string, bool) {
	v = strings.TrimSpace(v)
	ok := v != "" && utf8.RuneCountInString(v) <= maxPhone &&
		strings.IndexFunc(v, unicode.IsDigit) >= 0 && strings.IndexFunc(v, unicode.IsControl) < 0

	return v, ok
}

// phonesIn is the numbers a list of `phoneNumbers` says: `[{value, type}]`.
func phonesIn(raw json.RawMessage) ([]phone, error) {
	var vs []struct {
		Value string `json:"value"`
		Type  string `json:"type"`
	}
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &vs); err != nil {
		return nil, badRequest("invalidValue", "phoneNumbers: a list of {value, type}")
	}

	out := make([]phone, 0, len(vs))
	for _, v := range vs {
		out = append(out, phone{kind: kindOf(v.Type), number: v.Value})
	}

	return out, nil
}

// phonePath is the kind a path names: `phoneNumbers[type eq "work"].value`,
// or the same with nothing after the bracket. Not ok for any other path.
func phonePath(path string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(path))
	if !strings.HasPrefix(lower, "phonenumbers[") {
		return "", false
	}
	shut := strings.LastIndex(path, "]")
	if shut < 0 {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(path[shut+1:])) {
	case "", ".value":
	default:
		return "", false
	}

	attr, value, err := splitEq(path[strings.Index(path, "[")+1 : shut])
	if err != nil || strings.ToLower(attr) != "type" {
		return "", false
	}

	return kindOf(value), true
}

// say is a number said for a kind; empty takes that kind's away.
func (c *changes) say(kind, number string) {
	if c.phones == nil {
		c.phones = map[string]string{}
	}
	c.phones[kind] = number
}

// phonesOf is somebody's numbers, in the order roster lists them.
//
// A key that may not list them answers none, rather than failing the read: a
// directory's key minted before numbers were kept here does not name the
// method, and everything else it reads should go on being read.
func (s *Server) phonesOf(ctx context.Context, holder []byte) ([]*rstr.Phone, error) {
	var out []*rstr.Phone

	after := ""
	for {
		vs, err := s.roster.Phone().List(ctx, rstr.PhoneListRequest_builder{
			Filters: []*rstr.PhoneFilter{rstr.PhoneFilter_builder{
				Holder: rstr.HolderRef_builder{Id: holder}.Build(),
			}.Build()},
			After: after,
		}.Build())
		if status.Code(err) == codes.PermissionDenied {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, vs.GetItems()...)

		after = vs.GetNext()
		if after == "" {
			return out, nil
		}
	}
}

// shown is a person as SCIM writes one, their numbers with them.
func (s *Server) shownUser(ctx context.Context, p *person) (map[string]any, error) {
	u := p.user()

	vs, err := s.phonesOf(ctx, p.holder.GetId())
	if err != nil {
		return nil, err
	}
	if len(vs) > 0 {
		ns := make([]map[string]any, 0, len(vs))
		for _, v := range vs {
			n := map[string]any{"value": v.GetNumber()}
			if k := v.GetKind(); k != "" {
				n["type"] = k
			}
			ns = append(ns, n)
		}
		u["phoneNumbers"] = ns
	}

	return u, nil
}

// dial writes what a write says of somebody's numbers.
//
// A number said for a kind is that kind's one number: the rows of that kind
// holding another go, and so does a row holding this number under another
// kind, since a person lists a number once. A whole list -- a User replaced --
// is every number they have, and what it does not name goes too.
//
// There is no verb that changes a row, so a change is the old row erased and
// a new one made; nothing reads a `Phone` by its id from one write to the
// next.
func (s *Server) dial(ctx context.Context, r *http.Request, holder []byte, c *changes) error {
	if c.phones == nil && c.allPhones == nil {
		return nil
	}

	want := map[string]string{}
	for k, v := range c.phones {
		want[k] = v
	}
	if c.allPhones != nil {
		for _, v := range *c.allPhones {
			want[v.kind] = v.number
		}
	}
	for k, v := range want {
		if v == "" {
			continue
		}
		n, ok := dialled(v)
		if !ok {
			s.c.Log.InfoContext(r.Context(), "scim: not a number, so not kept", "path", r.URL.Path, "kind", k)
			delete(want, k)

			continue
		}
		want[k] = n
	}

	have, err := s.phonesOf(ctx, holder)
	if err != nil {
		return err
	}
	kept := map[string]bool{}
	for _, v := range have {
		n, said := want[v.GetKind()]
		switch {
		case said && n == v.GetNumber() && !kept[v.GetKind()]:
			kept[v.GetKind()] = true

			continue
		case said:
			// This kind's, and not the number it is now.
		case c.allPhones != nil:
			// Every number was said, and this is none of them.
		case numberOf(want, v.GetNumber()):
			// Said for another kind now.
		default:
			continue
		}
		if _, err := s.roster.Phone().Erase(ctx, rstr.PhoneRef_builder{Id: v.GetId()}.Build()); err != nil &&
			status.Code(err) != codes.NotFound {
			return err
		}
	}

	for k, n := range want {
		if n == "" || kept[k] {
			continue
		}
		_, err := s.roster.Phone().Add(ctx, rstr.PhoneAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: holder}.Build(),
			Number: n,
			Kind:   k,
		}.Build())
		switch status.Code(err) {
		case codes.OK:
		case codes.PermissionDenied, codes.InvalidArgument:
			// A key that does not name the write, or a number roster will not
			// keep after all: said, and the rest of the write goes on.
			s.c.Log.InfoContext(r.Context(), "scim: a number was not kept", "path", r.URL.Path, "kind", k, "err", status.Convert(err).Message())
		default:
			return err
		}
	}

	return nil
}

func numberOf(want map[string]string, number string) bool {
	for _, n := range want {
		if n == number {
			return true
		}
	}

	return false
}
