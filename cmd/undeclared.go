package cmd

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	app "github.com/lesomnus/roster/rstr"
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
// # And an app's holder, the same way
//
// [app.LabelApp] is the label `roster app install` writes on the holder it
// makes for an app in a tenant, and a directory built on roster leaves such a
// holder out of its people (`app.Holder.NoPerson`). Anybody's to write, a
// person could leave the directory by saying they were an app, and an app's
// holder would be told from them by nothing. So it is refused here as
// [Declared] is, and written by that command at the box (#451). Taking it
// off is no more a caller's: no verb served at a port writes a holder's
// labels but `Add`, which makes a row, and `Patch` is served nowhere
// ([Server.closed]).
//
// # What it reads
//
// Any `labels` there is in the request, at any depth, so that a request which
// carries a row inside it is read like one that is the row.
func Undeclared() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		m, ok := req.(proto.Message)
		if !ok {
			return next(ctx, req)
		}
		if carries(m.ProtoReflect(), Declared) {
			return nil, status.Errorf(codes.InvalidArgument,
				"labels: %q says a file declares the row, and is written by what applies that file -- "+
					"`resources:`, or a front door's own configuration -- and not by a caller", Declared)
		}
		if carries(m.ProtoReflect(), app.LabelApp) {
			return nil, status.Errorf(codes.InvalidArgument,
				"labels: %q says a holder is an app's, and is written by `roster app install` at the box "+
					"and not by a caller", app.LabelApp)
		}

		return next(ctx, req)
	}
}

// carries is whether a message, or one it carries, has `key` among its
// labels.
func carries(m protoreflect.Message, key string) bool {
	found := false
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			if fd.Name() == "labels" && fd.MapKey().Kind() == protoreflect.StringKind {
				found = v.Map().Has(protoreflect.ValueOfString(key).MapKey())
			}
		case fd.Message() == nil:
		case fd.IsList():
			for i := range v.List().Len() {
				if carries(v.List().Get(i).Message(), key) {
					found = true

					break
				}
			}
		default:
			found = carries(v.Message(), key)
		}

		return !found
	})

	return found
}
