package cmd_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestACallerWhoLeftIsNoFaultOfTheStore: a call whose own context has ended
// while its credential is read is answered as the context's, on every road a
// credential is read by; one whose caller hung up leaves no line of roster's,
// and one whose deadline passed keeps the line that names the store. A store
// that cannot be asked, by a caller who is still there, is `Unavailable` with
// that line at ERROR, as before.
//
// # What it was
//
// An app that asks two things side by side and drops the one it no longer
// needs cancelled its call while roster was reading the key it came with.
// SQLite said `interrupted`, and roster logged `keys: could not ask whether a
// credential is good` at ERROR and answered `Unavailable`: a store that had
// failed, where nothing had. Right is `Canceled` where the caller cancelled
// and `DeadlineExceeded` where its deadline passed, the same for a key, a
// delegation and a key narrowed by `roster-at`.
//
// The context is ended before the call reaches the store, which is what makes
// this deterministic: what is held is how a failed read is answered once the
// caller has gone, whatever the store made of it.
func TestACallerWhoLeftIsNoFaultOfTheStore(t *testing.T) {
	b := keyFor(t, app.HolderService_Get_FullMethodName)
	door := auth.InterceptorUnary(b.Auth, cmd.Resolver(b.Ungated, b.Control.Ungated), cmd.Public)

	madeUp := keys.PrefixDeployment + strings.Repeat("A", 43)
	delegation := keys.PrefixDelegation + strings.Repeat("A", 43)

	// Each road a credential is read by, and what a call that ended is
	// answered with there.
	roads := map[string][]string{
		"a key":                       {"authorization", "Bearer " + madeUp},
		"a delegation":                {"authorization", "Bearer " + madeUp, keys.HeaderActing, delegation},
		"a key narrowed by roster-at": {"authorization", "Bearer " + madeUp, keys.HeaderAt, "@contoso"},
	}

	// ask is the door asked with `ctx`, and what it said in the log.
	ask := func(t *testing.T, ctx context.Context, md []string) (error, *heard) {
		t.Helper()
		said := &heard{}
		in := metadata.NewIncomingContext(log.Into(ctx, slog.New(slog.NewJSONHandler(said, nil))), metadata.Pairs(md...))
		_, err := door(in, nil, &grpc.UnaryServerInfo{FullMethod: app.HolderService_Get_FullMethodName},
			func(context.Context, any) (any, error) {
				t.Fatal("a call nobody could say was good was served")
				return nil, nil
			})

		return err, said
	}
	errors := func(said *heard) []map[string]any { return said.lines(t, `"level":"ERROR"`) }

	for name, road := range roads {
		t.Run(name, func(t *testing.T) {
			for why, end := range map[string]struct {
				ctx  func() context.Context
				code codes.Code
				said bool // the line that names the store is written
			}{
				"cancelled": {func() context.Context {
					ctx, cancel := context.WithCancel(t.Context())
					cancel()
					return ctx
				}, codes.Canceled, false},
				"past its deadline": {func() context.Context {
					ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
					t.Cleanup(cancel)
					return ctx
				}, codes.DeadlineExceeded, true},
			} {
				t.Run(why, func(t *testing.T) {
					x := require.New(t)

					err, said := ask(t, end.ctx(), road)
					x.Equal(end.code, status.Code(err), "%v", err)
					if !end.said {
						x.Empty(errors(said), "a caller who left was logged as roster's fault: %s", said.b.String())
						return
					}

					// A read that did not come back in time is a slow store,
					// and this is the line that says so: one, roster's.
					lines := errors(said)
					x.Len(lines, 1, "%s", said.b.String())
					x.Equal("keys: could not ask whether a credential is good", lines[0]["msg"])
				})
			}
		})
	}

	// And a store that really cannot be asked, by a caller who is still
	// there, is what it was.
	require.NoError(t, b.Control.Drv.Close())
	for name, road := range roads {
		t.Run(name+", the store away", func(t *testing.T) {
			x := require.New(t)

			err, said := ask(t, t.Context(), road)
			x.Equal(codes.Unavailable, status.Code(err), "%v", err)
			x.NotContains(status.Convert(err).Message(), "closed", "the store's words were said to a stranger")
			x.NotEmpty(errors(said), "the log does not say what the store said: %s", said.b.String())
		})
	}
}
