// Package console is what a console asks that no entity answers.
//
// One service now: `AuthService`, which makes a session and ends one. It is
// here because a session belongs to no entity's rows and because the credential
// it makes is a **response header** -- `set-cookie` as response metadata, which
// `web.Transcode` hands to a browser -- and no generated verb can answer with
// one.
//
// `IssueService` was the other, and it made a key or a password and answered
// once. Both are verbs on the rows they write: `ApiKey.Issue` and
// `Credential.Issue`. What was left here after the first moved was a password
// written through the generated `Credential` verbs, which is to say written
// with none of the rules `server/core` puts on that column.
//
// Everything else a console does is an ordinary entity RPC, which is why there
// is so little here.
//
// # Where these are served
//
// The control plane's listener, over the control plane's own rows. An operator
// is a holder of that plane, and so is a service that calls this deployment.
package console

import (
	"context"
	"crypto/rand"
	"encoding/base64"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
	"google.golang.org/protobuf/proto"
)

// Auth is `AuthService` over this plane's holders.
//
// `s` has no wall on it, for the reason `cmd.Resolver` and `vouch.Verify` read
// one: this runs before anybody has been resolved, which is the whole of what
// it is for.
//
// # Which tenant somebody is signing in to
//
// The control plane has one, so nothing has to say: [Auth] with no option
// takes the tenant it finds. A plane with many needs telling, and that is
// [WithTenant] -- a function of the request, because on the data plane the
// answer is the name a browser arrived at and there is nowhere else it could
// come from. A resolver that refuses is a sign-in refused, which is the right
// direction: one that carried on with no tenant would look somebody up in
// whichever one it happened to reach, the failure `front.WhoseHost` names.
func Auth(s app.Server, db *ent.Client, sessions *authsession.Sessions, opts ...Option) app.AuthServiceServer {
	a := authed{s: s, db: db, sessions: sessions, v: vouch.New(s, s)}
	for _, opt := range opts {
		opt(&a)
	}

	return a
}

// Option is what [Auth] is told beyond the three things it is built on.
type Option func(*authed)

// WithTenant is which tenant a sign-in is about, by alias, from the request.
func WithTenant(fn func(ctx context.Context) (string, error)) Option {
	return func(a *authed) { a.tenant = fn }
}

// WithArrival is the name the request arrived at, beside the tenant it names.
//
// For the one sign-in that is bound to a name rather than to a secret: a link
// a front door handed the browser is spent at the name it was minted for
// (`Link.at`), so the user console compares the name and not only the tenant.
// Two of a tenant's names are two doors, and a link for one is not a link for
// the other. `cmd.ArrivedAt` is it on a served deployment; the sandbox
// supplies the name it made up, the way it does for [WithTenant].
func WithArrival(fn func(ctx context.Context) string) Option {
	return func(a *authed) { a.arrival = fn }
}

type authed struct {
	app.UnimplementedAuthServiceServer

	s        app.Server
	db       *ent.Client
	sessions *authsession.Sessions
	v        *vouch.Server

	// tenant is [WithTenant], and nil is the one this plane has.
	tenant func(ctx context.Context) (string, error)

	// arrival is [WithArrival], and nil is a plane nothing arrives at by name.
	arrival func(ctx context.Context) string
}

func (a authed) SignIn(ctx context.Context, req *app.AuthSignInRequest) (*app.AuthSignInResponse, error) {
	if link := req.GetLink(); link != "" {
		// Exactly one way of proving somebody, refused rather than resolved in
		// some order this comment would then have to define.
		if req.GetAlias() != "" || req.GetPassword() != "" {
			return nil, status.Error(codes.InvalidArgument, "a link, or an alias and a password; not both")
		}

		return a.handed(ctx, link)
	}

	if req.GetAlias() == "" || req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "both an alias and a password")
	}

	// Which tenant, by **alias**, because that is what `VouchWho` names one by:
	// the pair a username field and a tenant selector make, rather than an
	// identifier a form would have to be told.
	tenant, err := a.whose(ctx)
	if err != nil {
		return nil, err
	}

	res, err := a.v.Verify(ctx, app.VouchVerifyRequest_builder{
		Who: app.VouchWho_builder{
			Tenant: tenant,
			Alias:  req.GetAlias(),
		}.Build(),
		Secret: []byte(req.GetPassword()),
	}.Build())
	if err != nil {
		return nil, err
	}
	if !res.GetOk() {
		// One answer however it was wrong. Which of "no such person", "wrong
		// password" and "locked" it was is an oracle, and the lockout in
		// `server/vouch` is what makes guessing expensive rather than this.
		return nil, status.Error(codes.Unauthenticated, "no")
	}

	return a.admit(ctx, res)
}

// handed is a sign-in by a link a front door handed the browser: the person
// was checked at the tenant's own directory, `Vouch.Accept` minted a link for
// this name, and this spends it. `AuthSignInRequest.link` is the why.
//
// Only where a name decides the tenant. The control plane's door has no
// [WithArrival], and a link presented there is refused before it is looked up:
// nothing arrives at a name there, so nothing was ever minted for it.
func (a authed) handed(ctx context.Context, link string) (*app.AuthSignInResponse, error) {
	if a.tenant == nil || a.arrival == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"a link is spent at the name it was minted for, and nothing arrives at a name here")
	}

	tenant, err := a.whose(ctx)
	if err != nil {
		return nil, err
	}

	res, err := a.v.SpendAt(ctx, link, a.arrival(ctx), tenant)
	if err != nil {
		return nil, err
	}
	if !res.GetOk() {
		// One answer, as above: a link that was never one, one already spent,
		// one for another name. Told apart, this would say whether a string
		// was ever a real link.
		return nil, status.Error(codes.Unauthenticated, "no")
	}

	return a.admit(ctx, res)
}

// admit is the session a finished sign-in ends in, whichever way the person
// was proved: one function, so that a password and a link cannot come to two
// answers about what a session is.
func (a authed) admit(ctx context.Context, res *app.VouchVerifyResponse) (*app.AuthSignInResponse, error) {
	k, err := pdid.From(res.GetHolder())
	if err != nil {
		return nil, err
	}
	t, err := pdid.From(res.GetTenant())
	if err != nil {
		return nil, err
	}

	_, c, err := a.sessions.Mint(ctx, authsession.Session{
		Id:       k.String(),
		TenantId: t.String(),

		// Whatever this operator may do, which their bindings decide on every
		// call. A session is not the place to narrow it: a grant here would be
		// a second answer to a question the policy already answers, frozen at
		// the moment somebody signed in.
		Grant: frame.Whole(),
	})
	if err != nil {
		return nil, err
	}

	// The line the whole arrangement rests on. A cookie is an HTTP response
	// header and this handler has no response writer -- but `set-cookie` as
	// response metadata reaches the browser through `web.Transcode` as a header
	// like any other. See payday's `authsession.Mint`.
	// Best effort, deliberately. The cookie is how a browser holds the
	// session, and over HTTP this never fails. What can fail is the transport
	// having nowhere to put a header at all -- a message port, which is what
	// the sandbox calls over (`wasm/main.go`), where there is no cookie jar
	// for it to reach anyway and `auth.Plain` stands behind it. A sign-in
	// refused for that reason read as a wrong password on the sandbox's form.
	_ = grpc.SetHeader(ctx, metadata.Pairs("set-cookie", c.String()))

	return &app.AuthSignInResponse{}, nil
}

// whose is the tenant this sign-in is about.
//
// The plane's only one where nothing was said, which is the control plane and
// is why `AuthSignInRequest` has no tenant field: asking would be asking a
// question with a single answer.
// Offers is what this name lets somebody in with; `proto/app/auth.proto` says
// why a page has to ask and why this is a method rather than a route.
//
// Read with `a.s`, which has no wall on it -- the same reason [Auth]'s doc gives
// for `SignIn`: this runs before anybody has been resolved. What keeps it narrow
// is not a frame but the subject, which is `whose` and cannot be pointed
// anywhere else.
func (a authed) Offers(ctx context.Context, _ *app.AuthOffersRequest) (*app.AuthOffersResponse, error) {
	tenant, err := a.whose(ctx)
	if err != nil {
		return nil, err
	}

	t, err := a.s.Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    app.TenantRef_builder{Alias: &tenant}.Build(),
		Select: app.TenantSelect_builder{Config: proto.Bool(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	// The directories, by name and issuer and nothing else off the row --
	// the same two fields `GET /providers` answers, read here with the same
	// unwalled server for the same reason as the tenant above.
	cs, err := a.s.Connection().List(ctx, app.ConnectionListRequest_builder{
		Filters: []*app.ConnectionFilter{
			app.ConnectionFilter_builder{Tenant: app.TenantRef_builder{Id: t.GetId()}.Build()}.Build(),
		},
		Size: 100,
	}.Build())
	if err != nil {
		return nil, err
	}
	providers := make([]*app.AuthOffersProvider, 0, len(cs.GetItems()))
	for _, c := range cs.GetItems() {
		providers = append(providers, app.AuthOffersProvider_builder{
			Name:   c.GetName(),
			Issuer: c.GetIssuer(),
		}.Build())
	}

	// `vouch.Offers` and not the field, so the two places that answer this
	// question answer it the same way -- including *unset is yes*, which is what
	// a tenant written before the field existed relies on.
	return app.AuthOffersResponse_builder{
		Password:  vouch.Offers(t, vouch.KindPassword),
		Providers: providers,
		FrontDoor: t.GetConfig().GetFrontDoor(),
	}.Build(), nil
}

func (a authed) whose(ctx context.Context) (string, error) {
	if a.tenant != nil {
		return a.tenant(ctx)
	}

	v, err := a.db.Tenant.Query().First(ctx)
	if err != nil {
		return "", status.Error(codes.FailedPrecondition, "this deployment has no owner")
	}

	return v.Alias, nil
}

func (a authed) SignOut(ctx context.Context, req *app.AuthSignOutRequest) (*app.AuthSignOutResponse, error) {
	var was string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		was = a.sessions.KeyOf(md.Get("cookie"))
	}

	// Best effort, as above; the row is ended either way, which is the half
	// that matters.
	_ = grpc.SetHeader(ctx, metadata.Pairs("set-cookie", a.sessions.End(ctx, was).String()))

	return &app.AuthSignOutResponse{}, nil
}

// passphrase is 32 bytes from `crypto/rand`, printable.
//
// Long enough that it is not guessed and not a word anybody will recognise,
// because the one thing it must not be is something somebody keeps. It is for
// the first sign-in, and what happens next is that they change it.
func passphrase() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}
