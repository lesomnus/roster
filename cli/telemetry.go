package cli

import (
	"context"
	"fmt"
	"github.com/lesomnus/roster/cmd"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"

	// The exporter a collector is reached with. mkot knows an exporter by the
	// name a package registers, and without this one an `otel:` naming `otlp`
	// is refused as an unknown type when the configuration is read: what is
	// left is `pretty`, and so stderr, in a cluster whose apps send to a
	// collector on the node (#214). Loft and levyan link the same module at
	// the same version.
	//
	// Here and not in `cmd`, for the reason a database driver is: it is a
	// client for something only a process reaches, and the Wasm sandbox
	// imports `cmd`.
	_ "github.com/lesomnus/mkot/otlp"

	"github.com/lesomnus/otx"
	otlog "github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/config"
)

// telemetry builds what `otel:` describes and hands it to everything under
// `ctx`: the providers, and a logger that writes through them.
//
// It was declared and never built. `cmd.Config.Otel` had been in the file since
// the template, `grpcx.Serving` had installed the request logger and the
// tracer on every server from the start -- and both read the `otx` off the
// context, which nothing had put there, so `otx.From` fell back to the
// OpenTelemetry globals and every record went to a provider that drops them.
// A day at the sandbox asking where the server's log was is how that was
// found. Now `roster serve`, `roster account serve` and `roster ldap serve`
// each build one, and a deployment that wrote no `otel:` gets the defaults:
// one line per call, pretty-printed on stderr.
//
// The version is the build's, read the way `roster version` reads it, so a
// record says which binary wrote it.
func telemetry(ctx context.Context, c *cmd.Config, name string) (context.Context, func(), error) {
	svc := config.Service{Name: name, Scope: "github.com/lesomnus/roster"}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		svc.Version = info.Main.Version
	}

	ctx, o, err := c.Otel.Build(ctx, svc)
	if err != nil {
		return nil, nil, err
	}
	ctx = otlog.Into(ctx, slog.New(o.SlogHandler()))
	if err := otx.Start(ctx); err != nil {
		return nil, nil, err
	}

	return ctx, func() { _ = o.Shutdown(context.WithoutCancel(ctx)) }, nil
}

// aloud hands everything under `ctx` a logger that prints, for a command at
// the box whose work says what it did in the log: every line of it on stderr
// as it is logged, and the lines that carry an error kept count of.
//
// [telemetry] is what a process that serves builds, and no other command
// builds one: `log.From` on a bare context writes to a provider that drops
// every record. So work that logs rather than fails -- payday's pass, which
// is written for a loop, and the payment of an erasure's archive half -- did
// what it did at the box and said it to nobody, and the command above it
// answered exit 0 whatever had come of it (#280).
//
// Stderr, and not what `otel:` describes. A command is run by somebody
// looking at a terminal, or by a schedule that keeps what a job printed; a
// deployment whose `otel:` names a collector would send the one line that
// says a pass failed somewhere the person who ran it is not looking.
//
// # What counts as having failed
//
// A line that carries an `err` is the work saying what it tried and could
// not, which is the error it would have returned had it been written to
// return one: every such line in payday's `trail` is. A warning with none --
// a chunk the archive's account does not have, left alone -- is something it
// found and went on from, which is said and is no failure of the command's;
// `roster trail verify` is what fails for those.
func aloud(ctx context.Context) (context.Context, *said) {
	v := &said{}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})

	return otlog.Into(ctx, slog.New(saying{Handler: h, said: v})), v
}

// said is what a command's work logged that it could not do.
type said struct {
	mu    sync.Mutex
	n     int
	first string
}

// failed is the error of a command whose work handed back no result, when
// what it logged says it could not do all of it: how much, and the first.
// The lines themselves are already on stderr.
func (v *said) failed(what string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.n == 0 {
		return nil
	}

	return fmt.Errorf("%s could not do %d thing(s) it set out to, each said above; the first: %s", what, v.n, v.first)
}

// saying is the handler [aloud] prints through.
type saying struct {
	slog.Handler
	said *said
}

func (h saying) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key != "err" {
				return true
			}

			h.said.mu.Lock()
			defer h.said.mu.Unlock()

			h.said.n++
			if h.said.first == "" {
				h.said.first = fmt.Sprintf("%s: %s", r.Message, a.Value)
			}

			return false
		})
	}

	return h.Handler.Handle(ctx, r)
}

func (h saying) WithAttrs(as []slog.Attr) slog.Handler {
	return saying{Handler: h.Handler.WithAttrs(as), said: h.said}
}

func (h saying) WithGroup(name string) slog.Handler {
	return saying{Handler: h.Handler.WithGroup(name), said: h.said}
}
