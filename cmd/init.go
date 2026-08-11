package cmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// NewCmdInit is `roster init`: the first tenant, the first person, and the
// first thing either of them may do.
//
// It exists because there is nowhere else it could happen. A tenant is not put
// up from inside one, so the first row of a deployment cannot arrive over the
// API. What puts it there is [Server.Ungated], which is not a privilege anybody
// holds: it is a server instance this process was handed, reachable from this
// command and from nowhere a request can get to.
//
// # It grants, and it used to only create
//
// This wrote a tenant and a holder, printed "sign in as @acme/admin", and
// stopped. Permissions are deny-by-default, so what it printed was somebody who
// could call exactly one method -- `MeService.Get`, which would tell them they
// held nothing.
//
// There was no way out either. Writing the first role needs `RoleService/Add`,
// which no binding allowed, and there is no other door: `mayGrant` is waived
// only where there is no frame at all, which is `Ungated`, which is here. So a
// fresh deployment answered nothing and nothing could change that. It went
// unnoticed because this command had no test.
//
// So it binds a role, and the role is [Role.every_method] rather than a list.
// A list written here is a snapshot: the next release adds an RPC the first
// administrator cannot call, and cannot grant themselves either, because
// granting is refused for anything the granter does not already hold.
//
// # Running it twice
//
// An error, because an alias is unique and the database says so. That is the
// right answer -- an `init` that quietly did nothing is one somebody runs
// against the wrong deployment and believes.
func NewCmdInit(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "init",
		Brief: "put up the first tenant, somebody in it, and what they may do",

		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the alias of the tenant to create"},
			&flg.String{Name: "holder", Brief: "the alias of the holder to create in it"},
			&flg.String{Name: "operator", Brief: "the alias of the control plane holder who runs the console"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			tenant, ok := flg.Find[string](cmd, "tenant")
			if !ok || tenant == "" {
				tenant = "acme"
			}

			holder, ok := flg.Find[string](cmd, "holder")
			if !ok || holder == "" {
				holder = "admin"
			}

			operator, ok := flg.Find[string](cmd, "operator")
			if !ok || operator == "" {
				operator = "ops"
			}

			s, err := Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// The schema, so that a fresh database is one this can run against.
			// A deployment with migrations of its own does that instead; see
			// payday's `migrate`.
			if err := s.Ent.Schema.Create(ctx); err != nil {
				return err
			}

			t, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{
				Alias: tenant,
			}.Build())
			if err != nil {
				return fmt.Errorf("tenant %q: %w", tenant, err)
			}

			k, err := pdid.From(t.GetId())
			if err != nil {
				return err
			}

			h, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: t.GetId()}.Build(),
				Alias:  holder,
			}.Build())
			if err != nil {
				return fmt.Errorf("holder %q: %w", holder, err)
			}

			j, err := pdid.From(h.GetId())
			if err != nil {
				return err
			}

			if err := allow(ctx, s, k, j); err != nil {
				return fmt.Errorf("the first binding: %w", err)
			}

			cmd.Printf("tenant %s is %s\n", tenant, k)
			cmd.Printf("holder %s is %s\n", holder, j)
			cmd.Printf("  bound to role %q -- **every method there is**, now and after an upgrade\n", everything)
			cmd.Printf("\nsign in as: @%s/%s\n", tenant, holder)

			if s.Control == nil {
				cmd.Printf("\nno control plane is configured, so this deployment believes its callers.\n")
				cmd.Printf("see docs/OPERATING.md before serving it anywhere.\n")

				return nil
			}

			who, secret, err := seedOperator(ctx, s.Control, operator)
			if err != nil {
				return fmt.Errorf("operator %q: %w", operator, err)
			}

			cmd.Printf("\ncontrol plane\n")
			cmd.Printf("  holder %s is %s\n", operator, who)
			cmd.Printf("  bound to role %q -- **every method there is**\n", everything)
			cmd.Printf("  password  %s\n", secret)
			cmd.Printf("\nthat password is shown once and is not stored. write it down now.\n")

			return nil
		}),
	}
}

// everything is what the first role is called, on both planes.
//
// A name somebody will read in a list of roles and understand without opening
// it, which matters more here than anywhere else: it is the role that explains
// why somebody could do something.
const everything = "everything"

// allow writes the role that says everything and binds it.
//
// Through `Ungated`, where there is no frame, so `mayGrantEverything` waives
// itself -- which is the only place it ever does. Every later grant of this
// descends from somebody who already held it.
func allow(ctx context.Context, s *Server, in pdid.Id, to pdid.Id) error {
	r, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:  everything,
		Desc:   "Every RPC there is, including ones added by a later release.",

		// Not a list. See `Role.every_method` for why a snapshot of the
		// methods that exist today is the wrong thing to write here.
		EveryMethod: true,
	}.Build())
	if err != nil {
		return err
	}

	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: to.Bytes()}.Build(),
	}.Build())

	return err
}

// seedOperator is the person who signs in to the console, and the password they
// do it with.
//
// # Why the control plane
//
// Because what a console does first is put up a tenant, and that is not
// something anybody inside a tenant does. The control plane is the deployment's
// own roster -- its holders are the callers rather than the customers -- and an
// operator is a caller. See PLAN.md, D15.
//
// # Why a password and not a key
//
// A key is for a machine and travels on every call. This is a person at a
// browser, and what a browser carries is a session cookie the console sets
// after checking a secret; see `payday/auth/authsession` and
// `docs/guide/signing-in.md`. `VouchService` is what checks it, on the control
// plane's own instance.
//
// # Why it is generated and not typed
//
// There is no `--password` flag on purpose. A secret on a command line is in
// the shell history and in the process list, and `roster key add` already
// refuses to take a key for the same reason. What this prints is shown once and
// stored as an argon2id hash, so the deployment cannot tell anybody what it was
// any more than it can tell them their key.
func seedOperator(ctx context.Context, s *Server, alias string) (pdid.Id, string, error) {
	if err := s.Ent.Schema.Create(ctx); err != nil {
		return pdid.Nil, "", err
	}

	// The same owner tenant `roster key add` uses, made here if this is a fresh
	// control plane. There is nothing to choose: a control plane has one owner.
	who, err := serviceOf(ctx, s, alias)
	if err != nil {
		return pdid.Nil, "", err
	}

	t, err := s.Ent.Tenant.Query().First(ctx)
	if err != nil {
		return pdid.Nil, "", err
	}
	if err := allow(ctx, s, pdid.Id(t.ID), who); err != nil {
		return pdid.Nil, "", err
	}

	secret, err := passphrase()
	if err != nil {
		return pdid.Nil, "", err
	}

	// Hashed by the service that will later check it, so the argon2 parameters
	// are in one place. A hash computed here would be a second set of them, and
	// the weaker of the two is the one that matters.
	if _, err := vouch.New(s.Ungated, s.Ungated).Set(ctx, app.VouchSetRequest_builder{
		Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
		Secret: []byte(secret),
	}.Build()); err != nil {
		return pdid.Nil, "", err
	}

	return who, secret, nil
}

// passphrase is 32 bytes from `crypto/rand`, printable.
//
// Long enough that it is not guessed and not a word anybody will recognise,
// because the one thing it must not be is something somebody keeps. It is for
// the first sign-in, and the console's job is to make them change it.
func passphrase() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}
