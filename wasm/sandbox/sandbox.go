// Package sandbox is what the servers in a page share: the two things a
// message port cannot carry, and nothing else.
//
// # Who the caller is
//
// A real console signs in and holds a cookie; every call after carries it, and
// `authsession` reads it back into a caller. A message port has no cookie jar,
// so over it every call after the sign-in arrives naming nobody -- and
// `auth.Plain`, which these servers serve with, believes what a caller writes
// and refuses one that writes nothing. The page writes nothing on purpose: it
// is transport-blind, and a header it added only in the sandbox would be code
// that runs nowhere else.
//
// So the instance remembers instead. A sign-in that `console.Auth` accepted --
// the same `vouch`, a wrong password refused -- names somebody, and until a
// sign-out every call that names nobody is taken to be them. That is the whole
// of what a cookie would have done, minus the browser, and it keeps the two
// halves of the sandbox honest with each other: the form refuses what the
// server refuses, and what follows is the person the form accepted.
//
// It is not authentication and must not be mistaken for it: there is one
// caller in the page, and this is a note of who they said they were.
//
// # Which name the page arrived at
//
// The second thing, and it is the **user console**'s (#34). The data plane has
// many tenants and a sign-in is about one of them, so `cmd.Hosted` reads the
// name the request came in on and finds the tenant whose `Host` row claims it.
// A message port carries no `Host` either, and again the page must not be the
// one to say -- a field it filled in only here is a field that is wrong
// everywhere else.
//
// [ArrivedAt] is that half: the sandbox writes the name, and `cmd.Hosted`
// resolves it through the same rows as ever. What is faked is one string; the
// lookup, the refusal for a name nothing claims, and the tenant it answers with
// are the deployment's own.
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

// Caller is who the page is taken to be: a written name in the form
// `auth.Plain` reads (`@tenant/alias`), or nobody.
//
// Not *operator*, which it was while there was one page: the admin console's
// caller is a roster operator and the user console's is a roster user, and the
// note this keeps is the same note either way.
type Caller struct {
	mu   sync.Mutex
	name string
}

// Set makes them the caller; empty makes it nobody.
func (o *Caller) Set(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.name = name
}

// Name is who they are, or empty.
func (o *Caller) Name() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.name
}

// Believe is `auth.Plain`, and the remembered caller where a call writes
// nothing. A call that does write a name is believed as written, so a test or
// a hand-made call can still be anybody.
func Believe(o *Caller) pdauth.Handler {
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

// Auth is an `AuthService` with the memory attached: a sign-in it accepts is
// remembered, a sign-out forgets.
//
// `tenant` is which one the sign-in was about, and it is **the same function
// the server behind this was built with** -- [TheOneTenant] on the control
// plane, [ArrivedAt] over `cmd.Hosted` on the data plane. Handed over rather
// than worked out again, because the name this remembers has to be the tenant
// the password was actually checked against: a second answer here would
// remember somebody who is not who signed in, on the plane where there is more
// than one tenant to be wrong about.
func Auth(inner app.AuthServiceServer, tenant func(ctx context.Context) (string, error), o *Caller) app.AuthServiceServer {
	return remembering{AuthServiceServer: inner, tenant: tenant, o: o}
}

// TheOneTenant is the control plane's answer: it has exactly one, so nothing
// has to say which. The same question `console.Auth` answers the same way when
// it is built with no `WithTenant`.
func TheOneTenant(db *ent.Client) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		v, err := db.Tenant.Query().First(ctx)
		if err != nil {
			return "", err
		}

		return v.Alias, nil
	}
}

// ArrivedAt writes the name a browser would have arrived at and hands the
// request on to `inner`, which is `cmd.Hosted`.
//
// The one string a message port cannot carry. Written into the metadata rather
// than answered directly so that what resolves it is the deployment's own
// lookup: the `Host` row, the tenant it names, and the refusal for a name
// nothing claims. A sandbox whose seed forgot the row finds out here, which is
// where a deployment finds out too.
//
// Only where nothing was said. A call that carries a name -- a test, a
// hand-made request -- keeps it, for the reason `Believe` believes a written
// caller.
func ArrivedAt(name string, inner func(ctx context.Context) (string, error)) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			md = metadata.MD{}
		} else {
			md = md.Copy()
		}
		if len(md.Get("x-forwarded-host")) == 0 && len(md.Get("host")) == 0 && len(md.Get(":authority")) == 0 {
			md.Set("x-forwarded-host", name)
		}

		return inner(metadata.NewIncomingContext(ctx, md))
	}
}

type remembering struct {
	app.AuthServiceServer
	tenant func(ctx context.Context) (string, error)
	o      *Caller
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

// name is `@tenant/alias`, as `auth.Plain` reads it.
func (a remembering) name(ctx context.Context, alias string) (string, error) {
	t, err := a.tenant(ctx)
	if err != nil {
		return "", err
	}

	return "@" + t + "/" + alias, nil
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
