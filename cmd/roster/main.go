// Command roster is a payday app.
//
// What to read first is `cmd/serve.go`: it is the stack, written out, and it is
// the whole of what this app says to stand up a served, migrated, walled
// server.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lesomnus/roster/cmd"
)

func main() {
	// Both of the signals a process is asked to stop with, and the second is the
	// one this app is actually stopped with.
	//
	// `os.Interrupt` alone is Ctrl-C at a terminal. `docker stop` and every
	// orchestrator send SIGTERM, and the image in `docker/` runs
	// `exec roster serve`, which makes roster PID 1 -- where SIGTERM has no
	// default handler at all. So every `docker stop` sat out the grace period
	// and ended in SIGKILL, and anywhere that is not PID 1 the default
	// disposition killed the process on the spot. Either way the graceful path
	// -- `GracefulStop`, the spin teardown, the deferred stops for the other
	// listeners -- was written, wired, and never once executed in the way this
	// app is actually deployed.
	//
	// What it cost is that a routine restart was a crash: with `watch.outbox`
	// off, which is the default, an event committed and not yet published is
	// lost on every one of them, in the window `docs/OPERATING.md` describes as
	// the outbox's reason to exist.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var c cmd.Config
	if err := cmd.Cmd(&c).Run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "roster:", err)
		os.Exit(1)
	}
}
