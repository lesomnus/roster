package cli

import (
	"context"
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
