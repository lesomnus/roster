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
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
)

// Base is where the protocol is served. A deployment exposes this path and
// nothing else to the directory's cloud.
const Base = "/scim/v2"

// Methods is what a directory's key calls, and the whole of it: what `roster
// scim provision` writes into the role it binds the directory's holder and
// into the key it mints.
//
// What is **not** here is the point of it. No `Identity.Add`, no address
// written or attested, no `Holder.Add`, no `Erase`: each of those reaches
// people who already exist, and a key a directory's cloud presents from the
// internet should reach only the people it makes (`Provision`) and the people
// it signs in (`Deactivate`, `Activate`, a profile it owns).
var Methods = []string{
	rstr.MeService_Get_FullMethodName,
	rstr.ConnectionService_List_FullMethodName,
	rstr.HolderService_Get_FullMethodName,
	rstr.HolderService_Update_FullMethodName,
	rstr.HolderService_Provision_FullMethodName,
	rstr.HolderService_Deactivate_FullMethodName,
	rstr.HolderService_Activate_FullMethodName,
	rstr.EmailService_Get_FullMethodName,
	rstr.EmailService_List_FullMethodName,
	rstr.IdentityService_Get_FullMethodName,
	rstr.IdentityService_List_FullMethodName,
}

// DefaultConcurrency is how many requests are worked on at once when nothing
// says otherwise. Low on purpose: a directory's provisioning cycle is a burst,
// and the rate a tenant's callers are held to is shared with the people of
// that tenant signing in.
const DefaultConcurrency = 4

// Config is what the endpoint is built from.
type Config struct {
	// Roster is where roster's data plane speaks gRPC.
	Roster string

	// Insecure dials roster without TLS.
	Insecure bool

	// Concurrency bounds the requests in flight; [DefaultConcurrency] when
	// zero.
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

	mux   *http.ServeMux
	slots chan struct{}

	// known is what a token's tenant is, kept for a minute: every request
	// would otherwise ask twice before it asked anything it came for.
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

	until time.Time
}

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
		slots:  make(chan struct{}, c.Concurrency),
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

	m.Handle("GET "+Base+"/ServiceProviderConfig", s.authed(serviceProviderConfig))
	m.Handle("GET "+Base+"/ResourceTypes", s.authed(resourceTypes))
	m.Handle("GET "+Base+"/ResourceTypes/{name}", s.authed(resourceType))
	m.Handle("GET "+Base+"/Schemas", s.authed(schemas))
	m.Handle("GET "+Base+"/Schemas/{urn}", s.authed(schema))

	m.Handle("GET "+Base+"/Users", s.authed(s.listUsers))
	m.Handle("POST "+Base+"/Users", s.authed(s.createUser))
	m.Handle("GET "+Base+"/Users/{id}", s.authed(s.getUser))
	m.Handle("PUT "+Base+"/Users/{id}", s.authed(s.replaceUser))
	m.Handle("PATCH "+Base+"/Users/{id}", s.authed(s.patchUser))
	m.Handle("DELETE "+Base+"/Users/{id}", s.authed(s.deleteUser))

	// Groups are the next step (#86), and a directory told so by status
	// rather than by silence skips them rather than retrying them.
	m.Handle(Base+"/Groups", s.authed(groups))
	m.Handle(Base+"/Groups/", s.authed(groups))

	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "", "nothing is served here but "+Base)
	})

	s.mux = m
}

// authed is a route behind the bearer: a tenant key, or nothing.
func (s *Server) authed(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="roster"`)
			fail(w, http.StatusUnauthorized, "", "a tenant key, as `Authorization: Bearer rt_…`")

			return
		}

		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-r.Context().Done():
			return
		}

		// The bearer and nothing else: the context going out is made here,
		// not carried over from the request.
		ctx := metadata.AppendToOutgoingContext(r.Context(), "authorization", "Bearer "+token)

		t, err := s.tenancyOf(ctx, token)
		if err != nil {
			s.failed(w, r, err)

			return
		}
		if err := h(ctx, w, r, t); err != nil {
			s.failed(w, r, err)
		}
	})
}

// bearer is the request's tenant key. A deployment key is refused here: it is
// no tenant until a call names one, which is what this endpoint never does.
func bearer(r *http.Request) (string, bool) {
	v := r.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(v, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "rt_") {
		return "", false
	}

	return token, true
}

// tenancyOf is which tenant a key is, and which connection that tenant's
// directory provisions through.
func (s *Server) tenancyOf(ctx context.Context, token string) (*tenancy, error) {
	k := sha256.Sum256([]byte(token))
	if v, ok := s.known.Load(k); ok {
		if t := v.(*tenancy); time.Now().Before(t.until) {
			return t, nil
		}
	}

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
				t.provider = c.GetName()
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
