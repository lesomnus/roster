package cmd_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/server/pd"
)

// askedAgainStream is a stream as [cmd.AskedAgain] is handed one: a context
// with a credential on it, and a count of what was sent down it.
type askedAgainStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent atomic.Int32
}

func (s *askedAgainStream) Context() context.Context { return s.ctx }
func (s *askedAgainStream) SendMsg(any) error        { s.sent.Add(1); return nil }

// TestAStoreThatDoesNotAnswerOnceEndsNoStream: an open stream is ended where
// asking again said its caller may not, and only there.
//
// # What it was
//
// The resolver asked again hands back the store's own error, which carries no
// status, and the asking read a code off it: `Unknown`, which was not among
// the five it listed as *nobody could tell*. So with the database not
// answering for one asking, every `HolderService/Watch` and
// `SyncService/Watch` open in that moment was ended `Unauthenticated` --
// the code that tells a client to throw its credential away -- for something
// the caller did not do. The asking said of itself that a database that did
// not answer is not somebody being stopped; now it is so.
func TestAStoreThatDoesNotAnswerOnceEndsNoStream(t *testing.T) {
	const every = 10 * time.Millisecond

	who := pdid.New(pd.HolderDomain)
	tenant := pdid.New(pd.TenantDomain)
	handler := auth.HandlerFunc(func(context.Context) (auth.Identity, error) {
		return auth.Identity{Id: who.String(), Grant: frame.Whole()}, nil
	})
	arrived := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer whatever"))
	info := &grpc.StreamServerInfo{FullMethod: "/roster.HolderService/Watch"}

	t.Run("a store that fails one asking", func(t *testing.T) {
		x := require.New(t)

		// The first asking finds the store away, in its own words, as a
		// closed database says it; every asking after finds it back.
		var asked atomic.Int32
		back := make(chan struct{})
		resolver := auth.ResolverFunc(func(context.Context, auth.Identity) (*frame.Frame, error) {
			switch asked.Add(1) {
			case 1:
				return nil, errors.New("sql: database is closed")
			case 2:
				close(back)
			}

			return frame.New(who, tenant, frame.Grant{}), nil
		})

		ss := &askedAgainStream{ctx: arrived}
		err := cmd.AskedAgain(every, handler, resolver, nil, nil)(nil, ss, info, func(_ any, s grpc.ServerStream) error {
			// Served until it has been asked again after the store failed,
			// and then it is heard.
			select {
			case <-back:
			case <-s.Context().Done():
				return context.Cause(s.Context())
			}

			return s.SendMsg(nil)
		})
		x.NoError(err, "a stream was ended because the store did not answer once")
		x.EqualValues(1, ss.sent.Load(), "the stream was still heard after the store came back")
	})

	// The control: a caller the asking says may not is still ended, in the
	// words a client reconnects on.
	t.Run("a caller who was stopped", func(t *testing.T) {
		x := require.New(t)

		resolver := auth.ResolverFunc(func(context.Context, auth.Identity) (*frame.Frame, error) {
			return nil, fmt.Errorf("%w: this holder is disabled", auth.ErrNoCredential)
		})

		ss := &askedAgainStream{ctx: arrived}
		err := cmd.AskedAgain(every, handler, resolver, nil, nil)(nil, ss, info, func(_ any, s grpc.ServerStream) error {
			<-s.Context().Done()

			return s.Context().Err()
		})
		x.Equal(codes.Unauthenticated, status.Code(err), "%v", err)
		x.ErrorContains(err, "reconnect")
	})
}
