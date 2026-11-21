package cmd

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pderr"
)

// stamps are the moments the server stamps, and what each is, for the refusal:
// one every row has, and two a person has.
var stamps = map[protoreflect.Name]string{
	"date_created":     "when a row was made is stamped as it is made",
	"date_invalidated": "when somebody's sign-ins were ended is stamped by `HolderService/Invalidate`",
	"date_disabled":    "when somebody was suspended is stamped by `HolderService/Disable`",
}

// Undated refuses a request that says a moment the server stamps: when a row
// was made, and when somebody was suspended or signed out everywhere.
//
// # Why
//
// Every generated `Add` carries `date_created`, because the request is made
// from the entity and the field is one a row may be created with; and the
// generated create wrote what it was handed. So when a row was made was its
// caller's to say, at every entity. A list is in the order rows were made and
// a cursor is a place in that order: a row dated 2001 is behind every walk
// already under way, which never meets it, and one dated 2099 is last behind
// rows made after it. A sync that follows a list missed a person.
//
// `HolderService/Add` carries the two a person has as well. Somebody added
// with `date_invalidated` ten years on has every sign-in refused until then,
// as one made before it is, with nothing anywhere saying why; `Invalidate`
// takes no moment so that this cannot be said (`holder.ext.proto`), and `Add`
// said it. Somebody added with `date_disabled` was suspended by no verb that
// suspends.
//
// # Why here, and not as `stamped`
//
// payday has a word for a field no request asserts (`payday.field.stamped`),
// and the generated gate refuses one: that is how a person's `directory` is
// refused. It is said field by field in a schema, and `date_created` is
// payday's own field on its own entities -- a person, a tenant -- which an
// overlay may not redeclare. So it cannot be said of every entity, and the
// entities it cannot be said of are the ones this was seen on.
//
// What is true of all of them is where they arrive, which is [Undeclared]'s
// reason and so its place: one rule about every write there is and will be,
// asked where requests arrive, on every listener.
//
// # Who still says one
//
// What does not arrive: a write made in process, at start or from a shell on
// the box, which no interceptor is on. A test that needs a row made a week ago
// writes one that way, and nothing the servers write themselves says any of
// the three on an `Add`. Nobody is excepted at a port, an operator at the
// admin listener included: a row out of its place in a list is out of it
// whoever wrote it.
//
// # What it reads
//
// A moment of one of those names anywhere in the request, as [Undeclared]
// reads a label: a request that carries a row is read like one that is the
// row. A `select` is not one -- there the name is a yes or a no, not a moment.
func Undated() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if m, ok := req.(proto.Message); ok {
			if f := dated(m.ProtoReflect()); f != "" {
				return nil, pderr.Invalidf(string(f), "%s, and is not a request's to say", stamps[f])
			}
		}

		return next(ctx, req)
	}
}

// dated is the first of [stamps] a message says, itself or in a message it
// carries, and nothing where it says none.
func dated(m protoreflect.Message) protoreflect.Name {
	var found protoreflect.Name
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.Message() == nil || fd.IsMap():
		case fd.Message().FullName() == "google.protobuf.Timestamp":
			if _, ok := stamps[fd.Name()]; ok && !fd.IsList() {
				found = fd.Name()
			}
		case fd.IsList():
			for i := range v.List().Len() {
				if found = dated(v.List().Get(i).Message()); found != "" {
					break
				}
			}
		default:
			found = dated(v.Message())
		}

		return found == ""
	})

	return found
}
