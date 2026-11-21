package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"

	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// newCmdSync is `roster sync`: the stream an app holds so it can stop trusting
// what it was told, held by a person instead.
//
// What an operator wants it for is watching a stop land: `holder disable` in
// one shell and this in another, saying that every app on the stream has been
// told. The request takes nothing because the wall is the whole narrowing --
// `sync.proto` argues that at length -- so there are no flags here either.
func newCmdSync(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "sync",
		Brief: "the stream that says somebody's standing, or what they may call, stopped being what an app was told",

		Commands: xli.Commands{
			newCmdSyncWatch(c),
		},
	}
}

// newCmdSyncWatch prints one line per event: who, whose tenant, the word for
// what moved, and the timestamps that are the actual message.
//
// # It fails when the stream ends
//
// The same stance `pdcmd`'s `watch` takes, for the same reason: a watch has no
// backlog, so a stream that stopped and a stream where nothing is happening
// look exactly alike, and a command that returned quietly would be somebody
// reading an empty screen and believing it. What it fails with is what the
// server ended the stream with ([ended]).
func newCmdSyncWatch(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "watch",
		Brief: "hear it, one line per event, until interrupted",

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			if c.Client.Local || c.Client.Addr == "" {
				return errors.New(
					"`sync watch` hears a served deployment's stream, and a local run has none: " +
						"events are published by the serving stack, not by the rows. name client.addr")
			}

			s, err := app.NewSyncServiceClient(pdcmd.MustConn(ctx)).Watch(ctx,
				app.SyncWatchRequest_builder{}.Build())
			if err != nil {
				return err
			}

			for {
				v, err := s.Recv()
				if err != nil {
					// A signal or a cancelled context is the one ending
					// somebody asked for, and the only one that is not a gap.
					if ctx.Err() != nil {
						return next(ctx)
					}

					return ended(err)
				}

				h, _ := pdid.From(v.GetHolder())
				t, _ := pdid.From(v.GetTenant())
				cl.Printf("%s\t%s\t%s", h, t,
					strings.ToLower(strings.TrimPrefix(v.GetReason().String(), "SYNC_REASON_")))

				// Only the stamps that are set, so the line reads as the fact
				// it is: nothing after the reason is somebody in good standing.
				if w := v.GetDateInvalidated(); w != nil {
					cl.Printf("\tinvalidated=%s", w.AsTime().Format(time.RFC3339))
				}
				if w := v.GetDateDisabled(); w != nil {
					cl.Printf("\tdisabled=%s", w.AsTime().Format(time.RFC3339))
				}
				if w := v.GetDateErased(); w != nil {
					cl.Printf("\terased=%s", w.AsTime().Format(time.RFC3339))
				}
				if w := v.GetDateClosed(); w != nil {
					cl.Printf("\tclosed=%s", w.AsTime().Format(time.RFC3339))
				}
				cl.Printf("\n")
			}
		}),
	}
}

// ended is what `sync watch` says of a stream the server, or the connection,
// ended: its code and its words, and *run it again* only where running it
// again mends it. A connection lost (`Unavailable`), a stream cut for falling
// behind or for a broker that lost its connection (`ResourceExhausted`), and a
// stream roster ended to have its caller ask again (`Unauthenticated`, whose
// words end in *reconnect*: `stream.recheck`, a credential or a policy that
// changed) are mended by it. A refusal, a deployment with no broker and a key
// that is not one are not, and are the server's words and nothing more.
//
// A stream that ended with no error at all is said as one that is mended by
// running it again, which is what this said of every ending before: roster
// ends none that way.
func ended(err error) error {
	if errors.Is(err, io.EOF) {
		return errors.New("the stream ended, and a watch has no backlog: " +
			"whatever changed after this was sent to nobody. Run it again")
	}

	v := status.Convert(err)
	again := false
	switch v.Code() {
	case codes.Unavailable, codes.ResourceExhausted:
		again = true
	case codes.Unauthenticated:
		again = strings.HasSuffix(v.Message(), "reconnect")
	}
	if !again {
		return fmt.Errorf("the stream ended, %s: %s", v.Code(), v.Message())
	}

	return fmt.Errorf("the stream ended, %s: %s. A watch has no backlog: "+
		"whatever changed after this was sent to nobody. Run it again", v.Code(), v.Message())
}
