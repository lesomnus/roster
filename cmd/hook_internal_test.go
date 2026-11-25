package cmd

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/server/pd"
)

// TestNoSecretOfARequestReachesAHook holds [hookSecrets] to every request a
// port serves: a field of one whose name says it holds a secret is cleared
// before a hook is told the request -- by payday, where the schema declares
// the field of the entity a generated request writes, and by roster's list
// for a hand-written one -- or is named here as no secret, with why.
//
// payday clears what the schema declares and nothing else, so a verb whose
// request carries a password, a PIN or a token is told to a hook whole unless
// it is said; and a verb added tomorrow is one nobody remembered to say.
func TestNoSecretOfARequestReachesAHook(t *testing.T) {
	secretly := regexp.MustCompile(`(^|_)(secret|password|pin|token|current|seed|verifier)$`)

	// Fields whose names sound like a secret's and are not.
	notSecret := map[string]string{
		"roster.TenantConfig.password":           "whether a tenant takes passwords, not one",
		"roster.HostProofAddRequest.token":       "the one token stored as it is compared, for the tenant to publish (`host.proto`)",
		"roster.ConnectionAddRequest.secret_ref": "where the deployment keeps a secret, not the secret",
	}

	schema := func(msg protoreflect.MessageDescriptor, f protoreflect.FieldDescriptor) bool {
		name := string(msg.Name())
		for _, sfx := range []string{"AddRequest", "PatchRequest", "Ref"} {
			entity, ok := strings.CutSuffix(name, sfx)
			if !ok {
				continue
			}
			d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName("roster." + entity))
			if err != nil {
				continue
			}
			row := d.(protoreflect.MessageDescriptor).Fields().ByName(f.Name())
			if row == nil {
				continue
			}
			v, _ := proto.GetExtension(row.Options(), pdpb.E_Field).(*pdpb.Field)
			if v.GetSecret() {
				return true
			}
		}

		return false
	}

	seen := map[protoreflect.FullName]bool{}
	var walk func(protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		if seen[md.FullName()] || md.FullName().Parent() != "roster" {
			return
		}
		seen[md.FullName()] = true
		fs := md.Fields()
		for i := range fs.Len() {
			f := fs.Get(i)
			if f.Message() != nil {
				walk(f.Message())
				continue
			}
			if f.Kind() != protoreflect.StringKind && f.Kind() != protoreflect.BytesKind {
				continue
			}
			if !secretly.MatchString(string(f.Name())) {
				continue
			}
			full := string(md.FullName()) + "." + string(f.Name())
			if _, ok := notSecret[full]; ok {
				continue
			}
			if slices.Contains(hookSecrets[md.FullName()], f.Number()) || schema(md, f) {
				continue
			}
			t.Errorf("%s holds a secret by its name, and nothing clears it from what a hook is told: "+
				"add it to hookSecrets, or name it here as no secret", full)
		}
	}

	for _, m := range Served(pd.Sink{}) {
		svc, name, _ := strings.Cut(strings.TrimPrefix(m, "/"), "/")
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svc))
		require.NoError(t, err, m)
		walk(d.(protoreflect.ServiceDescriptor).Methods().ByName(protoreflect.Name(name)).Input())
	}
	require.NotEmpty(t, seen)
}

// TestTheHooksConnectionIsClosedWithTheServers: what `Build` dialled is given
// back by what gives back the rest. It was dialled and never closed, so every
// server a process built and closed left a connection behind.
func TestTheHooksConnectionIsClosedWithTheServers(t *testing.T) {
	x := require.New(t)
	drv, dsn := pdtest.DB(t)

	c := Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Hook:  HookConfig{Addr: "127.0.0.1:9", Insecure: true, Writes: []string{"/roster.GroupMembershipService/*"}},
	}
	s, err := Build(t.Context(), c)
	x.NoError(err)
	x.NotNil(s.hook)
	x.NotEqual(connectivity.Shutdown, s.hook.GetState())

	x.NoError(s.Close())
	x.Equal(connectivity.Shutdown, s.hook.GetState(), "the hook's connection outlived the servers")
}
