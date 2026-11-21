// Package scim is roster as a SCIM 2.0 service provider (RFC 7643, RFC 7644):
// where a directory -- Entra, Okta, OneLogin -- pushes the people it
// provisions, what changed about them, and their leaving.
//
// # Why it is a consumer
//
// It reaches roster over the wire, as the caller that presented the token: the
// directory's tenant key, forwarded and nothing else. So every rule the
// tenant's own callers meet is met here -- the wall, the key's methods, the
// role behind it, the trail, the stream an app hears a suspension on -- and
// this package decides nothing those rules do not. `docs/scim.md` is the
// longer argument, and `docs/ldap.md` the other directory front, which reads
// where this writes.
//
// # What it never forwards
//
// Anything but the bearer. Not a cookie: the listener a deployment exposes
// this on may share a name with a page that keeps a session, and a browser's
// session must not be a way to write people. Not `roster-as` or `roster-at`:
// a tenant key is one tenant, which it says by being one.
package scim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// Base is where the protocol is served. A deployment exposes this path and
// nothing else to the directory's cloud.
const Base = "/scim/v2"

// Methods is what a directory's key calls, and the whole of it: what `roster
// scim provision` writes into the key it mints, and -- but for [InGroups] --
// into the role it binds the directory's holder.
//
// What is **not** here is the point of it. No `Identity.Add`, no address
// written or attested, no `Holder.Add`, no `Holder.Erase`: each of those
// would give somebody who already exists a way in, or take them away, from a
// key a directory's cloud presents from the internet.
//
// What is here reaches more than the people the directory makes and signs
// in, and is meant to. It writes the profile of anybody of the tenant no file
// declares (`Holder.Update`), and who they report to; it reads every
// person's addresses, accounts and numbers. Only making (`Provision`),
// suspending (`Deactivate`, `Activate`) and telling (`Tell`) are held to the
// connection the tenant provisions through: none is made where there is no
// such connection, a suspension reaches only who signs in through it, and
// what `Tell` writes is believed by nothing.
var Methods = []string{
	rstr.MeService_Get_FullMethodName,
	rstr.ConnectionService_List_FullMethodName,
	rstr.HolderService_Get_FullMethodName,
	rstr.HolderService_Update_FullMethodName,
	rstr.HolderService_Provision_FullMethodName,
	rstr.HolderService_Deactivate_FullMethodName,
	rstr.HolderService_Activate_FullMethodName,
	rstr.HolderService_Tell_FullMethodName,
	rstr.EmailService_Get_FullMethodName,
	rstr.EmailService_List_FullMethodName,
	rstr.IdentityService_Get_FullMethodName,
	rstr.IdentityService_List_FullMethodName,

	// Their numbers (`phones.go`): written onto whoever holds no more than
	// the directory does, which is everybody it provisions.
	rstr.PhoneService_Add_FullMethodName,
	rstr.PhoneService_Erase_FullMethodName,
	rstr.PhoneService_List_FullMethodName,

	// Who they report to (`manager.go`): a record no rule reads.
	rstr.RelationService_Add_FullMethodName,
	rstr.RelationService_Erase_FullMethodName,
	rstr.RelationService_List_FullMethodName,

	// Its groups (`groups.go`): made, read, and -- what [InGroups] names --
	// changed, and erased.
	rstr.GroupService_Add_FullMethodName,
	rstr.GroupService_Get_FullMethodName,
	rstr.GroupService_List_FullMethodName,
	rstr.GroupMembershipService_List_FullMethodName,
	rstr.GroupService_Update_FullMethodName,
	rstr.GroupService_Erase_FullMethodName,
	rstr.GroupMembershipService_Add_FullMethodName,
	rstr.GroupMembershipService_Erase_FullMethodName,
}

// InGroups is what of [Methods] a directory's holder is **not** bound: the
// writes that change a group, which it holds in each group it made -- as that
// group's manager, the way anybody who makes a group is -- and so for that
// group alone.
//
// Bound, they would reach every group of the tenant: `GroupMembership.Add`
// into a group nothing is bound to is asked nothing else, and `Group.Erase`
// of one is the same. The key lists them all the same, because a key's own
// list narrows what its holder holds and would otherwise narrow these away.
var InGroups = []string{
	rstr.GroupService_Update_FullMethodName,
	rstr.GroupService_Erase_FullMethodName,
	rstr.GroupMembershipService_Add_FullMethodName,
	rstr.GroupMembershipService_Erase_FullMethodName,
}

// Bound is what the role a directory's holder is bound names: [Methods]
// without [InGroups].
func Bound() []string {
	out := make([]string, 0, len(Methods))
	for _, m := range Methods {
		if !slices.Contains(InGroups, m) {
			out = append(out, m)
		}
	}

	return out
}

// DefaultConcurrency is how many requests of one tenant's directory are worked
// on at once when nothing says otherwise. Low on purpose: a directory's
// provisioning cycle is a burst, and the rate a tenant's callers are held to
// is shared with the people of that tenant signing in. That is a reason about
// one tenant, so the turns are counted for each: one tenant's directory in
// the middle of a burst holds no other tenant's back.
//
// Asking roster who a key is, for a key not remembered, is held to the same
// number for the whole process, since no tenant is known until it is asked.
const DefaultConcurrency = 4

// BodyTimeout is how long a request's body has to arrive once its key is
// accepted. A directory sends a body of a few hundred bytes with its headers;
// a body still on its way after this is a connection being held open, and it
// is answered `408` where it can still be answered. The body is read outside
// any turn, so what this bounds is a connection and a goroutine, not anybody
// else's request.
const BodyTimeout = 30 * time.Second

// Config is what the endpoint is built from.
type Config struct {
	// Roster is where roster's data plane speaks gRPC.
	Roster string

	// Insecure dials roster without TLS.
	Insecure bool

	// Concurrency is how many requests of one tenant's directory are worked
	// on at once, and how many keys not remembered are asked about at once;
	// [DefaultConcurrency] when nought or less. A negative number is refused
	// where a deployment's file is read, not here.
	Concurrency int

	// Log is where what was refused or ignored is said.
	Log *slog.Logger
}

// Server is the endpoint, an [http.Handler] serving [Base].
type Server struct {
	c Config

	conn   *grpc.ClientConn
	roster rstr.Client
	me     rstr.MeServiceClient

	mux *http.ServeMux

	// asks is the turns of asking roster who a key is: one for each request
	// whose key is not remembered, for the process.
	asks chan struct{}

	// turns is each tenant's turns of work, made the first time a key of that
	// tenant is accepted.
	turns sync.Map // string(tenant) -> chan struct{}

	// known is what a token's tenant is, kept for a minute for reads: every
	// read would otherwise ask twice before it asked anything it came for.
	known sync.Map // [32]byte -> *tenancy
}

// tenancy is what a directory's key says, read once a minute.
type tenancy struct {
	tenant []byte

	// self is the holder the key hangs off, which is never one of the people.
	self []byte

	// provider is the connection the tenant provisions through: the provider
	// of every identity this links.
	provider string

	// claim is the claim that connection names its people by
	// (`Connection.subject_claim`), which says how an `externalId` is spelled
	// ([tenancy.subject]).
	claim string

	until time.Time
}

// subject is an `externalId` as roster keeps the subject it is
// (`front.Subject`): a GUID in small letters where the connection names its
// people by `oid`, and as the directory wrote it anywhere else.
//
// Folded where a request comes in, so that what the directory says is
// compared with what roster holds in the spelling roster holds it in. Said in
// capitals and compared as said, the id somebody signs in by was another id
// than theirs: nobody was found by it, and it was kept beside them as though
// the two differed.
func (t *tenancy) subject(v string) string { return front.Subject(t.claim, v) }

// New dials roster and answers [Base].
func New(c Config) (*Server, error) {
	if c.Roster == "" {
		return nil, errors.New("scim: where roster speaks gRPC")
	}
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}

	creds := credentials.NewTLS(nil)
	if c.Insecure {
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(c.Roster, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("scim: %s: %w", c.Roster, err)
	}

	s := &Server{
		c:      c,
		conn:   conn,
		roster: rstr.NewClient(conn),
		me:     rstr.NewMeServiceClient(conn),
		asks:   make(chan struct{}, c.Concurrency),
	}
	s.routes()

	return s, nil
}

// Close lets go of the connection to roster.
func (s *Server) Close() error { return s.conn.Close() }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// handler is one route, given the context its calls to roster go out with.
type handler func(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error

func (s *Server) routes() {
	m := http.NewServeMux()

	// Each path, and what it takes. A path asked by a method it does not
	// take is `405` with `Allow`, answered here and not by the catch-all
	// below, which would say nothing is served at a path that is.
	for _, p := range []struct {
		path string
		by   map[string]handler
	}{
		{"/ServiceProviderConfig", map[string]handler{http.MethodGet: serviceProviderConfig}},
		{"/ResourceTypes", map[string]handler{http.MethodGet: resourceTypes}},
		{"/ResourceTypes/{name}", map[string]handler{http.MethodGet: resourceType}},
		{"/Schemas", map[string]handler{http.MethodGet: schemas}},
		{"/Schemas/{urn}", map[string]handler{http.MethodGet: schema}},
		{"/Users", map[string]handler{http.MethodGet: s.listUsers, http.MethodPost: s.createUser}},
		{"/Users/{id}", map[string]handler{
			http.MethodGet: s.getUser, http.MethodPut: s.replaceUser, http.MethodPatch: s.patchUser, http.MethodDelete: s.deleteUser,
		}},
		{"/Groups", map[string]handler{http.MethodGet: s.listGroups, http.MethodPost: s.createGroup}},
		{"/Groups/{id}", map[string]handler{
			http.MethodGet: s.getGroup, http.MethodPut: s.replaceGroup, http.MethodPatch: s.patchGroup, http.MethodDelete: s.deleteGroup,
		}},
	} {
		var allow []string
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			h, ok := p.by[method]
			if !ok {
				continue
			}
			m.Handle(method+" "+Base+p.path, s.authed(h))
			allow = append(allow, method)
			if method == http.MethodGet {
				// `net/http` serves a HEAD by the GET's handler.
				allow = append(allow, http.MethodHead)
			}
		}
		m.Handle(Base+p.path, notAllowed(strings.Join(allow, ", ")))
	}

	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "", "nothing is served here but "+Base)
	})

	s.mux = m
}

// notAllowed answers a path asked by a method it does not take.
func notAllowed(allow string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unread(w, r)
		w.Header().Set("Allow", allow)
		fail(w, http.StatusMethodNotAllowed, "", "this path takes "+allow)
	})
}

// authed is a route behind the bearer: a tenant key, or nothing.
//
// In this order, so that a turn is held only for work. The bearer's shape;
// who the key is, which asks roster -- in one of the process's turns of
// asking -- only for a key not remembered; then the body, to its bound and
// its clock and in no turn at all, since a body is the asker's to send as
// slowly as it likes; and then one of the tenant's turns, for the work.
// Nothing of a body is read before the key is accepted.
func (s *Server) authed(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r)
		if !ok {
			unread(w, r)
			w.Header().Set("WWW-Authenticate", `Bearer realm="roster"`)
			fail(w, http.StatusUnauthorized, "", "a tenant key, as `Authorization: Bearer rt_…`")

			return
		}

		// The bearer and nothing else: the context going out is made here,
		// not carried over from the request.
		ctx := metadata.AppendToOutgoingContext(r.Context(), "authorization", "Bearer "+token)

		t, err := s.tenancyOf(ctx, token, !reads(r))
		if err != nil {
			unread(w, r)
			s.failed(w, r, err)

			return
		}
		if err := read(w, r); err != nil {
			s.failed(w, r, err)

			return
		}

		turns := s.turnsOf(t.tenant)
		if !take(r.Context(), turns) {
			return
		}
		defer func() { <-turns }()

		if err := h(ctx, w, r, t); err != nil {
			s.failed(w, r, err)
		}
	})
}

// take is a turn of ch, or false where the asker went first.
func take(ctx context.Context, ch chan struct{}) bool {
	select {
	case ch <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// turnsOf is a tenant's turns of work.
func (s *Server) turnsOf(tenant []byte) chan struct{} {
	if v, ok := s.turns.Load(string(tenant)); ok {
		return v.(chan struct{})
	}
	v, _ := s.turns.LoadOrStore(string(tenant), make(chan struct{}, s.c.Concurrency))

	return v.(chan struct{})
}

// read is the request's body, read whole before the work: to [maxBody] and
// within [BodyTimeout], the connection's own read deadline. The handlers read
// it from the request as they did.
//
// Only a method that carries something is waited for. A body sent with any
// other is not read, and the connection ends with the answer.
func read(w http.ResponseWriter, r *http.Request) error {
	if !hasBody(r) {
		return nil
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		unread(w, r)

		return nil
	}

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(BodyTimeout))
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		unread(w, r)

		var large *http.MaxBytesError
		switch {
		case errors.As(err, &large):
			return &problem{status: http.StatusRequestEntityTooLarge, detail: "a body of at most a megabyte"}
		case errors.Is(err, os.ErrDeadlineExceeded):
			return &problem{status: http.StatusRequestTimeout, detail: "a body has thirty seconds to arrive"}
		default:
			return badRequest("invalidSyntax", "a body of at most a megabyte")
		}
	}
	// What is read is read: the connection is the asker's again, until its
	// next request's clocks.
	_ = rc.SetReadDeadline(time.Time{})
	r.Body = io.NopCloser(bytes.NewReader(b))

	return nil
}

func hasBody(r *http.Request) bool { return r.Body != nil && r.Body != http.NoBody }

// unread answers a request whose body will not be read, and ends its
// connection with the answer. `net/http` reads what is left of a declared
// body before it writes an answer, and after, to keep the connection; so a
// request refused before its key was accepted, that declared a body and sent
// a byte of it, was held for as long as its asker liked. The read deadline,
// passed, is what stops that reading; `Connection: close` is the answer
// saying so.
func unread(w http.ResponseWriter, r *http.Request) {
	if !hasBody(r) {
		return
	}
	w.Header().Set("Connection", "close")
	_ = http.NewResponseController(w).SetReadDeadline(time.Now())
}

// bearer is the request's tenant key. A deployment key is refused here: it is
// no tenant until a call names one, which is what this endpoint never does.
//
// A key is `rt_` and then RFC 6750's `b64token`, and anything else is no key:
// refused here, in the words any bearer that is no tenant key gets, with
// roster not asked. It took any bytes after `rt_`, and gRPC sends no header
// that holds some of them, so a bearer anybody could write was a `500` and a
// line of the log at `ERROR`.
func bearer(r *http.Request) (string, bool) {
	v := r.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(v, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	rest, ok := strings.CutPrefix(token, "rt_")
	if !ok || !b64token(rest) {
		return "", false
	}

	return token, true
}

// b64token is whether v is RFC 6750's `b64token`: one or more of its letters,
// then any number of `=`.
func b64token(v string) bool {
	v = strings.TrimRight(v, "=")
	if v == "" {
		return false
	}
	for _, c := range []byte(v) {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '-', c == '.', c == '_', c == '~', c == '+', c == '/':
		default:
			return false
		}
	}

	return true
}

// reads is whether a request only reads, and so may be answered from what is
// remembered of its key.
func reads(r *http.Request) bool { return r.Method == http.MethodGet || r.Method == http.MethodHead }

// tenancyOf is which tenant a key is, and which connection that tenant's
// directory provisions through.
//
// Remembered for a minute, for reads. A write asks again (fresh) and what it
// learns is remembered for the reads after it: with `provisions` taken off a
// connection, a rename, a number and every write of a group landed for that
// minute, since only making, suspending and telling ask roster about the
// mark themselves. A write that is refused leaves what was remembered as it
// was, so a read within the minute is still answered.
func (s *Server) tenancyOf(ctx context.Context, token string, fresh bool) (*tenancy, error) {
	k := sha256.Sum256([]byte(token))
	if v, ok := s.known.Load(k); ok && !fresh {
		if t := v.(*tenancy); time.Now().Before(t.until) {
			return t, nil
		}
	}

	if !take(ctx, s.asks) {
		return nil, ctx.Err()
	}
	defer func() { <-s.asks }()

	me, err := s.me.Get(ctx, rstr.MeGetRequest_builder{}.Build())
	if err != nil {
		return nil, err
	}

	t := &tenancy{tenant: me.GetTenant(), self: me.GetId(), until: time.Now().Add(time.Minute)}
	after := ""
	for t.provider == "" {
		vs, err := s.roster.Connection().List(ctx, rstr.ConnectionListRequest_builder{
			Filters: []*rstr.ConnectionFilter{rstr.ConnectionFilter_builder{
				Tenant: rstr.TenantRef_builder{Id: t.tenant}.Build(),
			}.Build()},
			After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, c := range vs.GetItems() {
			if c.GetProvisions() {
				t.provider, t.claim = c.GetName(), c.GetSubjectClaim()
			}
		}

		after = vs.GetNext()
		if after == "" && t.provider == "" {
			return nil, status.Error(codes.FailedPrecondition,
				"no connection of this tenant provisions its people; name one with `provisions`")
		}
	}

	s.known.Store(k, t)

	return t, nil
}
