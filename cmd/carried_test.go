package cmd_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/roster/internal/ent"
	app "github.com/lesomnus/roster/rstr"
)

// What holds an answer that carries somebody's data to the access record
// (#280), beside what holds a command at the box to it
// (`cli/recorded_test.go`).
//
// # Why `HolderService/SignsIn` passed
//
// The record's interceptor finds whose data an answer carries in the messages
// that declare a personal field: `Holder`, `Email`, `Identity`, `Phone`. An
// answer that carries the same data in a message of its own is one it does not
// see -- `SignInIdentity.subject` is `Identity.subject`, copied into a message
// that declares nothing. What held reads to the record did not see it either.
// `cli/recorded_test.go` holds the commands: that every pipe a command stands
// up has the interceptor, and that every read a command makes in Go says what
// it shows. `roster holder signs-in` is an RPC on the pipe, the interceptor
// was there, and it found nothing to record.
//
// # What this walks
//
// The schema, which is the one place such an answer can be found without
// calling it: every method a listener serves, and every message its answer can
// hold. A message that declares a personal field is the interceptor's. Any
// other that is no entity's row and has a field called what a declared one is
// called, of its type -- or holds a `Profile` or a `Portrait` under any name --
// is named below with which RPCs answer it and what is done about it.
//
// By name, since nothing in the schema says one message's field is a copy of
// another's. So it finds a copy that kept its name, which is how a copy is
// usually written, and not one that was renamed; and a name as plain as
// `name` finds fields that are nobody's, which the list says of each.

// answers is what is done about a field an answer carries under a personal
// field's name: the RPCs that answer it, and why it is or is not a row.
type answers struct {
	by  []string
	why string
}

// TestEveryAnswerThatNamesSomebodyIsOnTheRecordOrSaysWhy: a field of an
// answer that is called what a personal field is called, in a message the
// access record does not read, is named here with what is done about it. One
// that is new, or gone, or answered by an RPC not named with it, fails this
// until somebody says which it is: an RPC that answers somebody's subject at a
// provider and leaves no row is what #280 found.
func TestEveryAnswerThatNamesSomebodyIsOnTheRecordOrSaysWhy(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	const (
		said = "somebody else's, read by an administrator or an operator: said with access.Saw, naming the person (TestReadingHowSomebodySignsInIsOnTheRecord)"
		own  = "the caller's own, told to them or to the app they are using; no row, on purpose (TestWhatSomebodyIsToldOfThemselvesLeavesNoRow)"
	)
	signsIn := []string{app.HolderService_SignsIn_FullMethodName, app.MeService_Get_FullMethodName}
	me := []string{app.MeService_Get_FullMethodName}

	want := map[string]answers{
		"SignInIdentity.subject": {signsIn, said + "; and in MeService/Get, " + own},
		"SignInKey.alias":        {signsIn, "what a key is called, which no declaration calls personal; in SignsIn it is on the row that names the person, and in MeService/Get it is " + own},
		"SignInCredential.name":  {signsIn, "what a factor is called, as a key is; the same two answers"},

		"MeGetResponse.alias": {me, own},
		"MeGetResponse.name":  {me, own},
		"MeEmail.address":     {me, own},
		"MeTeam.alias":        {me, "a team's, which is nobody's personal data, told to somebody on it"},
		"MeTeam.name":         {me, "a team's, as its alias is"},
		"MeGroup.alias":       {me, "a group's, as a team's is"},
		"MeGroup.name":        {me, "a group's, as a team's is"},

		"VouchFactor.name": {[]string{
			app.AuthService_SignIn_FullMethodName,
			app.VouchService_Accept_FullMethodName,
			app.VouchService_Continue_FullMethodName,
			app.VouchService_Delegate_FullMethodName,
			app.VouchService_Redeem_FullMethodName,
			app.VouchService_Verify_FullMethodName,
		}, "what somebody calls the second factor they are being asked for, told to the front door they are signing in at, after their first: " + own},
		"VouchLeaveLinkResponse.address": {[]string{app.VouchService_LeaveLink_FullMethodName},
			"the address a leaving link is mailed to, which the front door sent and the person signed in there proved: " + own},

		"AuthOffersProvider.name": {[]string{app.AuthService_Offers_FullMethodName},
			"what a tenant calls a provider on its sign-in page; nobody's"},
		"TokenIntrospectResponse.alias": {[]string{pdpb.TokenService_Introspect_FullMethodName},
			"roster answers who a token stands for by identifier and fills no name (server/keys/service.go)"},
	}

	// Every listener there is: what a customer's caller reaches, what an
	// operator's console does, and the control plane's own.
	s, c, _ := adminDeployment(t, nil)
	var served []*grpc.Server
	for _, build := range []func() (*grpc.Server, error){
		func() (*grpc.Server, error) { return s.Grpc(ctx, c) },
		func() (*grpc.Server, error) { return s.GrpcAdmin(ctx, c) },
		func() (*grpc.Server, error) { return s.GrpcControl(ctx, c) },
	} {
		g, err := build()
		x.NoError(err)
		x.NotNil(g)
		served = append(served, g)
	}

	// What the schema declares personal: the messages the interceptor reads,
	// and what their personal fields are called and hold.
	declared := map[protoreflect.FullName]bool{}
	var personal []protoreflect.FieldDescriptor
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		ms := fd.Messages()
		for i := range ms.Len() {
			fs := ms.Get(i).Fields()
			for j := range fs.Len() {
				v, _ := proto.GetExtension(fs.Get(j).Options(), pdpb.E_Field).(*pdpb.Field)
				if v.GetPersonal() {
					declared[ms.Get(i).FullName()] = true
					personal = append(personal, fs.Get(j))
				}
			}
		}
		return true
	})
	x.Contains(declared, protoreflect.FullName("roster.Identity"))

	// An entity's own row is not a copy of another's: what it holds of anybody
	// is declared on it, and a tenant's `alias` is not a person's.
	entity := func(m protoreflect.MessageDescriptor) bool {
		_, ok := reflect.TypeOf(ent.Client{}).FieldByName(string(m.Name()))
		return ok
	}
	// A message that is somebody's data wherever it is held: a profile, a
	// picture. Not an `Any` or a map, which are anything's.
	theirs := func(m protoreflect.MessageDescriptor) bool {
		for _, p := range personal {
			if p.Message() != nil && !p.IsMap() && p.Message().FullName() == m.FullName() && m.FullName().Parent() == "roster" {
				return true
			}
		}
		return false
	}
	carries := func(f protoreflect.FieldDescriptor) bool {
		if f.Message() != nil {
			return !f.IsMap() && theirs(f.Message())
		}
		for _, p := range personal {
			if p.Message() == nil && p.Name() == f.Name() && p.Kind() == f.Kind() {
				return true
			}
		}
		return false
	}

	got := map[string][]string{}
	for _, g := range served {
		for name, info := range g.GetServiceInfo() {
			d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
			x.NoError(err, name)
			sd := d.(protoreflect.ServiceDescriptor)

			for _, mi := range info.Methods {
				method := "/" + name + "/" + mi.Name

				seen := map[protoreflect.FullName]bool{}
				var walk func(m protoreflect.MessageDescriptor)
				walk = func(m protoreflect.MessageDescriptor) {
					if seen[m.FullName()] || declared[m.FullName()] {
						return
					}
					seen[m.FullName()] = true

					fs := m.Fields()
					for i := range fs.Len() {
						f := fs.Get(i)
						if !entity(m) && carries(f) {
							at := string(m.Name()) + "." + string(f.Name())
							if !slices.Contains(got[at], method) {
								got[at] = append(got[at], method)
							}
							continue
						}
						if f.Message() != nil {
							walk(f.Message())
						}
					}
				}
				walk(sd.Methods().ByName(protoreflect.Name(mi.Name)).Output())
			}
		}
	}

	for at, by := range got {
		slices.Sort(by)
		w, ok := want[at]
		x.True(ok, "%s is answered by %v under a personal field's name, in a message the access record does not read: "+
			"say here what is done about it", at, by)
		slices.Sort(w.by)
		x.Equal(w.by, by, "%s: which RPCs answer it", at)
		x.NotEmpty(w.why, at)
	}
	for at := range want {
		x.Contains(got, at, "%s is named here and no answer carries it any more", at)
	}
}
