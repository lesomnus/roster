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
	return issuer{s: s, db: db, v: vouch.New(s, s), prefix: keys.PrefixDeployment}
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
	return issuer{s: s, db: db, v: vouch.New(s, s), prefix: keys.PrefixTenant}
}

type issuer struct {
	app.UnimplementedIssueServiceServer

	s  app.Server
	db *ent.Client
	v  app.VouchServiceServer

	// prefix is which plane this instance mints for, and it is not in any
	// request: a caller that could name it could ask the customer-facing port
	// for a key of the deployment's own kind, and the prefix is exactly what
	// tells the two apart. See `server/keys`.
	prefix string
}

func (i issuer) IssueKey(ctx context.Context, req *app.IssueKeyRequest) (*app.IssueKeyResponse, error) {
	if req.GetAlias() == "" {
		return nil, status.Error(codes.InvalidArgument, "a name for the key")
	}
	if len(req.GetMethods()) == 0 {
		// Refused rather than defaulted in either direction. Everything hands
		// out more than was asked for; nothing mints a key that silently does
		// not work.
		return nil, status.Error(codes.InvalidArgument, "methods: a key that allows nothing opens no door")
	}

	ref, err := i.whose(ctx, req)
	if err != nil {
		return nil, err
	}

	// Which kind is a fact about which server answered, and not a field. See
	// [issuer.prefix].
	token, sum, err := keys.Mint(i.prefix)
	if err != nil {
		return nil, err
	}

	add := app.ApiKeyAddRequest_builder{
		Holder:  ref,
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

// IssuePassword is the control plane's alone, and refuses on the other.
//
// It names somebody by a bare alias, which only says one person where there is
// one tenant -- the control plane. On the data plane an alias names one person
// per customer, and this does not take a tenant to tell them apart: it would
// resolve against `holder`'s `Tenant.Query().First()`, an arbitrary tenant, and
// make the row if it were not there. So a data-plane caller holding this method
// could set -- and create -- a password on somebody in a tenant they never
// named, through the issuer's own `vouch.New(s, s)`, which carries no
// `WithReach`: the escalation rule that guards every other credential write is
// not in this path. `IssueKey` is safe there because `whose` refuses a bare
// name and reads the reference back through the wall; this took neither guard.
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
// whose is who the key is for, and it refuses a request that said twice or not
// at all.
//
// The two forms are not interchangeable and are not a convenience. `service` is
// an alias in the one tenant this plane has, and names a holder **into
// existence** if there is none -- which is right there and wrong here, because
// a customer's people are the customer's and a call that made one by mentioning
// them is a way to write rows into somebody else's tenant by typo.
//
// `holder` is a reference, so it carries a tenant and the wall narrows it. It
// is refused on the control plane rather than accepted, because the field means
// *this exists already* and there it might not.
//
// Both, or neither, is a caller that has not decided -- the refusal
// `vouch.refOf` makes about a person named two ways, for the same reason.
func (i issuer) whose(ctx context.Context, req *app.IssueKeyRequest) (*app.HolderRef, error) {
	service, ref := req.GetService(), req.GetHolder()

	byName := service != ""
	byRef := ref != nil

	switch {
	case byName && byRef:
		return nil, status.Error(codes.InvalidArgument,
			"a service and a holder name whose key this is two ways; give one")

	case i.prefix == keys.PrefixTenant:
		if !byRef {
			return nil, status.Error(codes.InvalidArgument,
				"holder: whose key this is; `service` is the other plane's, where there is one tenant")
		}

		// Read back through the walled server, so that a reference this caller
		// cannot see is a NotFound rather than a key minted into a tenant they
		// have no business in. `ApiKey.Add` would narrow it too; this is so the
		// refusal says which field.
		v, err := i.s.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref}.Build())
		if err != nil {
			return nil, err
		}

		return app.HolderRef_builder{Id: v.GetId()}.Build(), nil

	case byRef:
		return nil, status.Error(codes.InvalidArgument,
			"holder: this plane has one tenant, so a key is for a `service` by name")

	case !byName:
		return nil, status.Error(codes.InvalidArgument, "service: whose key this is")
	}

	who, err := i.holder(ctx, service)
	if err != nil {
		return nil, err
	}

	return app.HolderRef_builder{Id: who.Bytes()}.Build(), nil
}

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
