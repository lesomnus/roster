package cmd

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/grpcx"
)

// AskedAgain ends a stream that its caller would no longer be let open.
//
// # Why
//
// A call carries its credential every time, so somebody stopped is refused at
// their next call. A stream carries one **once**, when it opens, and a `Watch`
// is then served until somebody hangs up. payday ends one at its credential's
// expiry, by a timer set as it opens (`auth.InterceptorStream`); nothing ended
// one for anything that happens afterwards. So a `Watch` opened by somebody
// who was then suspended, erased, signed out of everything, or whose key was
// ended or role taken away, went on delivering whole rows as they changed --
// while the same credential was `401` at a call made beside it (#274). It is
// what `standing` exists to keep an app from doing with somebody it signed in,
// and roster's own streams did it.
//
// # How
//
// Every `every`, the stream is asked what it was asked when it opened: the
// credential it arrived with is read again, resolved again, and the gate is
// asked again about this method. A stream whose caller is now refused is
// ended, in the words a stream cut for any other reason is: *reconnect*. A
// client does then what it does when a connection drops, and is refused at
// the door like any other call.
//
// Asked on a clock, and not told the moment it happens. Telling means finding
// every stream somebody has open, in every replica, when a row changes; asking
// is a few reads a stream, and bounds how long somebody stopped is still
// served by a number a deployment can read (`stream.recheck`). The one does
// not rule out the other, and this is the one that needs nothing else to be
// true.
//
// # What ends one, and what does not
//
// A refusal: the credential is nobody's now, or its holder may not call this.
// Not an error of another kind -- a database that did not answer is not
// somebody being stopped, and the stream is asked again next time.
//
// It sits outside the interceptor that authenticates, because that one leaves
// a call alone once it has a frame, and what is asked here is the request as
// it arrived.
func AskedAgain(every time.Duration, h auth.Handler, r auth.Resolver, public auth.Public, p gate.Policy) grpc.StreamServerInterceptor {
	if every <= 0 {
		every = DefaultRecheck
	}

	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		arrived := ss.Context()
		if _, framed := frame.From(arrived); framed || (public != nil && public(info.FullMethod)) {
			// A call from inside this process, vouched for where it came in;
			// or one served to anybody, which nobody can be stopped from.
			return handler(srv, ss)
		}

		ctx, cancel := context.WithCancelCause(arrived)
		defer cancel(nil)

		go func() {
			t := time.NewTicker(every)
			defer t.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}

				if err := stillMay(ctx, arrived, h, r, p, info.FullMethod); err != nil {
					cancel(errStopped)

					return
				}
			}
		}()

		err := handler(srv, grpcx.StreamWithContext(ss, ctx))
		if errors.Is(context.Cause(ctx), errStopped) {
			return status.Error(codes.Unauthenticated, errStopped.Error())
		}

		return err
	}
}

// errStopped is why a stream was ended: in `auth.ErrChanged`'s word for what
// to do about it, since it is the same thing to do.
var errStopped = errors.New("whoever opened this stream may not any more; reconnect")

// stillMay is whether the request a stream arrived as would be served now,
// and nil where that cannot be said.
func stillMay(ctx, arrived context.Context, h auth.Handler, r auth.Resolver, p gate.Policy, method string) error {
	// On the stream's own context, so that asking ends with the stream, and
	// with what it arrived carrying.
	id, err := h.Handle(arrived)
	if err != nil {
		return refusal(ctx, err)
	}
	f, err := r.Resolve(arrived, id)
	if err != nil {
		return refusal(ctx, err)
	}
	if f == nil {
		return status.Error(codes.Unauthenticated, "nobody")
	}

	// What the credential allows is the credential's to say, as it was when
	// the stream opened (`auth.authenticate`).
	v := *f
	v.Grant = id.Grant
	if _, err := gate.Decide(frame.Into(arrived, &v), p, method); err != nil {
		return refusal(ctx, err)
	}

	return nil
}

// refusal is an error where it says the caller is refused, and nil where it
// says only that nobody could tell -- or the stream is over, and nobody is
// asking.
//
// A handler refuses in words of its own -- a token nothing holds, a session
// the store has forgotten -- and says [auth.ErrUnavailable] for the one thing
// that is not a refusal: it could not ask. So what is not that, and is not a
// server saying it is in trouble, is somebody who may not.
func refusal(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, auth.ErrUnavailable) {
		return nil
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Internal, codes.ResourceExhausted:
		return nil
	default:
		return err
	}
}
