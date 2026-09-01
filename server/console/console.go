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
	return issuer{s: s, db: db, prefix: keys.PrefixDeployment}
}

// IssueTenant is the same service on the **data plane**, minting a customer's
// kind.
//
// # What differs, and what deliberately does not
//
// The prefix, and how a holder is named. Everything else -- the secret made
// here, the hash stored before the answer leaves, the answer given once -- is
// the same code, because those were never facts about which plane this is.
//
// The **rules** are the same too, and they are not written here: minting goes
// through the walled server, so `core.ApiKey.Add` runs both of the two that
// matter. *Nobody hands out a method they do not hold* -- `mayGrant` -- and
// *nobody writes a way into an account wider than their own*, because a key
// resolves to its holder and calls made with it are made as them.
//
// That second one is the whole reason this can be served to customers at all.
// Without it, somebody who may mint keys could mint one on the administrator's
// holder carrying a method they themselves hold, and then call as the
// administrator -- which is the finding `core/apikey.go` records, one door
// over.
//
// # Why a customer minting their own key is safe to offer
//
// Because it hands out nothing new. An `rt_` resolves to a person and is
// narrowed by what that person may do, so a key is at most a second copy of a
// credential they already have -- and less, since it names methods. What it
// replaces is `roster key add`, which is a shell on the box, and a shell is not
// something a customer has or should be given.
func IssueTenant(s app.Server, db *ent.Client) app.IssueServiceServer {
	return issuer{s: s, db: db, prefix: keys.PrefixTenant}
}

type issuer struct {
	app.UnimplementedIssueServiceServer

	s  app.Server
	db *ent.Client

	// prefix is which plane this instance mints for, and it is not in any
	// request: a caller that could name it could ask the customer-facing port
	// for a key of the deployment's own kind, and the prefix is exactly what
	// tells the two apart. See `server/keys`.
	prefix string
}

// IssuePassword is the control plane's alone, and refuses on the other.
//
// It names somebody by a bare alias, which only says one person where there is
// one tenant -- the control plane. On the data plane an alias names one person
// per customer, and this does not take a tenant to tell them apart: it would
// resolve against `holder`'s `Tenant.Query().First()`, an arbitrary tenant, and
// make the row if it were not there. So a data-plane caller holding this method
// could set -- and create -- a password on somebody in a tenant they never
// named, through the generated `Credential` verbs, which `server/core` leaves
// unguarded for the deployment's own work: the escalation rule that guards
// every other credential write is not in this path. `IssueKey` is safe there
// because `whose` refuses a bare name and reads the reference back through the
// wall; this took neither guard.
//
// A customer's person gets a password the guarded way -- `VouchService.Reset`
// or `Set`, served on the data plane with `WithReach` -- so nothing is lost by
// closing this here. `roster issue password` is control-plane only for exactly
// this reason, and now it is the server that says so rather than a brief.
func (i issuer) IssuePassword(ctx context.Context, req *app.IssuePasswordRequest) (*app.IssuePasswordResponse, error) {
	if i.prefix != keys.PrefixDeployment {
		return nil, status.Error(codes.Unimplemented,
			"IssuePassword names somebody by a bare alias, which is one person only where there "+
				"is one tenant -- the control plane. A customer's person is given a password by "+
				"VouchService.Reset or Set, which run the escalation rule this does not")
	}

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

	// Hashed with `vouch.Hash`, so the argon2 parameters are the ones the check
	// later uses and not a second, weaker set.
	sum, err := vouch.Hash([]byte(secret))
	if err != nil {
		return nil, status.Error(codes.Internal, "the secret cannot be stored just now")
	}

	// Written without the escalation rule, on purpose, and through the generated
	// `Credential` verbs rather than `coreCredential.Set`. `Set` runs `mayReach`
	// -- *nobody writes a credential for a person who holds more than they do*
	// -- and here that would refuse a console key setting an operator's password
	// on the grounds that the key holds nothing, which is the whole act rather
	// than a hole. This is the deployment's own work about its own operators:
	// one tenant, reached only over the control plane, and `roster key add`
	// warns that a key that reaches here can become an operator. The old path
	// was `vouch.New(s, s)` carrying no `WithReach` for the same reason; the
	// generated verbs are what `server/core` leaves unguarded for exactly this
	// (see `core.Reaching`).
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()
	byKind := app.CredentialRef_builder{
		Kind: app.CredentialRefByKind_builder{Holder: ref, Kind: z.Ptr(vouch.KindPassword)}.Build(),
	}.Build()

	v, err := i.s.Credential().Get(ctx, app.CredentialGetRequest_builder{
		Ref:    byKind,
		Select: app.CredentialSelect_builder{DateUpdated: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		if _, err := i.s.Credential().Add(ctx, app.CredentialAddRequest_builder{
			Holder: ref,
			Kind:   vouch.KindPassword,
			Secret: sum,
		}.Build()); err != nil {
			return nil, err
		}

		return app.IssuePasswordResponse_builder{Password: secret}.Build(), nil
	}

	if _, err := i.s.Credential().Patch(ctx, app.CredentialPatchRequest_builder{
		Ref:            app.CredentialRef_builder{Id: v.GetId()}.Build(),
		Secret:         sum,
		Failures:       z.Ptr(int32(0)),
		DateLockedNull: z.Ptr(true),
		DateUpdated:    v.GetDateUpdated(),
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

	in := pdid.Id(t.Id)

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
