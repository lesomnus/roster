package cmd

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lesomnus/xli"

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
func newCmdSync(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "sync",
		Brief: "the stream that says somebody's standing stopped being good",

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
// reading an empty screen and believing it.
func newCmdSyncWatch(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "watch",
		Brief: "hear it, one line per event, until interrupted",

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
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

					return errors.New("the stream ended, and a watch has no backlog: " +
						"whatever changed after this was sent to nobody. Run it again")
				}

				h, _ := pdid.From(v.GetHolder())
				t, _ := pdid.From(v.GetTenant())
				cmd.Printf("%s\t%s\t%s", h, t,
					strings.ToLower(strings.TrimPrefix(v.GetReason().String(), "SYNC_REASON_")))

				// Only the stamps that are set, so the line reads as the fact
				// it is: nothing after the reason is somebody in good standing.
				if w := v.GetDateInvalidated(); w != nil {
					cmd.Printf("\tinvalidated=%s", w.AsTime().Format(time.RFC3339))
				}
				if w := v.GetDateDisabled(); w != nil {
					cmd.Printf("\tdisabled=%s", w.AsTime().Format(time.RFC3339))
				}
				if w := v.GetDateErased(); w != nil {
					cmd.Printf("\terased=%s", w.AsTime().Format(time.RFC3339))
				}
				cmd.Printf("\n")
			}
		}),
	}
}
