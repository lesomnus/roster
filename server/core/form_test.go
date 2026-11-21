package core

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestEveryRowIsHeldToTheForm: every entity whose `Add` or `Patch` carries an
// alias, a name, a note or labels is held to the form by [form] -- the alias to
// its spelling, the rest to the table -- and so is whichever carries a
// subject, a list of methods or of scopes, an issuer or an identifier; and
// [New] puts every `Core` over it.
//
// The rule is one function and the entities it is asked of are a list, since
// each service is a type of its own: a list written by hand is right on the
// day it is written. So the list is read off the services themselves here. An
// entity that gains a `desc`, or a new one that has a `name` or a list of
// `methods`, and is not listed in `form.go` reaches the store with a megabyte
// in it, or is folded there and not found by the name it was given, and this
// says which.
func TestEveryRowIsHeldToTheForm(t *testing.T) {
	// A store that answers nothing: a write that reaches it is one the form
	// let through.
	s := formed(app.UnimplementedServer{})

	_, over := New(app.UnimplementedServer{}, Rules{}).Next().(form)
	require.True(t, over, "a Core is built over the form")

	long := map[string]func(m protoreflect.Message, f protoreflect.FieldDescriptor){
		// Not long: spelled otherwise than it is read, which is the other
		// thing the form holds a row to.
		"alias": func(m protoreflect.Message, f protoreflect.FieldDescriptor) {
			m.Set(f, protoreflect.ValueOfString("Seoul"))
		},
		"name": func(m protoreflect.Message, f protoreflect.FieldDescriptor) {
			m.Set(f, protoreflect.ValueOfString(strings.Repeat("n", MaxName+1)))
		},
		"desc": func(m protoreflect.Message, f protoreflect.FieldDescriptor) {
			m.Set(f, protoreflect.ValueOfString(strings.Repeat("d", MaxDesc+1)))
		},
		"labels": func(m protoreflect.Message, f protoreflect.FieldDescriptor) {
			m.Mutable(f).Map().Set(protoreflect.ValueOfString("k").MapKey(),
				protoreflect.ValueOfString(strings.Repeat("v", MaxLabelValue+1)))
		},
	}

	// What is compared and not read, each longer than the longest of them may
	// be; and the two lists, each of more entries than one has.
	for _, field := range []string{
		"subject", "provider", "issuer", "client_id", "secret_ref", "client", "scope", "document", "version",
	} {
		long[field] = func(m protoreflect.Message, f protoreflect.FieldDescriptor) {
			m.Set(f, protoreflect.ValueOfString(strings.Repeat("x", MaxLocation+1)))
		}
	}
	for field, most := range map[string]int{"methods": keys.MaxMethods, "scopes": MaxScopes} {
		long[field] = func(m protoreflect.Message, f protoreflect.FieldDescriptor) {
			for range most + 1 {
				m.Mutable(f).List().Append(protoreflect.ValueOfString("/a/b"))
			}
		}
	}
	listed := map[string]bool{"methods": true, "scopes": true}

	asked := 0
	server := reflect.ValueOf(s)
	for i := range server.NumMethod() {
		entity := server.Type().Method(i).Name
		if server.Method(i).Type().NumIn() != 0 || server.Method(i).Type().NumOut() != 1 ||
			!strings.HasSuffix(server.Method(i).Type().Out(0).Name(), "ServiceServer") {
			continue
		}
		service := server.Method(i).Call(nil)[0]

		for _, verb := range []string{"Add", "Patch"} {
			write := service.MethodByName(verb)
			if !write.IsValid() {
				continue
			}
			for field, set := range long {
				req := reflect.New(write.Type().In(1).Elem())
				m := req.Interface().(proto.Message).ProtoReflect()
				f := m.Descriptor().Fields().ByName(protoreflect.Name(field))
				if f == nil || (field == "labels") != f.IsMap() || listed[field] != f.IsList() ||
					(field != "labels" && f.Kind() != protoreflect.StringKind) {
					continue
				}
				set(m, f)

				out := write.Call([]reflect.Value{reflect.ValueOf(context.Background()), req})
				err, _ := out[1].Interface().(error)
				require.Equal(t, codes.InvalidArgument, status.Code(err),
					"%s.%s with a %s the form refuses reached the store: %v", entity, verb, field, err)
				require.Contains(t, status.Convert(err).Message(), field)
				asked++
			}
		}
	}

	// Fifteen entities carry an alias, a name, a note or labels, on two verbs
	// each, and eight of them something of the rest: were the walk to find
	// none, it would hold nothing and pass.
	require.GreaterOrEqual(t, asked, 100)
}
