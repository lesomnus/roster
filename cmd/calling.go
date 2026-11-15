package cmd

import (
	"context"

	"google.golang.org/grpc"

	"github.com/lesomnus/payday/grpcx"
)

// Which method a call is, where what narrows a read can ask.
//
// # Why the wall asks it
//
// A grant made in a site reaches that site, and somebody holds more than one
// grant: a role across the tenant that reads its teams, and another in Seoul
// that erases them. The wall narrowed by the **caller** -- every site any of
// their roles reached, and nothing at all once one of them was held across the
// tenant -- so the role held in Seoul erased Busan's team, the other role
// having shown it to them (#274). Which sites a call is narrowed to is a
// question about the method it is: where the caller holds **that**.
//
// `gate.Policy` is told the method and answers only yes or no; what narrows a
// query is asked from inside the store, with the context and nothing else
// ([policy.sets]). So the method is put in the context, by an interceptor of
// its own beside the gate.
//
// # What it is not
//
// Anything a caller says. It is `info.FullMethod`, the name gRPC dispatched
// by.
type callingKey struct{}

// Calling says which method a call is, for what narrows a read beneath it.
//
// An interceptor says it of every call that arrives at a port. It is exported
// for what calls the walled stack from inside the process as a caller would --
// a test, mostly: with no method said, a call is narrowed by every site
// anything its caller holds reaches, which is what it was.
func Calling(ctx context.Context, method string) context.Context {
	return context.WithValue(ctx, callingKey{}, method)
}

// calling is the method [Calling] said, if one was.
func calling(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(callingKey{}).(string)

	return v, ok && v != ""
}

func callingUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(Calling(ctx, info.FullMethod), req)
	}
}

func callingStream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, grpcx.StreamWithContext(ss, Calling(ss.Context(), info.FullMethod)))
	}
}
