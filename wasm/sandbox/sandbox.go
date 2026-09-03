// Package sandbox is what the two instances in the page share: who the caller
// is, when a message port cannot say.
//
// The real console signs in and holds a cookie; every call after carries it,
// and `authsession` reads it back into a caller. A message port has no cookie
// jar, so over it every call after the sign-in arrives naming nobody -- and
// `auth.Plain`, which the instances serve with, believes what a caller writes
// and refuses one that writes nothing. The page writes nothing on purpose: it
// is transport-blind, and a header it added only in the sandbox would be code
// that runs nowhere else.
//
// So the instance remembers instead. A sign-in that `console.Auth` accepted --
// the same `vouch`, a wrong password refused -- names an operator, and until a
// sign-out every call that names nobody is taken to be them. That is the whole
// of what a cookie would have done, minus the browser, and it keeps the two
// halves of the sandbox honest with each other: the form refuses what the
// server refuses, and what follows is the person the form accepted.
//
// It is not authentication and must not be mistaken for it: there is one
// caller in the page, and this is a note of who they said they were.
package sandbox

import (
	"context"
	"errors"
	"log"
	"sync"

	"google.golang.org/grpc/metadata"

	pdauth "github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"

	"github.com/lesomnus/roster/internal/ent"
	app "github.com/lesomnus/roster/rstr"
)

// Operator is who the page is taken to be: a written name in the form
// `auth.Plain` reads (`@tenant/alias`), or nobody.
type Operator struct {
	mu   sync.Mutex
	name string
}

// Set makes them the caller; empty makes it nobody.
func (o *Operator) Set(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.name = name
}

// Name is who they are, or empty.
func (o *Operator) Name() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.name
}

// Believe is `auth.Plain`, and the remembered operator where a call writes
// nothing. A call that does write a name is believed as written, so a test or
// a hand-made call can still be anybody.
func Believe(o *Operator) pdauth.Handler {
	plain := pdauth.Plain()

	return pdauth.HandlerFunc(func(ctx context.Context) (pdauth.Identity, error) {
		id, err := plain.Handle(ctx)
		if !errors.Is(err, pdauth.ErrNoCredential) {
			return id, err
		}
		name := o.Name()
		if name == "" {
			return id, err
		}

		md, _ := metadata.FromIncomingContext(ctx)
		md = md.Copy()
		md.Set("authorization", "Plain "+name)

		return plain.Handle(metadata.NewIncomingContext(ctx, md))
	})
}

// Auth is the console's `AuthService` with the memory attached: a sign-in it
// accepts is remembered, a sign-out forgets.
//
// The tenant is the deployment's own -- the one `roster init` wrote, which is
// what `console.Auth` signs in against as well.
func Auth(inner app.AuthServiceServer, db *ent.Client, o *Operator) app.AuthServiceServer {
	return remembering{AuthServiceServer: inner, db: db, o: o}
}

type remembering struct {
	app.AuthServiceServer
	db *ent.Client
	o  *Operator
}

func (a remembering) SignIn(ctx context.Context, req *app.AuthSignInRequest) (*app.AuthSignInResponse, error) {
	res, err := a.AuthServiceServer.SignIn(ctx, req)
	if err != nil {
		return nil, err
	}

	name, err := a.name(ctx, req.GetAlias())
	if err != nil {
		return nil, err
	}
	a.o.Set(name)

	return res, nil
}

func (a remembering) SignOut(ctx context.Context, req *app.AuthSignOutRequest) (*app.AuthSignOutResponse, error) {
	a.o.Set("")

	return a.AuthServiceServer.SignOut(ctx, req)
}

// name is `@tenant/alias` for an operator, as `auth.Plain` reads it.
func (a remembering) name(ctx context.Context, alias string) (string, error) {
	t, err := a.db.Tenant.Query().First(ctx)
	if err != nil {
		return "", err
	}

	return "@" + t.Alias + "/" + alias, nil
}

// Own is the name of the operator `roster init` wrote, for an instance that
// has no sign-in of its own to remember -- the admin instance, which the page
// reaches after signing in on the other one.
func Own(ctx context.Context, db *ent.Client, alias string) (string, error) {
	return remembering{db: db}.name(ctx, alias)
}

// Resolver is `inner`, saying on the instance's own log why a caller could not
// be resolved. The interceptor answers the page with a status code and writes
// the reason to the request's logger, and a message port's request has none:
// what the page then shows is "could not say who is calling" and nowhere to
// look. Here it is the worker's console.
func Resolver(inner pdauth.Resolver) pdauth.Resolver {
	return pdauth.ResolverFunc(func(ctx context.Context, id pdauth.Identity) (*frame.Frame, error) {
		f, err := inner.Resolve(ctx, id)
		if err != nil {
			log.Printf("sandbox: cannot resolve %q: %v", id.Name(), err)
		}

		return f, err
	})
}
