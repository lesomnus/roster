// Package console is what a console asks that no entity answers.
//
// Two services, and they have one thing in common: each makes a credential.
// `AuthService` makes a session and ends one; `IssueService` makes a key or a
// password and answers with it exactly once.
//
// Everything else a console does is an ordinary entity RPC, which is why there
// is so little here. What is here is what the generated servers structurally
// cannot do -- and in both cases the reason is the same declaration.
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

	"github.com/lesomnus/z"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/vouch"
)

// Auth is `AuthService` over this plane's holders.
//
// `s` has no wall on it, for the reason `cmd.Resolver` and `vouch.Verify` read
// one: this runs before anybody has been resolved, which is the whole of what
// it is for.
func Auth(s app.Server, db *ent.Client, sessions *authsession.Sessions) app.AuthServiceServer {
	return authed{s: s, db: db, sessions: sessions, v: vouch.New(s, s)}
}

type authed struct {
	app.UnimplementedAuthServiceServer

	s        app.Server
	db       *ent.Client
	sessions *authsession.Sessions
	v        app.VouchServiceServer
}

func (a authed) SignIn(ctx context.Context, req *app.AuthSignInRequest) (*app.AuthSignInResponse, error) {
	if req.GetAlias() == "" || req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "both an alias and a password")
	}

	// The one tenant this plane has, by **alias**, because that is what
	// `VouchWho` names one by: the pair a username field and a tenant selector
	// make, rather than an identifier a form would have to be told.
	who, err := a.db.Tenant.Query().First(ctx)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "this deployment has no owner")
	}

	res, err := a.v.Verify(ctx, app.VouchVerifyRequest_builder{
		Who: app.VouchWho_builder{
			Tenant: who.Alias,
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
	if err := grpc.SetHeader(ctx, metadata.Pairs("set-cookie", c.String())); err != nil {
		return nil, err
	}

	return &app.AuthSignInResponse{}, nil
}

func (a authed) SignOut(ctx context.Context, req *app.AuthSignOutRequest) (*app.AuthSignOutResponse, error) {
	var was string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		was = a.sessions.KeyOf(md.Get("cookie"))
	}

	return &app.AuthSignOutResponse{}, grpc.SetHeader(ctx,
		metadata.Pairs("set-cookie", a.sessions.End(ctx, was).String()))
}

// Issue is `IssueService` over this plane's rows.
func Issue(s app.Server, db *ent.Client) app.IssueServiceServer {
	return issuer{s: s, db: db, v: vouch.New(s, s)}
}

type issuer struct {
	app.UnimplementedIssueServiceServer

	s  app.Server
	db *ent.Client
	v  app.VouchServiceServer
}

func (i issuer) IssueKey(ctx context.Context, req *app.IssueKeyRequest) (*app.IssueKeyResponse, error) {
	if req.GetService() == "" || req.GetAlias() == "" {
		return nil, status.Error(codes.InvalidArgument, "a service and a name for the key")
	}
	if len(req.GetMethods()) == 0 {
		// Refused rather than defaulted in either direction. Everything hands
		// out more than was asked for; nothing mints a key that silently does
		// not work.
		return nil, status.Error(codes.InvalidArgument, "methods: a key that allows nothing opens no door")
	}

	who, err := i.holder(ctx, req.GetService())
	if err != nil {
		return nil, err
	}

	// The deployment's own kind. A `rt_` is a customer's and belongs to the
	// other plane; this service is served here and mints what belongs here.
	token, sum, err := keys.Mint(keys.PrefixDeployment)
	if err != nil {
		return nil, err
	}

	add := app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:   req.GetAlias(),
		Secret:  sum,
		Methods: req.GetMethods(),
	}
	if v := req.GetExpires(); v != nil {
		add.DateExpires = v
	}

	// Through the **walled** server, so the escalation rule runs: nobody hands
	// out a method they do not hold. `roster key add` goes around it through
	// `Ungated`, where there is no frame at all, which is the deployment doing
	// its own work rather than anybody asking.
	v, err := i.s.ApiKey().Add(ctx, add.Build())
	if err != nil {
		return nil, err
	}

	return app.IssueKeyResponse_builder{Token: token, Key: v}.Build(), nil
}

func (i issuer) IssuePassword(ctx context.Context, req *app.IssuePasswordRequest) (*app.IssuePasswordResponse, error) {
	if req.GetAlias() == "" {
		return nil, status.Error(codes.InvalidArgument, "whose")
	}

	who, err := i.holder(ctx, req.GetAlias())
	if err != nil {
		return nil, err
	}

	secret, err := passphrase()
	if err != nil {
		return nil, err
	}

	// Hashed by the service that will later check it, so the argon2 parameters
	// are in one place. A hash computed here would be a second set of them, and
	// the weaker of the two is the one that matters.
	if _, err := i.v.Set(ctx, app.VouchSetRequest_builder{
		Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
		Secret: []byte(secret),
	}.Build()); err != nil {
		return nil, err
	}

	return app.IssuePasswordResponse_builder{Password: secret}.Build(), nil
}

// holder is somebody of this plane's one tenant, made if they are not there.
//
// Made rather than refused, for the reason `roster key add` gives: naming a
// service is the moment it becomes a caller of this deployment, and asking for
// two commands to express one intent is how a runbook grows a step nobody
// remembers.
func (i issuer) holder(ctx context.Context, alias string) (pdid.Id, error) {
	t, err := i.db.Tenant.Query().First(ctx)
	if err != nil {
		return pdid.Nil, status.Error(codes.FailedPrecondition, "this deployment has no owner")
	}

	in := pdid.Id(t.ID)

	v, err := i.s.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Alias:  z.Ptr(alias),
				Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	if err == nil {
		return pdid.From(v.GetId())
	}
	if status.Code(err) != codes.NotFound {
		return pdid.Nil, err
	}

	w, err := i.s.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(w.GetId())
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
