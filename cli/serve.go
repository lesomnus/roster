package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/xli"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"golang.org/x/sync/errgroup"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
)

// Ready is the databases, before anything is served on them.
//
// payday owns some of this app's schema, so a field added to a holder there
// arrives in `internal/ent` the next time this app generates -- and nothing
// about that is loud. It compiles, the tests pass against a database the tests
// just created, and the first sign of trouble is a column that is not there in
// the one handler that reads it.
//
// So one of two things happens here, and which one is the operator's to say.
// `db.migrate: true` hands the serving process the right to alter tables, which
// is right for development and is a thing to decide on purpose; anything else
// and the shapes have to agree already.
//
// **Both** planes, and the second is why this is a method rather than four
// lines in `serve`. Only the data plane was ever looked at: the control plane
// -- a second roster, on its own database, holding the keys every request is
// authenticated against -- was opened and served in whatever shape it was
// found. `control.db.migrate` was listed by `roster config env`, set by
// `compose.yaml`, promised by `docs/operating.md`, and read by nothing.
//
// What it cost is an upgrade past a release that adds a control-plane table --
// `session` is one -- on a deployment whose entrypoint skips `init` because the
// marker beside its databases says it is already seeded. The data plane
// migrates, the control plane does not, and the process starts and says
// nothing: the sweep logs the missing table and carries on, and the first
// report is an operator who cannot sign in. That is the quiet failure the
// check exists to turn into a refusal at startup.
//
// Both planes generate from the same `internal/ent`, so there is one set of
// tables to check against and no second answer to keep in step.
func Ready(ctx context.Context, s *cmd.Server, c cmd.Config) error {
	if err := ready(ctx, s, c.Db); err != nil {
		return err
	}
	if s.Control == nil {
		return nil
	}

	// Named, because the two databases are configured in two blocks and an
	// error saying a table is missing says nothing about which of them to go
	// and look at.
	if err := ready(ctx, s.Control, c.Control.Db); err != nil {
		return fmt.Errorf("control: %w", err)
	}

	return nil
}

// ready is [Server.Ready] for one plane, told what that plane's `db` block
// says.
//
// A method on the server rather than a function taking one, because what it
// needs is the three things `Build` worked out and kept -- the connection, the
// dialect it speaks and the client -- and re-deriving any of them from the
// configuration would be a second answer to a question already answered.
func ready(ctx context.Context, s *cmd.Server, c config.DbConfig) error {
	if c.Migrate {
		return entmigrate.NewSchema(s.Drv).Create(ctx)
	}

	return entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables)
}

// NewCmdServe is `<app> serve`.
//
// It is the app's own and not payday's, for the reason at the top of
// `cl/config.go`: the body of this command is the stack, and a framework that
// supplied it would be hiding the one thing a reader of an app most needs to
// see.
func NewCmdServe(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "answer requests",

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			ctx, stop, err := telemetry(ctx, c, "roster")
			if err != nil {
				return err
			}
			defer stop()

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			if err := Ready(ctx, s, *c); err != nil {
				return err
			}

			// What a file declared, before anything is served.
			//
			// After `Ready` because it writes rows and the tables have to be
			// there; before the listener because a caller that arrived between
			// the two would see a deployment half configured. A failure here
			// stops the process rather than being logged: a declared
			// `Connection` that did not apply is a directory nobody can sign in
			// through, and coming up anyway would make that somebody's morning
			// instead of this line.
			if rs, err := cmd.ReadResources(c.Resources); err != nil {
				return err
			} else if v, err := cmd.ApplyResources(ctx, s, rs, false); err != nil {
				return err
			} else if len(rs) > 0 {
				log.From(ctx).InfoContext(ctx, "resources",
					slog.Int("added", len(v.Added)), slog.Int("changed", len(v.Changed)), slog.Int("same", len(v.Same)))
			}

			l, err := net.Listen("tcp", c.Server.ListenAddr())
			if err != nil {
				return err
			}

			log.From(ctx).InfoContext(ctx, "grpc", slog.String("addr", l.Addr().String()))

			// The background work and the server, together: whichever stops
			// first stops the other. A loop that keeps running under a server
			// that is going down is a process that will not exit, and a server
			// that goes on answering after its outbox drain has died is one
			// that accepts writes it will never publish.
			g, ctx := errgroup.WithContext(ctx)
			g.Go(func() error { return spin.Run(ctx, slices.Values(s.Spin)) })
			g.Go(func() error { return s.Serve(ctx, *c, l) })

			// The consumers, when this deployment runs them itself rather than
			// in processes of their own. Named is a listener and empty is
			// nowhere; `cmd/consumers.go` says which a deployment should want.
			//
			// In the same errgroup as the server, which is the whole of what
			// "one process" costs and buys: whichever stops first stops the
			// others, so a front door that cannot come up is a start-up failure
			// rather than a deployment that is half there.
			if c.Account.Serves() {
				ac, err := frontDoor(c, l)
				if err != nil {
					return err
				}
				g.Go(func() error { return serveAccount(ctx, ac) })
			}
			if c.Ldap.Serves() {
				lc, err := directory(c, l)
				if err != nil {
					return err
				}
				g.Go(func() error { return serveLdap(ctx, lc) })
			}
			if c.Login.Serves() {
				gc, err := loginApp(c, l)
				if err != nil {
					return err
				}

				// **Nothing to front, so it stays off and the rest serves.**
				//
				// `minted` drops an operator whose key has not been written
				// yet, which on a first start is all of them: `roster login
				// provision` skips a tenant that does not exist, and the
				// tenant is made by `resources:` a moment from now, by this
				// process. Refusing here would be the deployment that cannot
				// come up because it has not come up -- which is what it was,
				// and what `deploy/` found on an empty cluster.
				//
				// The next start finds the key and fronts them. Until then
				// this says so, once, rather than a browser saying it.
				//
				// `roster login serve` still refuses: somebody typed that, and
				// a process whose only job is the Login App has nothing to do
				// without one.
				if len(gc.Clients) == 0 {
					slog.Warn("login: no operator has a key yet, so the login app is not serving; " +
						"make a tenant and restart")
				} else {
					g.Go(func() error { return serveLogin(ctx, gc) })
				}
			}

			return g.Wait()
		}),
	}
}

// Migrate brings the database s runs on into the shape this app's schema says.
//
// A function here rather than a method on `cmd.Server` because this is the call
// that links the migration engine -- ent's is Atlas, with its diff planner, its
// three dialects and the HCL parser those want -- and `cl` is what the sandbox
// imports. A sandbox does not migrate: there is no database there that outlives
// the page, so what it wants is `wasm/schema`, which is the same tables as a
// script.
func Migrate(ctx context.Context, s *cmd.Server) error {
	if err := entmigrate.NewSchema(s.Drv).Create(ctx); err != nil {
		return err
	}
	if s.Control == nil {
		return nil
	}

	// Both planes, named, because they are two databases configured in two
	// blocks and an error about a table says nothing about which of them to go
	// and look at.
	if err := entmigrate.NewSchema(s.Control.Drv).Create(ctx); err != nil {
		return fmt.Errorf("control: %w", err)
	}

	return nil
}

// frontDoor is `account:` with what only this process knows filled in.
//
// `roster` and `connect` default to the deployment's own listeners, because
// writing them again in the file that already says `server.addr` is one more
// place for two answers to drift. It is a default and not a shortcut: the call
// goes out on a socket, with a key, and comes back through the wall, exactly as
// it does from a process of its own.
//
// `l` rather than `c.Server.ListenAddr()` because a configuration may name port
// 0 and a listener knows what it got.
func frontDoor(c *cmd.Config, l net.Listener) (cmd.AccountConfig, error) {
	ac := c.Account
	if ac.Roster == "" {
		ac.Roster = l.Addr().String()
	}
	if ac.Connect == "" {
		if c.Server.Http.Addr == "" {
			return ac, errors.New("account.connect: the page's calls go out over HTTP and this deployment serves none; name server.http.addr, or account.connect")
		}

		// http rather than https: this is a dial to a listener of this same
		// process, and a deployment that terminates TLS in front of itself is
		// not terminating it here.
		ac.Connect = "http://" + c.Server.Http.Addr
	}

	keys, err := keysOf(c.Account.Keys, AccountKeyPrefix, nil)
	if err != nil {
		return ac, err
	}
	ac.Keys = keys

	return ac, nil
}

// loginApp is `login:` with the same default, for the same reason -- and with
// each operator's two halves checked against each other, because half of one
// runs and answers nothing.
func loginApp(c *cmd.Config, l net.Listener) (cmd.LoginConfig, error) {
	gc := c.Login
	if gc.Roster == "" {
		gc.Roster = l.Addr().String()
	}

	clients, err := clientsOf(gc.Clients, LoginClientPrefix, nil)
	if err != nil {
		return gc, err
	}
	// Before `keysOf`, which reads the files -- and before it refuses an empty
	// set, which on a first start is what is left.
	refs, clients := minted(gc.Keys, clients)
	if len(refs) == 0 && len(clients) == 0 {
		gc.Keys, gc.Clients = nil, nil

		return gc, nil
	}

	keys, err := keysOf(refs, LoginKeyPrefix, nil)
	if err != nil {
		return gc, err
	}
	gc.Keys, gc.Clients = keys, clients

	return gc, whole(keys, clients)
}

// directory is `ldap:` with the same default, for the same reason.
func directory(c *cmd.Config, l net.Listener) (cmd.LdapConfig, error) {
	lc := c.Ldap
	if lc.Roster == "" {
		lc.Roster = l.Addr().String()
	}

	keys, err := keysOf(c.Ldap.Keys, LdapKeyPrefix, nil)
	if err != nil {
		return lc, err
	}
	lc.Keys = keys

	return lc, nil
}
