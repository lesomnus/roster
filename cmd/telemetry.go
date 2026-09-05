package cmd

import (
	"context"
	"log/slog"
	"runtime/debug"

	"github.com/lesomnus/otx"
	otlog "github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/config"
)

// telemetry builds what `otel:` describes and hands it to everything under
// `ctx`: the providers, and a logger that writes through them.
//
// It was declared and never built. `Config.Otel` had been in the file since
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
func telemetry(ctx context.Context, c *Config, name string) (context.Context, func(), error) {
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
