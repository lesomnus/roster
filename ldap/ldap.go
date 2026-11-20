// Package ldap is a directory over roster: what `roster ldap serve` is.
//
// It is a consumer, exactly as `account` is -- its own process, one tenant
// key per tenant it fronts, reaching roster over the wire and never past
// it -- and what it does is translate. A bind is `Me.Get` bearing the app
// password the client presented, or `Vouch.Verify` with the person's own; a
// search is `Holder.Get`, `Holder.Search`, `Holder.List` and the lists beside
// them, read with this process's key and shaped into `inetOrgPerson` entries
// under one suffix per tenant. It holds no data and keeps no cache: every
// search is a read of roster, so somebody disabled a moment ago is not in the
// next answer. `docs/ldap.md` is the plan this is written to, and the reasons.
//
// # The bind decides whether, not what
//
// A successful bind lets the connection search. It does not choose what the
// search sees: every read is made with the tenant key this process holds, so
// the directory is one view for everybody who binds, as a directory is. The
// bound person's app password is a key whose methods are `Me.Get` and nothing
// else, and could not read the tree if it were asked to.
//
// # And it decides for as long as whoever made it still stands
//
// A connection is whoever bound it, and roster is asked about them again each
// [Recheck] the connection searches in: somebody suspended or erased since, or
// whose app password was erased, is somebody whose connection is nobody's from
// the next asking on ([Directory.boundTenant]). What is kept of a bind to ask
// with is a [binding], which says why it is what it is.
//
// # Why a password bind cannot walk around a second factor
//
// Not by a rule here. `Vouch.Verify` answers `ok` only when the sign-in is
// finished; for somebody with a second factor it answers `ok: false` with a
// continuation this process has no second form to spend. So the refusal is
// roster's, the same one that keeps a product app that has never heard of
// second factors failing closed, and this package only reports it.
package ldap

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/ldap/wire"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// Mode is what a bind's password may be.
type Mode int

const (
	// BindKey accepts an app password only: an `rt_` the person minted for
	// this client. The default, and the one that needs no `Verify` on the
	// process's key.
	BindKey Mode = iota
	// BindPassword accepts the person's own password only.
	BindPassword
	// BindEither accepts both.
	BindEither
)

// ParseMode reads `key`, `password`, `either`.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "", "key":
		return BindKey, nil
	case "password":
		return BindPassword, nil
	case "either":
		return BindEither, nil
	default:
		return 0, fmt.Errorf("%q is not one of key, password, either", s)
	}
}

func (m Mode) keys() bool      { return m == BindKey || m == BindEither }
func (m Mode) passwords() bool { return m == BindPassword || m == BindEither }

// Config is what a deployment says.
type Config struct {
	// Roster is where the data plane speaks gRPC.
	Roster string

	// Insecure dials it without TLS. A development setting.
	Insecure bool

	// Keys is one tenant key per tenant this directory fronts, by the
	// tenant's alias. Minted for a holder in that tenant whose role names
	// what a directory reads (`docs/ldap.md` § The key this process holds).
	Keys map[string]string

	// Key is one deployment key instead: the directory is answered in each
	// tenant as the holder that tenant nominated for it, naming the tenant on
	// every call (`roster-at: @<tenant>`, since a directory has a DN where a
	// front door has a host). The tenants are the key's own nominations, read
	// at start. One of Key and Keys (#76).
	Key string

	// Bases renames a tenant's suffix, by alias: `dc=contoso,dc=example`
	// where the default is `o=contoso`. A suffix is one tenant's, and two
	// tenants left with the same one are refused.
	Bases map[string]string

	// Bind is what a password may be. Zero is [BindKey].
	Bind Mode

	// PageSize is the most people one roster read fetches, and so the most
	// one page of a paged search carries. Zero is [DefaultPageSize].
	PageSize int

	// StartTLS is whether the listener this directory is served on offers
	// StartTLS, which is what the root DSE then says. The certificate is the
	// wire's (`wire.Server.TLS`) and never comes here, so whoever builds the
	// two says it to both.
	StartTLS bool

	Log *slog.Logger

	// Now is the clock a connection's last asking is aged by ([Recheck]). Nil
	// is `time.Now`. A test's, and nothing a deployment says.
	Now func() time.Time
}

// Recheck is how long a bound connection is served before roster is asked
// again whether whoever bound it still stands: at most this long after
// somebody is stopped, a connection they had bound goes on reading.
//
// Five minutes, which is what roster's own streams are held to where a
// deployment says nothing (`stream.recheck`, `cmd.AskedAgain`), and for that
// rule's reason. A connection is to this process what a stream left open is to
// roster -- a credential shown once, and then served until somebody hangs up
// -- so it is one promise, *no credential of theirs works, and a stream they
// left open ends*, and one number an operator has already read. Asked on a
// clock rather than at every search, because a client that keeps a connection
// searches on it at each of its own sign-ins, and a call of roster for each
// would be paid by every deployment to shorten a wait that only somebody
// being stopped is in.
//
// A constant. This process is not told `stream.recheck` -- it is as often
// another process as a block of the server's -- and a key of its own would be
// a second number for a deployment to keep in step with the first.
const Recheck = 5 * time.Minute

// DefaultPageSize is `server/core`'s `SearchPageLimit`: asking for more
// gets this anyway.
const DefaultPageSize = 100

// KeyPrefix is what an app password starts with: an `rt_`, the customer's
// kind of key.
const KeyPrefix = "rt_"

// keyShaped is whether what a bind sent as its password is an app password,
// by its whole shape: the prefix, and after it what roster mints -- thirty-two
// bytes in unpadded URL-safe base64, forty-three characters
// (`server/keys.Mint`, which this package may not import). Anything else is a
// password.
//
// # Why the prefix is not enough, and why the shape decides alone
//
// The prefix decided, and a person's password may begin with anything. One
// that begins `rt_` never bound: where only passwords bind it was refused as
// an app password, and where both do it was sent to roster as a key and never
// tried as a password (#281).
//
// What has the whole shape is tried as a key and as nothing else, where it
// might have been tried as a password after roster said it is no key. That
// would send every app password its owner has revoked to `Vouch.Verify` as
// their wrong password, and a client that goes on trying one would lock its
// owner out of the password they sign in with everywhere else, ten tries at a
// time. A person does not type forty-three characters of base64, and one whose
// password is exactly that binds with an app password instead.
//
// Every test that binds with a key roster minted holds this to what roster
// mints: a key of another length would bind nowhere.
func keyShaped(s string) bool {
	rest, ok := strings.CutPrefix(s, KeyPrefix)
	if !ok || len(rest) != base64.RawURLEncoding.EncodedLen(32) {
		return false
	}

	return !strings.ContainsFunc(rest, func(r rune) bool {
		return !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	})
}

// tenant is one operator this directory fronts.
type tenant struct {
	alias string
	id    pdid.Id
	key   string
	base  dn

	// at is what every call about this tenant declares with `roster-at`, when
	// the key is a deployment key narrowed to whoever the tenant nominated.
	at string
}

// Directory is the tree, over roster.
type Directory struct {
	c       Config
	conn    *grpc.ClientConn
	roster  rstr.Client
	me      rstr.MeServiceClient
	vouch   rstr.VouchServiceClient
	tenants []*tenant // longest suffix first, so `o=a` never claims `ou=x,o=a`'s look-alike
}

// New dials roster and learns each tenant's identifier, which is also what
// proves each key sees what it was minted to.
func New(ctx context.Context, c Config) (*Directory, error) {
	switch {
	case c.Roster == "":
		return nil, errors.New("ldap: Roster: where the data plane speaks gRPC")
	case len(c.Keys) == 0 && c.Key == "":
		return nil, errors.New("ldap: Keys or Key: one tenant key per operator this directory fronts, or one deployment key; none is nobody to front")
	case len(c.Keys) > 0 && c.Key != "":
		return nil, errors.New("ldap: Keys and Key: one way to front tenants, not two")
	}
	if c.PageSize <= 0 || c.PageSize > DefaultPageSize {
		c.PageSize = DefaultPageSize
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}

	creds := credentials.NewTLS(nil)
	if c.Insecure {
		creds = insecure.NewCredentials()
	}
	opts := append(auth.Inject(auth.ProviderFunc(func(ctx context.Context) context.Context {
		if k, ok := keyOf(ctx); ok {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+k.key)
			if k.at != "" {
				ctx = metadata.AppendToOutgoingContext(ctx, front.HeaderAt, k.at)
			}
		}

		return ctx
	})), grpc.WithTransportCredentials(creds))

	conn, err := grpc.NewClient(c.Roster, opts...)
	if err != nil {
		return nil, fmt.Errorf("ldap: %s: %w", c.Roster, err)
	}

	d := &Directory{
		c:      c,
		conn:   conn,
		roster: rstr.NewClient(conn),
		me:     rstr.NewMeServiceClient(conn),
		vouch:  rstr.NewVouchServiceClient(conn),
	}

	fronts, err := d.fronted(ctx)
	if err != nil {
		conn.Close()

		return nil, err
	}
	for alias := range c.Bases {
		if _, ok := fronts[alias]; !ok {
			// Refused, as everything this is told and cannot do is. Which
			// means a suffix pinned for a tenant ties this directory's start
			// to that tenant still fronting it -- `docs/ldap.md` says so where
			// `--base` is described.
			conn.Close()

			return nil, fmt.Errorf("ldap: Bases: a suffix for %q, and no key or nomination for it", alias)
		}
	}

	aliases := make([]string, 0, len(fronts))
	for alias := range fronts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		t := fronts[alias]

		base := dn{{attr: "o", value: alias}}
		if s, ok := c.Bases[alias]; ok {
			base, err = parseDN(s)
			if err != nil || len(base) == 0 {
				conn.Close()

				return nil, fmt.Errorf("ldap: Bases[%s]: %q is not a DN", alias, s)
			}
		}
		// A suffix is one tenant's. Two given the same one both started, and
		// a name under it was always read as the first's: nobody of the other
		// could bind, and nothing said so (#281).
		for _, other := range d.tenants {
			if len(other.base) == len(base) && base.hasSuffix(other.base) {
				conn.Close()

				return nil, fmt.Errorf("ldap: Bases: %q and %q are both given the suffix %s, and a suffix is one tenant's: "+
					"a name under it is read as one of them and nobody of the other binds", other.alias, alias, other.base)
			}
		}
		t.base = base
		d.tenants = append(d.tenants, t)
	}
	sort.SliceStable(d.tenants, func(i, j int) bool { return len(d.tenants[i].base) > len(d.tenants[j].base) })

	return d, nil
}

// Close hangs up.
func (d *Directory) Close() error { return d.conn.Close() }

// Handler is the directory as the wire sees it.
func (d *Directory) Handler() wire.Handler { return d }

// NamingContexts is every suffix served, for the root DSE and for a test.
func (d *Directory) NamingContexts() []string {
	out := make([]string, len(d.tenants))
	for i, t := range d.tenants {
		out[i] = t.base.String()
	}
	sort.Strings(out)

	return out
}

// tenantOf finds which suffix a name is under, and the part above it.
func (d *Directory) tenantOf(name dn) (*tenant, dn, bool) {
	for _, t := range d.tenants {
		if name.hasSuffix(t.base) {
			return t, name[:len(name)-len(t.base)], true
		}
	}

	return nil, nil, false
}

// Bind is the wire's: the bind is answered, and one line says how it ended.
//
// # One line a bind
//
// Nothing logged one. Roster's trail has a line for a wrong password and none
// for where it was typed -- the caller it names is this process's own holder,
// from this process's address -- so somebody locked out through this port
// could not be told which client did it (#281).
//
// The line holds the client's address as the socket has it; the tenant by its
// identifier, and only where the name was under a suffix this process serves;
// which kind of secret was sent; how it ended, in one of the few words below;
// and roster's code where roster is why. Each is this process's own or
// roster's, and none is the client's: not the name, which is what a stranger
// typed and would be a line of the log with a line break in it; not the
// password, not the key. And one level, whatever was sent and however it
// ended, so that whoever can make lines cannot make warnings.
func (d *Directory) Bind(ctx context.Context, c *wire.Conn, req wire.BindRequest) wire.Result {
	res, how := d.bind(ctx, c, req)

	attrs := []slog.Attr{slog.String("from", c.RemoteAddr().String())}
	if how.tenant != nil {
		attrs = append(attrs, slog.String("tenant", how.tenant.id.String()))
	}
	attrs = append(attrs, slog.String("kind", how.kind), slog.String("ended", how.ended))
	if how.code != codes.OK {
		attrs = append(attrs, slog.String("code", how.code.String()))
	}
	d.c.Log.LogAttrs(ctx, slog.LevelInfo, "ldap: bind", attrs...)

	return res
}

// ended is how a bind ended, for the log: every value a word of this
// process's.
type ended struct {
	// tenant is the tenant the name is in, and nil for a name under no suffix
	// this process serves.
	tenant *tenant

	// kind is which kind of secret was sent.
	kind string

	ended string

	// code is roster's, where roster could not say or refused this process.
	code codes.Code
}

// The kinds of secret, and the ways a bind ends.
const (
	kindKey      = "key"
	kindPassword = "password"
	kindNone     = "none"

	endedBound = "bound"

	// endedRefused is roster saying no, or the name not being whose the key
	// is: a wrong password, nobody of that name, somebody suspended or
	// erased. Roster does not say which, and neither does this.
	endedRefused = "refused"

	// endedUntried is a bind roster was not asked about: no password, the
	// kind this directory does not bind, or a password for a name under no
	// suffix. It counted toward nobody's lockout.
	endedUntried = "not tried"

	endedFactor = "second factor"
	endedLocked = "locked"
	endedAway   = "roster away"
	endedBusy   = "roster busy"

	// endedKeyRefused is roster refusing this process's own key: the tenant
	// closed, the holder it nominated suspended, or a role that does not name
	// `Vouch.Verify` where passwords bind. An operator's to mend, and said
	// here rather than as a warning for each attempt by anybody.
	endedKeyRefused = "the directory's key was refused"
)

// bind answers a bind, and says how it ended.
func (d *Directory) bind(ctx context.Context, c *wire.Conn, req wire.BindRequest) (wire.Result, ended) {
	// One answer for everything that is not a bind that works, so that the
	// shape of the tree and who is in it are learned by searching -- which
	// needs a bind -- and never by trying names.
	refused := wire.Refuse(wire.InvalidCredentials, "")

	// Which tenant the name is in, and who of it. A name somebody typed, so
	// it is folded as roster folds one (`front.Alias`): an app password was
	// refused for `uid=LEE` where a password bound.
	t, alias, named := d.bindDN(req.DN)
	alias = front.Alias(alias)
	how := ended{tenant: t, kind: kindNone, ended: endedUntried}

	if len(req.Password) == 0 {
		return refused, how
	}

	// # The sentence about the mode is about what was sent, and never the name
	//
	// A directory that binds one kind says so to a client that sent the
	// other, for the person reading that client's log. It was said after the
	// name had been found under a suffix this process serves, and so for no
	// other name: an unbound stranger learned which tenants there are by
	// which refusals carried it (#281). It is decided here, by the
	// deployment's mode and the shape of the secret and with nothing of the
	// name, so every name there could be is answered with it. What it tells
	// is which kind this directory binds, which is what it is for, and
	// nothing of who or what is here.
	pw := string(req.Password)
	asKey := keyShaped(pw)
	switch {
	case asKey && !d.c.Bind.keys():
		refused.Message = "this directory binds with passwords; an app password was given"
	case !asKey && !d.c.Bind.passwords():
		refused.Message = "this directory binds with app passwords: a key the person mints for this client"
	}

	if asKey {
		how.kind = kindKey
		if !d.c.Bind.keys() {
			return refused, how
		}
		// Bearing the key the client presented, not this process's: whose
		// it is, is the whole question, and roster answers it. Asked
		// whatever the name, since the key says whose it is and the name is
		// only held to that: a guess under a suffix this process serves
		// costs what one under any other costs.
		v, err := d.me.Get(withKey(ctx, pw), rstr.MeGetRequest_builder{}.Build())
		switch {
		case err != nil && unsaid(ctx, err):
			return away(err, how)
		case err != nil, !named, v.GetAlias() != alias, !bytes.Equal(v.GetTenant(), t.id.Bytes()):
			how.ended = endedRefused

			return refused, how
		}
		c.Keep(d.nameOf(t, v.GetAlias()), &binding{
			tenant: t, holder: v.GetId(), key: pw,
			from: c.RemoteAddr().String(), asked: d.c.Now(),
		})
		how.ended = endedBound

		return wire.Ok, how
	}

	how.kind = kindPassword
	if !d.c.Bind.passwords() || !named {
		return refused, how
	}
	// Read before roster is asked, so that whatever ended their sign-ins
	// while it answered is after it.
	began := d.c.Now()
	// As the directory's holder in the tenant, and naming the person alone:
	// which tenant is the key's to say, by its identifier, as it is for every
	// read. The tenant's alias was sent beside the name, as it was read at
	// start, and a tenant renamed since was one nobody could bind in by
	// password under either name (#281).
	v, err := d.vouch.Verify(t.on(ctx), rstr.VouchVerifyRequest_builder{
		Who:    rstr.VouchWho_builder{Alias: alias}.Build(),
		Secret: req.Password,
	}.Build())
	how.ended = endedRefused
	switch code := status.Code(err); {
	case err != nil && unsaid(ctx, err):
		return away(err, how)
	case code == codes.PermissionDenied, code == codes.Unauthenticated:
		// This process's own key, refused. Not a wrong password, and
		// answered as one all the same: a tenant that closed lets nobody in
		// and is no different to a client from a suffix that is not served,
		// which is the one answer a stranger is given. What is so is on the
		// line. It was a warning that the key cannot call `Vouch.Verify`,
		// once for each attempt by anybody, and of a closed tenant that is
		// not so.
		how.ended, how.code = endedKeyRefused, code

		return refused, how
	case err != nil:
		// Roster refusing the request itself: a name it will not read as one.
		return refused, how
	case v.GetOk():
		if !bytes.Equal(v.GetTenant(), t.id.Bytes()) {
			return refused, how
		}
		// A row's alias is in small letters or it was not written, so the
		// name as it was folded is the name the tree has.
		c.Keep(d.nameOf(t, alias), &binding{
			tenant: t, holder: v.GetHolder(), began: began,
			from: c.RemoteAddr().String(), asked: d.c.Now(),
		})
		how.ended = endedBound

		return wire.Ok, how
	case v.GetContinuation() != "":
		// The password was right and the sign-in is not finished: a second
		// factor is enrolled, and there is no form here to ask for it. The
		// diagnostic is for the person reading their client's log, and it
		// says nothing a wrong password would not have -- the refusal code is
		// the same, and only somebody holding the right password reaches it.
		how.ended = endedFactor

		return wire.Refuse(wire.InvalidCredentials, "a second factor is enrolled; use an app password for this client"), how
	case v.GetDateLocked() != nil:
		// Said to the log, where whoever is asked which client locked
		// somebody out reads it, and not to the client.
		how.ended = endedLocked

		return refused, how
	default:
		return refused, how
	}
}

// away answers a bind roster could not say anything about, and says so of how
// it ended.
//
// It was `invalidCredentials`, as every error of roster's was: with roster
// unreachable, held to a rate or failing, somebody's right password was
// answered as a wrong one, and nothing was logged (#281). A client shows that
// as a wrong password, and a person changes one that was never wrong.
func away(err error, how ended) (wire.Result, ended) {
	how.code = status.Code(err)
	if how.code == codes.ResourceExhausted {
		how.ended = endedBusy

		return wire.Refuse(resultBusy, sayBusy), how
	}
	how.ended = endedAway

	return wire.Refuse(resultUnavailable, sayAway), how
}

// The two result codes of RFC 4511 § 4.1.9 for a server that cannot answer
// just now, and what this process says with each: a sentence of its own, since
// the one gRPC writes names where roster is dialed.
const (
	resultBusy        = 51
	resultUnavailable = 52

	sayBusy = "roster is busy; try again in a while"
	sayAway = "the directory cannot ask roster just now; try again in a while"
)

// binding is what a bind made of a connection: who bound it, in which tenant,
// which kind of bind it was, and when roster was last asked about them. The
// wire keeps it with the connection ([wire.Conn.Keep]) and it goes at the next
// bind.
//
// # What is kept to ask with
//
// An app password is asked about by bearing it, so it is kept, in memory, for
// as long as the connection is bound with it -- as the client that sent it
// keeps it, and for no longer than the client stays. Less would not do. What
// roster could be asked instead is whether a key, named by its identifier, is
// still its holder's; `Me.Get` lists every key of the caller and does not say
// which bore the call, and no read of a key by its identifier is one the
// directory's role names. So asking with less is a field roster would have to
// answer first, and a method every installed directory would have to be given.
//
// A password is not kept and is not asked again: it would count toward a
// lockout each time, and its being right once is not what has to go on being
// true. Who it proved is kept, and when.
type binding struct {
	tenant *tenant
	holder []byte

	// key is the app password the connection bound with, and empty for a
	// bind by password.
	key string

	// began is when a bind by password was made, by this process's clock
	// and read before roster was asked: what ended their sign-ins after it
	// ends the connection, which is how `standing` holds a session to
	// `Holder.date_invalidated` too. The moment compared with it is roster's,
	// so where this is a process of its own they are two machines' clocks,
	// and one that runs ahead of roster's misses a sign-out made within
	// their difference after the bind.
	began time.Time

	// from is the client's address as the bind's line said it, for the line
	// that says the connection ended.
	from string

	// mu holds one asking at a time, so that searches arriving together when
	// the clock runs out are one call of roster; the rest wait for its answer
	// and are served by it.
	mu      sync.Mutex
	asked   time.Time
	stopped bool
}

// errStopped is whoever bound a connection being somebody who would not be
// let bind it now.
var errStopped = errors.New("whoever bound this connection may not any more")

// boundTenant is whose directory a connection is reading: the tenant of
// whoever bound it, for as long as they still stand, and nobody's before a
// bind or after they were stopped.
//
// # A bind is asked about again
//
// It was checked once. The connection kept the name it bound as and every
// search asked which tenant that name is in, so somebody suspended or erased,
// or whose app password was erased, was refused at a new bind and went on
// reading everybody on the connection they had bound before, for as long as it
// stayed up (#281). Roster's own streams had the same defect and the same
// mending (`cmd.AskedAgain`): a credential shown once is asked about on a
// clock.
//
// So a search on a connection roster has not been asked about for [Recheck]
// asks before it answers, and what it is told decides three ways:
//
//   - they still stand, and the search is served, as every search is until
//     the clock runs out again;
//   - they do not, and the connection is nobody's from then on
//     ([wire.Conn.Drop]): this search and every later one is answered as one
//     on a connection that never bound is, and WhoAmI says nobody. A client
//     binds again, and that bind is answered as any bind is;
//   - roster could not say -- it is away, or busy, or the search ended first
//     -- and that is the answer ([refusal]). It is not somebody being
//     stopped: the connection stays theirs, and the next search asks again.
//
// The result is the search's own answer where it is not success.
func (d *Directory) boundTenant(ctx context.Context, c *wire.Conn) (*tenant, bool, wire.Result) {
	b, ok := c.Kept().(*binding)
	if !ok {
		return nil, false, wire.Ok
	}

	switch err := d.stands(ctx, b); {
	case err == nil:
		return b.tenant, true, wire.Ok
	case errors.Is(err, errStopped):
		c.Drop(b)

		return nil, false, wire.Ok
	default:
		return nil, false, refusal(err)
	}
}

// stands asks roster whether whoever made a binding would still be let make
// it, where it has not been asked for [Recheck]: nil for yes, [errStopped] for
// no, and roster's own error where it could not say.
func (d *Directory) stands(ctx context.Context, b *binding) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// A no is not asked about twice: a search already running beside the one
	// that heard it is answered the same.
	if b.stopped {
		return errStopped
	}
	if d.c.Now().Sub(b.asked) < Recheck {
		return nil
	}

	err := d.ask(ctx, b)
	switch {
	case err == nil:
		b.asked = d.c.Now()
	case errors.Is(err, errStopped):
		b.stopped = true

		// Once, in the form the bind's own line has ([Directory.Bind]): what
		// a client then sees is *bind first*, and this is where an operator
		// reads why.
		kind := kindPassword
		if b.key != "" {
			kind = kindKey
		}
		d.c.Log.LogAttrs(ctx, slog.LevelInfo, "ldap: a bound connection is nobody's now",
			slog.String("from", b.from), slog.String("tenant", b.tenant.id.String()), slog.String("kind", kind))
	}

	return err
}

// ask is the one call of roster a binding is asked about with.
func (d *Directory) ask(ctx context.Context, b *binding) error {
	if b.key != "" {
		// What the bind asked, asked again: the app password is borne, and
		// roster answers whose it is. Refused, it is a key that was erased or
		// ran out, a holder suspended or erased, or a tenant that closed --
		// each of which refuses the bind itself.
		v, err := d.me.Get(withKey(ctx, b.key), rstr.MeGetRequest_builder{}.Build())
		switch {
		case err == nil:
			if !bytes.Equal(v.GetId(), b.holder) || !bytes.Equal(v.GetTenant(), b.tenant.id.Bytes()) {
				return errStopped
			}

			return nil
		case unsaid(ctx, err):
			return err
		default:
			return errStopped
		}
	}

	// The person, by the directory's own key, since their password was not
	// kept. The four facts and nothing they are called, which is what
	// `standing` reads of an account and for its reason: this is asked about
	// every bound connection, and a read that carried a name would be a row
	// of roster's access record each time.
	v, err := d.roster.Holder().Get(b.tenant.on(ctx), rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Id: b.holder}.Build(),
		Select: rstr.HolderSelect_builder{
			Tenant:          rstr.TenantSelect_builder{DateClosed: proto.Bool(true)}.Build(),
			DateInvalidated: proto.Bool(true),
			DateDisabled:    proto.Bool(true),
			DateErased:      proto.Bool(true),
		}.Build(),
	}.Build())
	switch {
	case status.Code(err) == codes.NotFound:
		// Erased, or forgotten since.
		return errStopped
	case err != nil:
		// Roster away, or the directory's own key refused -- which is said
		// as it is of every read ([refusal]), and is not an answer about
		// whoever bound.
		return err
	case v.GetDateDisabled() != nil, v.GetDateErased() != nil, v.GetTenant().GetDateClosed() != nil:
		return errStopped
	case v.GetDateInvalidated() != nil && v.GetDateInvalidated().AsTime().After(b.began):
		// Signed out everywhere since, or their password replaced: what ends
		// every sign-in of theirs ends this one.
		return errStopped
	default:
		return nil
	}
}

// unsaid is an error of roster's that says nothing about whoever a call was
// made as: the call ended first, or roster is in trouble. Everything else it
// answers a borne credential with is a refusal of it. `cmd.AskedAgain` draws
// the same line for a stream, and three codes are added to its list here
// because this is asked over a wire: a failure roster's handler did not
// explain arrives as `Unknown`, where inside roster it is an error to read.
//
// Erring this way reads nothing. A connection that could not be asked about
// is not served until it can be, and a bind that could not be checked does
// not bind.
func unsaid(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Internal, codes.ResourceExhausted,
		codes.Unknown, codes.DataLoss, codes.Aborted:
		return true
	default:
		return false
	}
}

// nameOf is the name the tree has for somebody of a tenant, which is what a
// connection they bound is bound as and what WhoAmI answers: the name of their
// entry, as a search returns it. It was the client's own spelling of what it
// sent -- `UID=lee, OU=People, O=Contoso` -- which names no entry.
func (d *Directory) nameOf(t *tenant, alias string) string {
	return d.personDN(t, rstr.Holder_builder{Alias: alias}.Build()).String()
}

// bindDN reads `uid=<alias>,ou=people,<suffix>`: the one shape a bind names.
func (d *Directory) bindDN(s string) (*tenant, string, bool) {
	name, err := parseDN(s)
	if err != nil {
		return nil, "", false
	}
	t, rel, ok := d.tenantOf(name)
	if !ok || len(rel) != 2 || rel[0].attr != "uid" || rel[0].value == "" || rel[1].attr != "ou" || !strings.EqualFold(rel[1].value, ouPeople) {
		return nil, "", false
	}

	return t, rel[0].value, true
}

// The units under a suffix.
const (
	ouPeople = "people"
	ouGroups = "groups"
	ouSites  = "sites"
)

var units = []string{ouPeople, ouGroups, ouSites}

// rootDSE is what this server is, as the connection it answers sees it: the
// suffix of the tenant it is bound in, or none before a bind, and what it can
// be asked to do.
//
// StartTLS is listed where a StartTLS on this connection would be taken: the
// listener holds a certificate ([Config.StartTLS]) and the connection is not
// under TLS already. It was listed always, and with no certificate the request
// is then refused -- so a client that reads this to decide whether a bind can
// be protected was told yes and answered no (#281).
func (d *Directory) rootDSE(c *wire.Conn, bound *tenant) *entry {
	e := newEntry(dn{})
	e.add("objectClass", "top")
	e.add("supportedLDAPVersion", "3")
	if bound != nil {
		e.add("namingContexts", bound.base.String())
	}
	e.add("supportedControl", wire.OidPagedResults)
	if d.c.StartTLS && !c.Secure() {
		e.add("supportedExtension", wire.OidStartTLS)
	}
	e.add("supportedExtension", wire.OidWhoAmI)
	e.add("vendorName", "roster")

	return e
}

func (d *Directory) organisation(t *tenant) *entry {
	e := newEntry(t.base)
	e.add("objectClass", "top", "organization")
	e.add("o", t.alias)
	// The RDN's own attribute is present on every entry, RFC 4512 § 2.3.
	if t.base[0].attr != "o" {
		e.add(t.base[0].attr, t.base[0].value)
	}

	return e
}

func (d *Directory) unit(t *tenant, ou string) *entry {
	e := newEntry(t.base.child("ou", ou))
	e.add("objectClass", "top", "organizationalUnit")
	e.add("ou", ou)

	return e
}

// keyOf and withKey carry the credential a call goes out with: the tenant's
// key for the process's own reads, the client's app password for the one
// call that checks it.
type keyKey struct{}

// cred is what a call goes out with: a key, and the tenant it is narrowed to
// when it is a deployment key.
type cred struct{ key, at string }

func withKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, keyKey{}, cred{key: key})
}

// on is a context whose calls go out as this tenant's.
func (t *tenant) on(ctx context.Context) context.Context {
	return context.WithValue(ctx, keyKey{}, cred{key: t.key, at: t.at})
}

func keyOf(ctx context.Context) (cred, bool) {
	v, ok := ctx.Value(keyKey{}).(cred)

	return v, ok && v.key != ""
}

// fronted is every tenant this directory fronts, by alias, each proved to be
// readable with what it will be read with: a tenant key reading its own
// tenant, or the deployment key narrowed to the holder each tenant nominated.
func (d *Directory) fronted(ctx context.Context) (map[string]*tenant, error) {
	out := map[string]*tenant{}

	for alias, key := range d.c.Keys {
		v, err := d.roster.Tenant().Get(withKey(ctx, key), rstr.TenantGetRequest_builder{
			Ref: rstr.TenantRef_builder{Alias: proto.String(alias)}.Build(),
		}.Build())
		if err != nil {
			return nil, fmt.Errorf("ldap: the key for %q cannot see %q: %w", alias, alias, err)
		}
		id, err := pdid.From(v.GetId())
		if err != nil {
			return nil, err
		}
		out[alias] = &tenant{alias: alias, id: id, key: key}
	}
	if d.c.Key == "" {
		return out, nil
	}

	after := ""
	for {
		vs, err := d.roster.Nomination().List(withKey(ctx, d.c.Key), rstr.NominationListRequest_builder{
			Size: 100, After: after,
		}.Build())
		if err != nil {
			return nil, fmt.Errorf("ldap: which tenants nominated this directory's key: %w", err)
		}
		for _, v := range vs.GetItems() {
			id, err := pdid.From(v.GetTenant().GetId())
			if err != nil {
				return nil, err
			}

			t := &tenant{id: id, key: d.c.Key, at: front.AtTenant(id.String())}
			tn, err := d.roster.Tenant().Get(t.on(ctx), rstr.TenantGetRequest_builder{
				Ref:    rstr.TenantRef_builder{Id: id.Bytes()}.Build(),
				Select: rstr.TenantSelect_builder{Alias: proto.Bool(true)}.Build(),
			}.Build())
			if err != nil {
				// The tenant's own doing -- a holder disabled, a role narrowed --
				// so the tenant goes unfronted and the directory serves everybody
				// else. Failing here failed the directory for every tenant.
				d.c.Log.Warn("ldap: a tenant nominated a holder for this directory that cannot read it; not fronting it",
					slog.String("tenant", id.String()), slog.Any("err", err))
				continue
			}
			t.alias = tn.GetAlias()
			out[t.alias] = t
		}
		if after = vs.GetNext(); after == "" {
			return out, nil
		}
	}
}

// refusal turns a roster error into the search's result. Not found is an
// empty answer rather than an error, because a search for nobody found
// nobody; a key that may not read is said so, because that is the
// operator's to fix and silence would hide it.
//
// A roster that did not answer is said as that, in this process's words. It
// was `operationsError` with the error's own sentence, and the sentence of a
// roster that cannot be reached is gRPC's: it names the address this process
// dials roster at, to whoever is bound (#281). Away or failing is
// `unavailable`, held to a rate is `busy`, and what is left -- roster
// answering that it will not, in a code none of the above -- is the code's
// name and nothing roster or the transport wrote.
func refusal(err error) wire.Result {
	switch code := status.Code(err); code {
	case codes.NotFound:
		return wire.Ok
	case codes.PermissionDenied, codes.Unauthenticated:
		return wire.Refuse(wire.InsufficientAccessRights, "the directory's key cannot read this: "+status.Convert(err).Message())
	case codes.InvalidArgument:
		return wire.Refuse(wire.ProtocolError, status.Convert(err).Message())
	case codes.DeadlineExceeded, codes.Canceled:
		return wire.Refuse(wire.TimeLimitExceeded, "")
	case codes.ResourceExhausted:
		return wire.Refuse(resultBusy, sayBusy)
	case codes.Unavailable, codes.Internal, codes.Unknown, codes.DataLoss, codes.Aborted:
		return wire.Refuse(resultUnavailable, sayAway)
	default:
		return wire.Refuse(wire.OperationsError, "roster would not answer this read: "+code.String())
	}
}
