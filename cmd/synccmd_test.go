package cmd_test

import (
	"bufio"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// TestSyncWatchFromATerminal is the one stream a person can hold: the same
// subscription an app makes, printed a line at a time.
//
// The poke loop is `cmd/sync_test.go`'s trick for the same reason: there is no
// snapshot, so nothing says the server has got as far as subscribing, and
// `Invalidate` moves a stamp forward every time -- a poke idempotence cannot
// suppress.
func TestSyncWatchFromATerminal(t *testing.T) {
	x := require.New(t)

	b := cliUp(t, "/roster.SyncService/Watch", "/roster.HolderService/Invalidate")

	wctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	pr, pw := io.Pipe()
	k := root(t, &b.Hers)
	k.Writer = pw

	done := make(chan error, 1)
	go func() {
		done <- k.Run(wctx, []string{"sync", "watch"})
		pw.Close()
	}()

	lines := make(chan string, 32)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	got := ""
	for range 100 {
		_, err := cliRun(t, &b.Hers, "holder", "invalidate", "@newco/bob")
		x.NoError(err)

		select {
		case got = <-lines:
		case <-time.After(50 * time.Millisecond):
			continue
		}

		break
	}
	x.NotEmpty(got, "the stream never subscribed")

	bob, _ := pdid.From(b.Bob.GetId())
	tn, _ := pdid.From(b.Tenant.GetId())
	x.Contains(got, bob.String(), "an event about somebody else")
	x.Contains(got, tn.String(), "the tenant is part of the line")
	x.Contains(got, "invalidated=", "the stamp is the message and it is not on the line")

	// A cancelled context is the one ending somebody asked for, so the command
	// finishes rather than reporting a gap.
	cancel()
	x.NoError(<-done)

	t.Run("and locally it is a refusal, since events are the serving stack's", func(t *testing.T) {
		x := require.New(t)

		_, err := cliRun(t, &b.Local, "sync", "watch")
		x.Error(err)
		x.ErrorContains(err, "client.addr")
	})
}
