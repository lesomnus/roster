package cmd_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/pdpb"

	_ "github.com/lesomnus/roster/rstr"
)

// wireNamesEntities is every entity of the package, by the name the generator
// gives it.
func wireNamesEntities(x *require.Assertions) []string {
	entities := []string{}
	protoregistry.GlobalFiles.RangeFilesByPackage("roster", func(file protoreflect.FileDescriptor) bool {
		for i := range file.Messages().Len() {
			m := file.Messages().Get(i)
			if proto.HasExtension(m.Options(), pdpb.E_Entity) {
				entities = append(entities, string(m.Name()))
			}
		}
		return true
	})
	x.Greater(len(entities), 20, "few entities were read, so this proves little")
	return entities
}

// wireNamesGenerated tells a message the generator writes for an entity from
// one written by hand, by the name it gives it.
func wireNamesGenerated(entities []string, m protoreflect.MessageDescriptor) bool {
	name := string(m.Name())
	for _, e := range entities {
		rest, ok := strings.CutPrefix(name, e)
		if !ok {
			continue
		}
		if rest == "" || strings.HasPrefix(rest, "RefBy") || slices.Contains([]string{
			"AddRequest", "GetRequest", "Ref", "Select", "PatchRequest", "ApplyRequest", "EraseRequest", "EraseResponse",
			"ListRequest", "ListResponse", "Filter", "WatchRequest", "WatchResponse", "WatchItem",
		}, rest) {
			return true
		}
	}
	return false
}

// TestARequestsRefIsItsServicesOwnRow: in every request written by hand, a
// field named `ref` is a reference to the entity its service is of, and a
// person who is not that row is `holder`.
//
// `CredentialService/Set`, `Enrol`, `Unlock` and `Issue` took the person as
// `ref`, a `HolderRef`, on the service whose `Erase` takes a credential as
// `ref`; the account page sent both on one screen. In twenty-three other
// requests `ref` is the service's own row, and where a request names a person
// who is not, as `ApiKey.Issue` does, the field is `holder`. Answered: `ref`
// is a person in four; right is a person is `holder` there as everywhere.
func TestARequestsRefIsItsServicesOwnRow(t *testing.T) {
	x := require.New(t)

	entities := wireNamesEntities(x)
	asked := 0
	other := []string{}
	protoregistry.GlobalFiles.RangeFilesByPackage("roster", func(file protoreflect.FileDescriptor) bool {
		for i := range file.Services().Len() {
			s := file.Services().Get(i)
			entity, ok := strings.CutSuffix(string(s.Name()), "Service")
			if !ok || !slices.Contains(entities, entity) {
				// A service of no entity (`MeService`, `VouchService`, ...)
				// has no row of its own for a `ref` to be.
				continue
			}
			for j := range s.Methods().Len() {
				in := s.Methods().Get(j).Input()
				if wireNamesGenerated(entities, in) {
					continue
				}
				f := in.Fields().ByName("ref")
				if f == nil {
					continue
				}
				asked++
				if f.Kind() != protoreflect.MessageKind || string(f.Message().Name()) != entity+"Ref" {
					other = append(other, string(in.Name())+".ref")
				}
			}
		}
		return true
	})
	x.Greater(asked, 15, "few requests were read, so this proves little")
	x.Empty(other, "a request's `ref` that is not its service's own row: a person is `holder`")
}

// TestASecretACallerSendsIsBytes: no request carries a password or a code
// being proved as text.
//
// `AuthSignInRequest.password` was a `string`, where the five other requests
// that take a password take `bytes`, and the same request's second step
// already took `secret` as `bytes`; so one page sent a password as it was
// typed to sign in and encoded to change it. Answered: text in one request;
// right is one type, and in `AuthSignInRequest` one field for whatever is
// being proved at that step.
//
// Which fields carry such a thing is said by their names, since nothing in a
// descriptor says it: `secret`, `current` and `password` are what this schema
// calls a password or a code a caller proves. A token roster minted and
// answered as text -- a `continuation`, a `link` -- is sent back as it was
// answered, and is not one of them.
func TestASecretACallerSendsIsBytes(t *testing.T) {
	x := require.New(t)

	proved := []string{"secret", "current", "password"}
	asked := 0
	text := []string{}
	var walk func(protoreflect.MessageDescriptors)
	walk = func(ms protoreflect.MessageDescriptors) {
		for i := range ms.Len() {
			m := ms.Get(i)
			walk(m.Messages())
			if !strings.HasSuffix(string(m.Name()), "Request") {
				continue
			}
			for j := range m.Fields().Len() {
				f := m.Fields().Get(j)
				if !slices.Contains(proved, string(f.Name())) {
					continue
				}
				asked++
				if f.Kind() != protoreflect.BytesKind {
					text = append(text, string(m.Name())+"."+string(f.Name()))
				}
			}
		}
	}
	protoregistry.GlobalFiles.RangeFilesByPackage("roster", func(file protoreflect.FileDescriptor) bool {
		walk(file.Messages())
		return true
	})
	x.GreaterOrEqual(asked, 6, "few fields were read, so this proves little")
	x.Empty(text, "a password or a code a caller proves, sent as text")
}

// TestNoEntityButAConnectionHasAnIssuer: `issuer` is an OpenID issuer, and a
// row that holds who a secret was handed to says `issued_to`.
//
// `Delegation.issuer`, `Link.issuer` and `Continuation.issuer` held the
// identifier of the caller the secret was given to, so that no other caller
// can present it, and on a delegation made by `Exchange` the audience: who
// it was made **for**, which a page lists as where somebody is signed in.
// `Connection.issuer` is an OpenID issuer's URL. Answered: one word for both;
// right is `issuer` on `Connection` alone.
func TestNoEntityButAConnectionHasAnIssuer(t *testing.T) {
	x := require.New(t)

	has := []string{}
	for _, name := range wireNamesEntities(x) {
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName("roster." + name))
		x.NoError(err, name)
		if d.(protoreflect.MessageDescriptor).Fields().ByName("issuer") != nil {
			has = append(has, name)
		}
	}
	x.Equal([]string{"Connection"}, has)
}

// TestWhoseIsAOneofInTheTwoIssueRequests: the alternatives of whom a key or a
// password is issued for are one `oneof`, as every generated reference says
// its alternatives.
//
// `ApiKeyIssueRequest` (`holder_alias` or `holder`) and
// `CredentialIssueRequest` (`holder`, `email` or `holder_alias`) said *exactly
// one of* in a comment and refused two at run time, so every generated client
// let two be built. Answered: separate fields; right is a `oneof whose`, the
// numbers as they were, so that two cannot be sent and none is a `oneof`
// unset.
func TestWhoseIsAOneofInTheTwoIssueRequests(t *testing.T) {
	x := require.New(t)

	for name, want := range map[string][]string{
		"roster.ApiKeyIssueRequest":     {"holder", "holder_alias"},
		"roster.CredentialIssueRequest": {"email", "holder", "holder_alias"},
	} {
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
		x.NoError(err, name)
		m := d.(protoreflect.MessageDescriptor)

		o := m.Oneofs().ByName("whose")
		x.NotNil(o, "%s says whose in separate fields", name)
		has := []string{}
		for i := range o.Fields().Len() {
			has = append(has, string(o.Fields().Get(i).Name()))
		}
		slices.Sort(has)
		x.Equal(want, has, name)
	}
}

// TestVouchTenantIsATenantRef: what `VouchWho.tenant` takes is
// `TenantRef`'s `oneof key`, arm for arm, written in `vouch.proto` because
// that file compiles before the references are generated.
//
// `VouchWho.tenant` was a `string`, the tenant's alias, where `tenant` is the
// identifier in six other messages and `FrontService/WhoseHost` answers an
// identifier, which a front door turned back into an alias to fill it.
// Answered: an alias alone; right is either, as a `TenantRef` names one.
func TestVouchTenantIsATenantRef(t *testing.T) {
	x := require.New(t)

	arms := func(name string) map[string]string {
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
		x.NoError(err, name)
		o := d.(protoreflect.MessageDescriptor).Oneofs().ByName("key")
		x.NotNil(o, "%s has no `oneof key`", name)
		out := map[string]string{}
		for i := range o.Fields().Len() {
			f := o.Fields().Get(i)
			out[string(f.Name())] = fmt.Sprintf("%d %s", f.Number(), f.Kind())
		}
		x.Equal(o.Fields().Len(), d.(protoreflect.MessageDescriptor).Fields().Len(), "%s has a field beside its key", name)
		return out
	}
	x.Equal(arms("roster.TenantRef"), arms("roster.VouchTenant"))

	who, err := protoregistry.GlobalFiles.FindDescriptorByName("roster.VouchWho")
	x.NoError(err)
	f := who.(protoreflect.MessageDescriptor).Fields().ByName("tenant")
	x.Equal(protoreflect.MessageKind, f.Kind())
	x.Equal("VouchTenant", string(f.Message().Name()))
}

// TestNoFieldIsNamedDateLocked: a moment is named for its event in the past
// tense, so that the name does not turn wrong once the moment has passed.
//
// `date_locked` held the moment a lockout **ends**, which is not when the
// account was locked, on `Credential` and in the four messages that answer
// it. Answered: a name for the start that holds the end; right is
// `date_unlocked`, the moment somebody may try again.
func TestNoFieldIsNamedDateLocked(t *testing.T) {
	x := require.New(t)

	named := []string{}
	var walk func(protoreflect.MessageDescriptors)
	walk = func(ms protoreflect.MessageDescriptors) {
		for i := range ms.Len() {
			m := ms.Get(i)
			walk(m.Messages())
			if m.Fields().ByName("date_locked") != nil {
				named = append(named, string(m.FullName()))
			}
		}
	}
	protoregistry.GlobalFiles.RangeFilesByPackage("roster", func(file protoreflect.FileDescriptor) bool {
		walk(file.Messages())
		return true
	})
	x.Empty(named, "a field named for when a lock began holds when it ends")
}
