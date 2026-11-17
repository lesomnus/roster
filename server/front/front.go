// Package front answers the two questions a front door has before it knows
// anything.
//
// Which tenant serves the name a browser arrived at, and where the people at an
// address authenticate. Both are asked at the moment nobody has been resolved
// to anybody, which is what decides everything about the shape below.
//
// # It reads through no wall
//
// `vouch.Verify` does the same and says why: *working out who somebody is
// cannot require knowing who they are.* There is no frame to narrow by, because
// producing one is what the caller is trying to do.
//
// What keeps that honest is that neither RPC answers with a **row**. One
// identifier, or one provider name. There is no `Select` to get wrong, nothing
// that could be pointed at anything but a name somebody typed into DNS, and no
// shape for a field to be added to later without somebody noticing.
//
// # Neither half is public
//
// The person arriving has no credential -- that is what they are asking for --
// but they are not the caller. The caller is a front door holding a key, and
// roster is reached only by machines.
package front

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// Server answers what a front door asks first.
type Server struct {
	app.UnimplementedFrontServiceServer

	open app.Server
}

// New makes the service from the stack the wall was never installed on.
//
// One argument rather than two, unlike `vouch.New`: nothing here writes, so
// there is no second stack to get right.
func New(open app.Server) *Server { return &Server{open: open} }

// HeaderAt is the name a request says it arrived at, and the whole of what a
// front door fronting many tenants has to send.
//
// Here rather than in `server/keys`, where the handler that reads it lives, for
// one reason that is a rule rather than a preference: a **consumer** has to
// write this header, and a consumer may import no server package but this one
// (`scripts/test.sh`, *the consumers reach roster only over the wire*). The
// Login App is that consumer -- one `rk_`, narrowed per request by the name it
// declares (#36) -- and a header name written out in two repositories is a
// header spelled differently in one of them.
//
// This package is where it belongs anyway: `FrontService.WhoseHost` answers the
// same question from the other side, which is *whose is this name*.
//
// `keys.HeaderAt` is this constant, and `server/keys` is where what happens when
// a request carries it is written down.
//
// # A name, or a tenant
//
// The value is a name a tenant answers at -- a `Host` -- or [AtTenant] of a
// tenant itself, `@contoso` or `@<its identifier>`. The second is for a caller
// with no name to give: a directory resolves its tenant from a DN, a job from
// its own configuration. It loses nothing beside the first, because the name
// was only ever how the tenant was chosen; what decides whether the key may act
// there is the tenant's `Nomination` for it, either way (#76).
const HeaderAt = "roster-at"

// AtTenant is the value of [HeaderAt] that names a tenant directly, by alias or
// identifier, rather than by a name it answers at.
func AtTenant(ref string) string { return TenantMark + ref }

// TenantMark is what a [HeaderAt] value naming a tenant begins with. Not a
// character a hostname may hold, so the two cannot be mistaken for each other
// -- and `server/core` holds a `Host` to being a hostname, so no row is named
// with one.
const TenantMark = "@"

// Hostname is a host as this app stores and compares one: lowercased, with any
// port removed, an address literal unbracketed, and without the dot a fully
// qualified name ends in.
//
// Exported because both sides need it and neither may disagree. A row written
// as `Contoso.Example.com:8443` is a row that silently never matches, and the
// symptom is a sign-in page saying nobody is there.
//
// # The last dot is not part of the name
//
// `contoso.example.com.` is how DNS writes `contoso.example.com` when it means
// the whole of it, and a request that arrives at the first is looking for the
// row of the second. Left on, the dotted spelling was a name of its own: a
// second `Host` row, proved by the same TXT record as the first because the
// lookup trims the dot, and so one name two tenants each held (#277).
//
// One dot, after the port is off, because that is the side of the colon it is
// on. A second is an empty label rather than a spelling of anything, and it is
// left for `server/core` to refuse as one.
//
// # Why the splitting is not done by hand
//
// It was, and both halves of it were wrong for an address literal.
//
// The bracketed form kept its brackets, so `::1` and `[::1]` were two spellings
// of one name and each a fixed point of this function -- `Host.Add` accepted
// either, and an operator who wrote the bare one had a row no request could
// arrive as. The unbracketed form was cut at its last colon, because the guard
// meant to catch *too many colons* looked at the part **after** it, which the
// last colon guarantees has none: so it was true always, `::1` was stored as
// `:` and `fe80::1` as `fe80:`, and what the operator saw was `Host.Add`
// telling them so.
//
// [net.SplitHostPort] holds every one of those rules already, including that
// the brackets exist only to tell a colon in an address from the colon before a
// port -- which is why it takes them off with the port rather than leaving
// them. What it does not do is answer for a string with no port at all, which
// is most of what arrives here, so the error is read as "there was nothing to
// split" and the one case that leaves is finished off: a literal in brackets
// and no port.
func Hostname(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))

	if h, _, err := net.SplitHostPort(v); err == nil {
		v = h
	} else if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		v = v[1 : len(v)-1]
	}

	return strings.TrimSuffix(v, ".")
}

// Address is an address as this app stores and compares one: lowercased and
// trimmed.
//
// Exported for [Hostname]'s reason, and it was learned the same way. The
// lookup that turns an address into a person -- `vouch.byAddress` -- lowers and
// trims what it is handed, and the write did neither. So the unique index on
// `(tenant_id, address)`, which is the whole of what makes an address name one
// person, was comparing strings the lookup would never compare:
// `Someone@Contoso.example` and `someone@contoso.example` are two rows to the index
// and one address to everything that reads it.
//
// What that cost is not a duplicate. It is that the second row wins: an address
// written as a provider sent it cannot sign in at all -- the lookup lowers and
// the column never was -- and anybody who may add an address can write the
// lowered spelling of somebody else's onto their own row, at which point the
// victim's address resolves to them. Their password stops working and the
// attacker's starts, and a link mailed to the victim's mailbox signs in
// whoever clicks it as the attacker.
//
// The local part is lowered as well as the domain. RFC 5321 lets a mail server
// treat the local part as case-sensitive and none do; more to the point,
// `byAddress` decided this already, and a store whose write and whose lookup
// disagree about a name has no unique index at all.
//
// This is the spelling and not the form. What somebody typed at a sign-in is
// folded by it and looked for; what is written is also held to being an
// address at all ([NotAddress]).
func Address(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

const (
	// MaxAddress is the longest an address may be, in bytes: what RFC 5321
	// lets a path be, less the two brackets a path is written between.
	MaxAddress = 254

	// MaxLocal is the longest the part before the `@` may be, in bytes
	// (RFC 5321 section 4.5.3.1.1).
	MaxLocal = 64
)

// NotAddress says why a string is not an address as this app keeps one, and
// nothing where it is. It is asked of an address already in the spelling
// [Address] answers, so a capital is not its to refuse.
//
// # One mailbox is one spelling
//
// [Address] made two spellings of one address one row. It left every other
// way of writing a mailbox twice, and of writing something that is no mailbox
// at all: `plain@contoso.example`, `<plain@contoso.example>` and
// `plain@contoso.example.` were three rows, three names in the index, for the
// one place a message to any of them is delivered; a name and an address in
// one string, a line break in the middle, and two words with no `@` were rows
// too. A row is what a link is mailed to and what a token says somebody's
// address is, so what a mail sender would make of a string is not left to the
// mail sender.
//
// So an address is the plain form and no other:
//
//   - **Before the `@`**, RFC 5321's dot-string: letters, digits and the marks
//     an address is written with, in runs with single dots between. No quoted
//     string, no comment, no space, nothing a line could be broken at.
//   - **One `@`**, which follows from the first: it is not one of the marks.
//   - **After it**, a hostname by [NotHostname], that is a name and not an
//     address literal, and with no dot after it -- the dot [Hostname] takes
//     off a host is here a second spelling, and refused as one.
//   - **254 bytes** at most, and 64 before the `@`, which are the protocol's.
//
// # Nothing outside ASCII before the `@`, for now
//
// A mailbox may be written in any script (RFC 6531), and then the same letter
// has more than one encoding: `á` is one code point or two, and the two were
// two rows. Keeping them would mean choosing a normal form and a case folding
// for every script, and whatever is chosen is what every row is compared by
// from then on. Refusing them can be undone without touching a row; taking
// them cannot. The name after the `@` has one answer already, its A-label.
func NotAddress(v string) string {
	if len(v) > MaxAddress {
		return fmt.Sprintf("an address is %d bytes at most", MaxAddress)
	}

	at := strings.LastIndexByte(v, '@')
	if at < 0 {
		return "there is no @ in it"
	}
	local, name := v[:at], v[at+1:]

	switch {
	case local == "":
		return "there is nothing before the @"
	case len(local) > MaxLocal:
		return fmt.Sprintf("what is before the @ is %d bytes at most", MaxLocal)
	}
	for _, part := range strings.Split(local, ".") {
		if part == "" {
			return "a dot begins or ends what is before the @, or two stand together"
		}
		for _, c := range []byte(part) {
			switch {
			case c >= utf8.RuneSelf:
				return "what is before the @ holds a character outside ASCII, and an address is kept in one spelling, which those do not have yet"
			case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
				strings.IndexByte("!#$%&'*+-/=?^_`{|}~", c) >= 0:
			default:
				return "what is before the @ holds something a plain address is not written with: " +
					"no quotation, no comment, no second @, no space and no line break; the address alone, as in someone@example.com"
			}
		}
	}

	switch {
	case name == "":
		return "there is nothing after the @"
	case strings.HasSuffix(name, "."):
		return "the name after the @ ends in a dot, which is a second spelling of the same name; write it without"
	}
	if _, err := netip.ParseAddr(strings.Trim(name, "[]")); err == nil {
		return "what is after the @ is an address literal, and a mailbox is kept at a name"
	}
	if why := NotHostname(name); why != "" {
		return "the name after the @ is not a hostname: " + why
	}

	return ""
}

// NotHostname says why a name is not one DNS could hold, and nothing where it
// is.
//
// Labels between dots, each of letters, digits and hyphens, with a hyphen at
// neither end, 63 bytes at most and 253 for the whole. That is RFC 1123's
// hostname and nothing further:
//
//   - **No dot is asked for.** `localhost` is where a deployment on one machine
//     is reached, and `scripts/e2e.sh` is one.
//   - **An address is a name too.** A request arrives at `127.0.0.1` or
//     `[::1]` as readily as at a name, and [Hostname] stores the second
//     without its brackets. Nothing can be proved about one, so it is a row an
//     operator writes.
//   - **An A-label is an ordinary label.** `xn--bcher-kva.example` is what a
//     browser sends and what a zone file has. The same name in its own letters
//     is refused, saying so: it is a spelling no request arrives as and no
//     server can be asked about.
//
// An underscore is refused with everything else that is not one of the three.
// It begins a name that is data rather than a host -- `_dmarc`,
// `prove.Record` itself -- and a front door is not reached at one.
//
// Here, beside [Hostname], and not in `server/core`, where it was: the part of
// an address after its `@` is held to it ([NotAddress]), and what an address is
// has to be one function for whoever writes one and whoever checks one.
func NotHostname(v string) string {
	if at, err := netip.ParseAddr(v); err == nil && at.Zone() == "" {
		return ""
	}
	if strings.IndexFunc(v, func(r rune) bool { return r >= utf8.RuneSelf }) >= 0 {
		return "write its A-label, the \"xn--\" form a browser sends, because DNS holds no character outside ASCII"
	}
	if len(v) > 253 {
		return "a name is 253 bytes at most"
	}

	for _, label := range strings.Split(v, ".") {
		switch {
		case label == "":
			return "a label between its dots is empty"
		case len(label) > 63:
			return "a label is 63 bytes at most"
		case label[0] == '-' || label[len(label)-1] == '-':
			return "a label neither begins nor ends with a hyphen"
		}

		for _, c := range []byte(label) {
			switch {
			case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-':
			default:
				return "a label holds something other than a letter, a digit or a hyphen"
			}
		}
	}

	return ""
}

// Domain is the part of an address that is looked up, from either an address or
// a bare domain.
func Domain(v string) string {
	v = Address(v)
	if i := strings.LastIndex(v, "@"); i >= 0 {
		v = v[i+1:]
	}

	return v
}

// ClaimOid is the claim a connection names its people by where their subject
// is a GUID (`Connection.subject_claim`): Entra's `oid`.
const ClaimOid = "oid"

// Subject is a subject as this app stores and compares one, at a connection
// that names its people by `claim`: a GUID in small letters where the claim is
// `oid`, and what the provider said, untouched, anywhere else.
//
// Exported for [Address]'s reason. A GUID is the same one in capitals and in
// small letters, and nothing made it one spelling: `server/core` kept an `oid`
// as it was written, and every lookup compared it as written. So the same
// `oid` in capitals on one person and in small letters on another was two
// rows, each spelling a different person, and a row typed in the spelling a
// token does not use was one no sign-in found (#278).
//
// Small letters, because that is how Entra writes one in a token and how its
// directory says an `objectId`.
//
// # Only where the connection says so
//
// What makes a subject a GUID is its connection's word that it is one. A
// provider that names people by `sub` may hand out something GUID-shaped whose
// case it means -- OpenID Connect says a `sub` is compared exactly -- and so
// may a provider no connection fronts. Theirs are kept as they said them.
//
// And only a GUID. A connection that came to name `oid` still has rows keyed by
// the `sub` people signed in by before, which a sign-in finds once to move
// (`IdentityService.Resubject`), and that is base64 whose case is the whole of
// it.
func Subject(claim, v string) string {
	if claim == ClaimOid && GUID(v) {
		return strings.ToLower(v)
	}

	return v
}

// GUID is whether a string has the shape `oid` takes: 8-4-4-4-12 hexadecimal
// digits, in either case.
func GUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, r := range v {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f' || 'A' <= r && r <= 'F') {
				return false
			}
		}
	}

	return true
}

// Unstable is why a string is nobody's subject at any provider, and empty for
// one that may be: what `IdentityService/Add` refuses a subject for on its
// form, before it asks anything of the row.
//
// `server/core/identity.go` says why each is refused, and refuses in these
// words. It is here, exported, for [Address]'s reason from the other side of a
// write. Under `enrolling` a front door makes somebody and then links their
// account, in two calls (`arrives.Enrolling`), so a subject the link is
// refused for was a person made and left with no way in, and another at every
// try (#279). What can be known of a subject before anybody is made is asked
// before anybody is made -- of the function the write asks, so that the two
// cannot come to differ. The other half of a subject's form is its
// connection's claim, which [GUID] and [ClaimOid] say.
func Unstable(v string) string {
	switch {
	case v == "":
		return "must not be empty"
	case v != strings.TrimSpace(v):
		return "has a space before or after it; a subject is compared as it is written, so write it as the provider says it"
	case strings.Contains(v, "@"):
		return "looks like an address; it must be the identifier the provider treats as immutable, " +
			"because an address is reassigned and whoever gets it next would be served as this person"
	}

	return ""
}

// WhoseHost answers which tenant serves a name.
func (s *Server) WhoseHost(ctx context.Context, req *app.FrontWhoseHostRequest) (*app.FrontWhoseHostResponse, error) {
	name := Hostname(req.GetHost())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "host: which name did they arrive at")
	}

	v, err := s.open.Host().Get(ctx, app.HostGetRequest_builder{
		Ref: app.HostRef_builder{Name: &name}.Build(),
		Select: app.HostSelect_builder{
			Tenant: app.TenantSelect_builder{DateClosed: ptr(true)}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		// `NotFound` unchanged, which is the answer a front door needs: one
		// that carried on with no tenant would look somebody up in whichever
		// one it happened to reach.
		return nil, err
	}
	if v.GetTenant().GetDateClosed() != nil {
		// Said rather than hidden. Its people arrive at this name with
		// bookmarks and mail, and *this organisation is closed* is the answer
		// they need -- not a sign-in form that refuses every password, which
		// reads as their own mistake. Nothing past this name answers anyway.
		return nil, status.Error(codes.FailedPrecondition, "this tenant is closed")
	}

	k, err := pdid.From(v.GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	return app.FrontWhoseHostResponse_builder{Tenant: k.Bytes()}.Build(), nil
}

// WhereFrom answers where the people at an address authenticate.
//
// A domain nobody has said anything about answers with an empty provider rather
// than `NotFound`, and that is the opposite of the choice above on purpose:
// there is nothing here a caller could carry on wrongly with, and a front door
// that learns nothing offers whatever it offers everybody. It is also what
// stops this from answering *does this domain exist here*.
func (s *Server) WhereFrom(ctx context.Context, req *app.FrontWhereFromRequest) (*app.FrontWhereFromResponse, error) {
	name := Domain(req.GetAddress())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "address: an address, or the domain from one")
	}

	tenant, err := pdid.From(req.GetTenant())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "tenant: %s", err)
	}

	// A caller of a tenant asks about its own. The row is read through no
	// wall, since a front door asks this before anybody is resolved to a
	// person, and the tenant is the request's to say -- so a caller inside one
	// tenant, whose role named this method, was answered how another tenant's
	// people sign in: the row `MailDomain.Get` refuses the same caller (#277).
	// Answered as a domain nobody has said anything about is, and for its
	// reason. A caller held by no tenant is a deployment's key calling as
	// itself, whose list is what it may ask.
	if f, ok := frame.From(ctx); ok && f.Tenant != pdid.Nil && f.Tenant != tenant {
		return app.FrontWhereFromResponse_builder{}.Build(), nil
	}

	v, err := s.open.MailDomain().Get(ctx, app.MailDomainGetRequest_builder{
		Ref: app.MailDomainRef_builder{
			At: app.MailDomainRefByAt_builder{
				Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
				Name:   &name,
			}.Build(),
		}.Build(),
		Select: app.MailDomainSelect_builder{Provider: ptr(true)}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return app.FrontWhereFromResponse_builder{}.Build(), nil
		}

		return nil, err
	}

	return app.FrontWhereFromResponse_builder{Provider: v.GetProvider()}.Build(), nil
}

func ptr[T any](v T) *T { return &v }
