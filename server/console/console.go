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
	// Best effort, deliberately. The cookie is how a browser holds the
	// session, and over HTTP this never fails. What can fail is the transport
	// having nowhere to put a header at all -- a message port, which is what
	// the sandbox calls over (`wasm/main.go`), where there is no cookie jar
	// for it to reach anyway and `auth.Plain` stands behind it. A sign-in
	// refused for that reason read as a wrong password on the sandbox's form.
	_ = grpc.SetHeader(ctx, metadata.Pairs("set-cookie", c.String()))

	return &app.AuthSignInResponse{}, nil
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
