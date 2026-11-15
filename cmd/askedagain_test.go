package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestAStreamLeftOpenIsAskedAgainWhoOpenedIt: somebody stopped is refused at
// their next call, and a stream they had open is ended within the time a
// deployment says (`stream.recheck`).
//
// # What it was
//
// A stream's credential is read once, when it opens. So a `Watch` opened by
// somebody who was then suspended, erased, signed out of everything, or whose
// key was ended or role taken away, went on delivering whole rows as they
// changed, for as long as the connection stayed up -- while the same key was
// `401` at a call made beside it (#274). An expiry ended a stream, by a timer
// set when it opened; nothing else did.
func TestAStreamLeftOpenIsAskedAgainWhoOpenedIt(t *testing.T) {
	const every = 50 * time.Millisecond

	// The deployment's own key may write a person's profile, which is how
	// something is made to happen on the stream: over the wire, where a write
	// is published.
	b := keyedFor(t, func(c *cmd.Config) { c.Stream.Recheck = every }, app.HolderService_Update_FullMethodName)
	ctx := t.Context()

	watching := []string{app.HolderService_Watch_FullMethodName, app.HolderService_Get_FullMethodName}

	// watcher is somebody who may watch, the stream they opened, and the
	// binding that lets them.
	type watcher struct {
		who     pdid.Id
		key     string
		binding []byte
		stream  grpc.ServerStreamingClient[app.HolderWatchResponse]
	}
	opens := func(t *testing.T, alias string) *watcher {
		t.Helper()
		x := require.New(t)

		w := &watcher{who: addHolder(t, ctx, b.Server, b.Contoso, alias)}
		role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: alias + "-watches", Methods: watching,
		}.Build())
		x.NoError(err)
		bound, err := b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: w.who.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		w.binding = bound.GetId()
		w.key = mintFor(t, ctx, b, w.who, "watches", watching, time.Time{})

		w.stream, err = app.NewHolderServiceClient(b.Conn).Watch(bearing(ctx, w.key), app.HolderWatchRequest_builder{
			Filters: []*app.HolderFilter{app.HolderFilter_builder{
				Ref: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
			}.Build()},
		}.Build())
		x.NoError(err)
		_, err = w.stream.Recv()
		x.NoError(err, "the control: the stream opens, and says what there is")

		return w
	}

	// ended is how the stream ends, waited for no longer than a few of the
	// deployment's own periods would take.
	ended := func(t *testing.T, w *watcher) error {
		t.Helper()

		done := make(chan error, 1)
		go func() {
			for {
				if _, err := w.stream.Recv(); err != nil {
					done <- err

					return
				}
			}
		}()

		select {
		case err := <-done:
			return err
		case <-time.After(100 * every):
			t.Fatal("the stream was still being served, long after its caller was stopped")

			return nil
		}
	}

	// What stops somebody who holds a key. Being signed out of everything is
	// not among them: that ends sessions and delegations, which are sign-ins,
	// and a key is not one -- a stream opened with either is ended by the same
	// asking, since what refuses them at a call refuses them here.
	for name, stop := range map[string]func(t *testing.T, w *watcher){
		"suspended": func(t *testing.T, w *watcher) {
			_, err := b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{
				Ref: app.HolderRef_builder{Id: w.who.Bytes()}.Build(),
			}.Build())
			require.NoError(t, err)
		},
		"erased": func(t *testing.T, w *watcher) {
			_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: w.who.Bytes()}.Build())
			require.NoError(t, err)
		},
		"no longer let": func(t *testing.T, w *watcher) {
			_, err := b.Ungated.Binding().Erase(ctx, app.BindingRef_builder{Id: w.binding}.Build())
			require.NoError(t, err)
		},
	} {
		t.Run("somebody "+name, func(t *testing.T) {
			x := require.New(t)

			w := opens(t, map[string]string{
				"suspended": "suspended", "erased": "erased", "no longer let": "unbound",
			}[name])
			stop(t, w)

			err := ended(t, w)
			x.Equal(codes.Unauthenticated, status.Code(err), "%v", err)
			x.ErrorContains(err, "reconnect", "what a client does about it is what it does when it is cut off")
		})
	}

	// And nobody else's is: asked again and again, whoever may still watch
	// goes on being served, and hears what changes.
	t.Run("and whoever still may is still served", func(t *testing.T) {
		x := require.New(t)

		w := opens(t, "stays")
		time.Sleep(6 * every)

		ref := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()
		was, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		_, err = app.NewHolderServiceClient(b.Conn).Update(bearing(ctx, b.Token), app.HolderUpdateRequest_builder{
			Ref:         ref,
			DateUpdated: was.GetDateUpdated(),
			Profile:     app.Profile_builder{DisplayName: "Said After Several Askings"}.Build(),
		}.Build())
		x.NoError(err)

		heard, cancel := context.WithTimeout(ctx, 100*every)
		defer cancel()
		got := make(chan error, 1)
		go func() {
			_, err := w.stream.Recv()
			got <- err
		}()
		select {
		case err := <-got:
			x.NoError(err, "a stream whose caller still may was ended")
		case <-heard.Done():
			t.Fatal("nothing was heard on a stream that should still be open")
		}
	})
}
