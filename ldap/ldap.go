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
	// where the default is `o=contoso`.
	Bases map[string]string

	// Bind is what a password may be. Zero is [BindKey].
	Bind Mode

	// PageSize is the most people one roster read fetches, and so the most
	// one page of a paged search carries. Zero is [DefaultPageSize].
	PageSize int

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
// kind of key. Anything else presented as a password is a password.
const KeyPrefix = "rt_"

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

// Bind is the wire's.
func (d *Directory) Bind(ctx context.Context, c *wire.Conn, req wire.BindRequest) wire.Result {
	// One answer for everything that is not a bind that works, so that the
	// shape of the tree and who is in it are learned by searching -- which
	// needs a bind -- and never by trying names.
	refused := wire.Refuse(wire.InvalidCredentials, "")

	t, alias, ok := d.bindDN(req.DN)
	if !ok || len(req.Password) == 0 {
		return refused
	}

	pw := string(req.Password)
	if strings.HasPrefix(pw, KeyPrefix) {
		if !d.c.Bind.keys() {
			return wire.Refuse(wire.InvalidCredentials, "this directory binds with passwords; an app password was given")
		}
		// Bearing the key the client presented, not this process's: whose
		// it is, is the whole question, and roster answers it.
		v, err := d.me.Get(withKey(ctx, pw), rstr.MeGetRequest_builder{}.Build())
		if err != nil {
			return refused
		}
		if v.GetAlias() != alias || string(v.GetTenant()) != string(t.id.Bytes()) {
			return refused
		}
		c.Keep(req.DN, &binding{tenant: t, holder: v.GetId(), key: pw, asked: d.c.Now()})

		return wire.Ok
	}

	if !d.c.Bind.passwords() {
		return wire.Refuse(wire.InvalidCredentials, "this directory binds with app passwords: a key the person mints for this client")
	}
	// Read before roster is asked, so that whatever ended their sign-ins
	// while it answered is after it.
	began := d.c.Now()
	v, err := d.vouch.Verify(t.on(ctx), rstr.VouchVerifyRequest_builder{
		Who:    rstr.VouchWho_builder{Tenant: t.alias, Alias: alias}.Build(),
		Secret: req.Password,
	}.Build())
	switch {
	case err != nil:
		if status.Code(err) == codes.PermissionDenied || status.Code(err) == codes.Unauthenticated {
			d.c.Log.Warn("ldap: the directory's key cannot call Vouch.Verify; --bind password needs it", slog.String("tenant", t.alias))
		}

		return refused
	case v.GetOk():
		c.Keep(req.DN, &binding{tenant: t, holder: v.GetHolder(), began: began, asked: d.c.Now()})

		return wire.Ok
	case v.GetContinuation() != "":
		// The password was right and the sign-in is not finished: a second
		// factor is enrolled, and there is no form here to ask for it. The
		// diagnostic is for the person reading their client's log, and it
		// says nothing a wrong password would not have -- the refusal code is
		// the same, and only somebody holding the right password reaches it.
		return wire.Refuse(wire.InvalidCredentials, "a second factor is enrolled; use an app password for this client")
	default:
		return refused
	}
}

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
//     binds again, and is refused at the bind as anybody stopped is;
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
// the same line for a stream, in the same codes.
func unsaid(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Internal, codes.ResourceExhausted:
		return true
	default:
		return false
	}
}

// bindDN reads `uid=<alias>,ou=people,<suffix>`: the one shape a bind names.
func (d *Directory) bindDN(s string) (*tenant, string, bool) {
	name, err := parseDN(s)
	if err != nil {
		return nil, "", false
	}
	t, rel, ok := d.tenantOf(name)
	if !ok || len(rel) != 2 || rel[0].attr != "uid" || rel[1].attr != "ou" || !strings.EqualFold(rel[1].value, ouPeople) {
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
// suffix of the tenant it is bound in, or none before a bind.
func (d *Directory) rootDSE(bound *tenant) *entry {
	e := newEntry(dn{})
	e.add("objectClass", "top")
	e.add("supportedLDAPVersion", "3")
	if bound != nil {
		e.add("namingContexts", bound.base.String())
	}
	e.add("supportedControl", wire.OidPagedResults)
	e.add("supportedExtension", wire.OidStartTLS, wire.OidWhoAmI)
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
func refusal(err error) wire.Result {
	switch status.Code(err) {
	case codes.NotFound:
		return wire.Ok
	case codes.PermissionDenied, codes.Unauthenticated:
		return wire.Refuse(wire.InsufficientAccessRights, "the directory's key cannot read this: "+status.Convert(err).Message())
	case codes.InvalidArgument:
		return wire.Refuse(wire.ProtocolError, status.Convert(err).Message())
	case codes.DeadlineExceeded, codes.Canceled:
		return wire.Refuse(wire.TimeLimitExceeded, "")
	default:
		return wire.Refuse(wire.OperationsError, status.Convert(err).Message())
	}
}
