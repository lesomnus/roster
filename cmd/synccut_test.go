package cmd_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/watch"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	rostersync "github.com/lesomnus/roster/server/sync"
)

// cutBroker is a broker that cuts every subscriber off as it subscribes: what
// the memory broker does to one that fell behind, and the `postgres` broker to
// every one when it lost its connection.
type cutBroker struct{}

func (cutBroker) Publish(context.Context, watch.Event) {}

func (cutBroker) Subscribe() (<-chan watch.Event, func()) {
	c := make(chan watch.Event)
	close(c)

	return c, func() {}
}

// cutSync is a sync stream served on a listener of its own, cut as it begins.
func cutSync(t *testing.T) string {
	t.Helper()
	b, _ := build(t)

	g := grpc.NewServer()
	app.RegisterSyncServiceServer(g, rostersync.Service(b.Walled, watch.New(cutBroker{}), nil, nil))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(g.Stop)
	go func() { _ = g.Serve(l) }()

	return l.Addr().String()
}

// TestAStreamThatIsCutSaysWhatHappened: a follower cut for falling behind, or
// because the `postgres` broker lost its connection, was ended in payday's
// words for an entity's watch, *ask again and you will be given what is there
// now*. This stream gives nothing on asking again, and a lost connection is
// not falling behind. The code stays `ResourceExhausted`; the words say what
// is so of this stream.
func TestAStreamThatIsCutSaysWhatHappened(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	s := rostersync.Service(b.Walled, watch.New(cutBroker{}), nil, nil)
	err := s.Watch(app.SyncWatchRequest_builder{}.Build(), &collector{ctx: ctx})
	x.Error(err)
	x.Equal(codes.ResourceExhausted, status.Code(err))

	said := status.Convert(err).Message()
	x.NotContains(said, "given what is there now", "a sync stream gives nothing on asking again")
	x.Contains(said, "cut")
	x.Contains(said, "fell behind")
	x.Contains(said, "broker lost its connection")
	x.Contains(said, "may have been missed")
	x.Contains(said, "nothing is said again")
	x.Contains(said, "ask about whoever you hold")
}

// TestSyncWatchSaysWhatEndedIt: `roster sync watch` answered every ending but
// an interrupt with *the stream ended … Run it again*, a refusal among them.
// It prints the code and the words the server ended the stream with, and says
// *run it again* only where running it again mends it.
func TestSyncWatchSaysWhatEndedIt(t *testing.T) {
	t.Run("a refusal", func(t *testing.T) {
		x := require.New(t)

		// A key, and a role, without SyncService/Watch.
		b := cliUp(t, "/roster.HolderService/Invalidate")
		_, err := cliRun(t, &b.Hers, "sync", "watch")
		x.Error(err)
		x.Contains(err.Error(), codes.PermissionDenied.String(), "the refusal's code")
		x.NotContains(err.Error(), "Run it again", "a refusal is not mended by asking again")
	})

	t.Run("a cut", func(t *testing.T) {
		x := require.New(t)

		c := cmd.Config{Client: cmd.ClientConfig{
			Addr: cutSync(t), Insecure: true,
			Auth: cmd.ClientAuthConfig{Scheme: "bearer", Credential: credentialOf("rt_anything")},
		}}
		_, err := cliRun(t, &c, "sync", "watch")
		x.Error(err)
		x.Contains(err.Error(), codes.ResourceExhausted.String(), "the code the stream was ended with")
		x.Contains(err.Error(), "fell behind", "the server's words")
		x.Contains(err.Error(), "Run it again")
	})
}
