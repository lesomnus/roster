// Package ldap is a directory over roster: what `roster ldap serve` is.
//
// It is a consumer, exactly as `account` is -- its own process, one tenant
// key per operator it fronts, reaching roster over the wire and never past
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
// # Why a password bind cannot walk around a second factor
//
// Not by a rule here. `Vouch.Verify` answers `ok` only when the sign-in is
// finished; for somebody with a second factor it answers `ok: false` with a
// continuation this process has no second form to spend. So the refusal is
// roster's, the same one that keeps a product app that has never heard of
// second factors failing closed, and this package only reports it.
package ldap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

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

	// Keys is one tenant key per operator this directory fronts, by the
	// tenant's alias. Minted for a holder in that tenant whose role names
	// what a directory reads (`docs/ldap.md` § The key this process holds).
	Keys map[string]string

	// Bases renames a tenant's suffix, by alias: `dc=contoso,dc=example`
	// where the default is `o=contoso`.
	Bases map[string]string

	// Bind is what a password may be. Zero is [BindKey].
	Bind Mode

	// PageSize is the most people one roster read fetches, and so the most
	// one page of a paged search carries. Zero is [DefaultPageSize].
	PageSize int

	Log *slog.Logger
}

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
	case len(c.Keys) == 0:
		return nil, errors.New("ldap: Keys: one tenant key per operator this directory fronts; none is nobody to front")
	}
	for alias := range c.Bases {
		if _, ok := c.Keys[alias]; !ok {
			return nil, fmt.Errorf("ldap: Bases: a suffix for %q, and no key for it", alias)
		}
	}
	if c.PageSize <= 0 || c.PageSize > DefaultPageSize {
		c.PageSize = DefaultPageSize
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}

	creds := credentials.NewTLS(nil)
	if c.Insecure {
		creds = insecure.NewCredentials()
	}
	opts := append(auth.Inject(auth.ProviderFunc(func(ctx context.Context) context.Context {
		if k, ok := keyOf(ctx); ok {
			return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+k)
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

	aliases := make([]string, 0, len(c.Keys))
	for alias := range c.Keys {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		key := c.Keys[alias]
		v, err := d.roster.Tenant().Get(withKey(ctx, key), rstr.TenantGetRequest_builder{
			Ref: rstr.TenantRef_builder{Alias: proto.String(alias)}.Build(),
		}.Build())
		if err != nil {
			conn.Close()

			return nil, fmt.Errorf("ldap: the key for %q cannot see %q: %w", alias, alias, err)
		}
		id, err := pdid.From(v.GetId())
		if err != nil {
			conn.Close()

			return nil, err
		}

		base := dn{{attr: "o", value: alias}}
		if s, ok := c.Bases[alias]; ok {
			base, err = parseDN(s)
			if err != nil || len(base) == 0 {
				conn.Close()

				return nil, fmt.Errorf("ldap: Bases[%s]: %q is not a DN", alias, s)
			}
		}
		d.tenants = append(d.tenants, &tenant{alias: alias, id: id, key: key, base: base})
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

		return wire.Ok
	}

	if !d.c.Bind.passwords() {
		return wire.Refuse(wire.InvalidCredentials, "this directory binds with app passwords: a key the person mints for this client")
	}
	v, err := d.vouch.Verify(withKey(ctx, t.key), rstr.VouchVerifyRequest_builder{
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

func (d *Directory) rootDSE() *entry {
	e := newEntry(dn{})
	e.add("objectClass", "top")
	e.add("supportedLDAPVersion", "3")
	e.add("namingContexts", d.NamingContexts()...)
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

func withKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, keyKey{}, key)
}

func keyOf(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(keyKey{}).(string)

	return v, ok && v != ""
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
