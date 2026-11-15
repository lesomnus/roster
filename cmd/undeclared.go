package cmd

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Undeclared refuses a request that carries [Declared]: the label that says a
// file owns a row is written by what applies the file, and by nothing at a
// port.
//
// # Why
//
// The label's presence is the whole test of a declared row
// (`core.mayWriteDeclared`), and a declared row is read-only to everybody who
// reaches it through a port: that is the point of it. But the label was one
// like any other. Every generated `Add` takes `labels`, and so does an
// `Update`, so the read-only row was anybody's to make. A tenant's
// administrator made an administrator of their own -- a person, a role, the
// binding, each labelled -- whom nobody could then suspend, erase, unbind or
// narrow: not another administrator of the tenant, and not an operator at the
// admin listener, each told the row is written from a file that does not
// exist. And labelled their own `Tenant` row, which the admin console could
// then not close (#274).
//
// # Why here, and not in a layer
//
// A layer is where a rule about a write lives, and this is one rule about
// every write there is and will be: a request, of any service, on any
// listener. Said a service at a time it is said again for each one added, and
// the one that forgets is the hole. What is true of all of them is where they
// arrive, so it is asked there.
//
// And what writes the label does not arrive: `ApplyResources` and the front
// doors' own rows are written in process, at start or from a shell on the box,
// which no interceptor is on. So nobody is excepted -- not a deployment key,
// not an operator's session. Somebody at the admin console who wants a row a
// file owns writes the file.
//
// # What it reads
//
// Any `labels` there is in the request, at any depth, so that a request which
// carries a row inside it is read like one that is the row.
func Undeclared() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if m, ok := req.(proto.Message); ok && declares(m.ProtoReflect()) {
			return nil, status.Errorf(codes.InvalidArgument,
				"labels: %q says a file declares the row, and is written by what applies that file -- "+
					"`resources:`, or a front door's own configuration -- and not by a caller", Declared)
		}

		return next(ctx, req)
	}
}

// declares is whether a message, or one it carries, has [Declared] among its
// labels.
func declares(m protoreflect.Message) bool {
	found := false
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			if fd.Name() == "labels" && fd.MapKey().Kind() == protoreflect.StringKind {
				found = v.Map().Has(protoreflect.ValueOfString(Declared).MapKey())
			}
		case fd.Message() == nil:
		case fd.IsList():
			for i := range v.List().Len() {
				if declares(v.List().Get(i).Message()) {
					found = true

					break
				}
			}
		default:
			found = declares(v.Message())
		}

		return !found
	})

	return found
}
