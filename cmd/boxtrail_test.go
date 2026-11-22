package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	app "github.com/lesomnus/roster/rstr"
)

// boxTrailed is what a plane's trail holds now, by line, for
// [boxActionsSince].
func boxTrailed(t *testing.T, c cmd.Config, control bool) map[pdid.Id]bool {
	t.Helper()

	out := map[pdid.Id]bool{}
	for _, v := range boxLines(t, c, control) {
		out[pdid.Id(v.Id)] = true
	}

	return out
}

func boxLines(t *testing.T, c cmd.Config, control bool) []*ent.Audit {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()

	db := s.Ent
	if control {
		db = s.Control.Ent
	}
	vs, err := db.Audit.Query().All(ctx)
	x.NoError(err)

	return vs
}

// boxActionsSince is what each line written on a plane since `seen` is called.
func boxActionsSince(t *testing.T, c cmd.Config, control bool, seen map[pdid.Id]bool) []string {
	t.Helper()

	out := []string{}
	for _, v := range boxLines(t, c, control) {
		if !seen[pdid.Id(v.Id)] {
			out = append(out, v.Action)
		}
	}

	return out
}

// TestWhatTheBoxDoesIsCalledWhatTheWireCallsIt: every line of the trail an act
// at the box writes is called what the same act is called over the wire, and
// an act with no RPC is called by a name of roster's own.
//
// A write made in process was filed under the generated method that wrote
// it: a reset at the box was `/roster.CredentialService/Patch` (`/Add` the
// first time) and `/roster.HolderService/Patch`, where over the wire it is
// `/roster.CredentialService/Issue` on every line; `vouch set` and `vouch
// unlock` were both `/Patch`, `hold place` and `hold lift` both
// `/roster.TenantService/Patch`, and `key add` `/roster.ApiKeyService/Add`
// where a key minted over the wire is `/Issue`. Answered: the generated
// method; right is the act, so that one name finds every reset.
func TestWhatTheBoxDoesIsCalledWhatTheWireCallsIt(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	var c *cmd.Config
	s, out := initedWith(t, func(v *cmd.Config) { c = v })
	conf := *c

	// Over the wire: an operator at the admin listener, which serves the
	// writes of a way in for a customer's person.
	cookie := signIn(t, s, "admin", passwordFrom(t, out))
	x.NotNil(cookie)
	g, err := s.GrpcAdmin(ctx, cmd.Config{})
	x.NoError(err)
	admin := pdtest.Serve(t, g)
	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", cookie.Name+"="+cookie.Value))

	tn, err := app.NewTenantServiceClient(admin).Add(as, app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)
	newco := app.TenantRef_builder{Id: tn.GetId()}.Build()
	for _, alias := range []string{"alice", "bob"} {
		_, err = app.NewHolderServiceClient(admin).Add(as, app.HolderAddRequest_builder{Tenant: newco, Alias: alias}.Build())
		x.NoError(err)
	}
	alice := app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: z.Ptr("alice"), Tenant: newco}.Build()}.Build()

	creds := app.NewCredentialServiceClient(admin)
	wire := map[string][]string{}
	overTheWire := func(act string, call func() error) {
		t.Helper()
		seen := boxTrailed(t, conf, false)
		x.NoError(call(), act)
		wire[act] = boxActionsSince(t, conf, false, seen)
		x.NotEmpty(wire[act], "%s over the wire writes no line, so this would prove nothing", act)
	}
	overTheWire("reset", func() error {
		_, err := creds.Issue(as, app.CredentialIssueRequest_builder{Holder: alice}.Build())
		return err
	})
	overTheWire("reset again", func() error {
		_, err := creds.Issue(as, app.CredentialIssueRequest_builder{Holder: alice}.Build())
		return err
	})
	overTheWire("unlock", func() error {
		_, err := creds.Unlock(as, app.CredentialUnlockRequest_builder{Holder: alice}.Build())
		return err
	})
	overTheWire("key", func() error {
		_, err := app.NewApiKeyServiceClient(admin).Issue(as, app.ApiKeyIssueRequest_builder{
			Holder: alice, Alias: "laptop", Methods: []string{"/roster.MeService/Get"},
		}.Build())
		return err
	})
	x.Equal([]string{app.CredentialService_Issue_FullMethodName}, boxUnique(wire["reset"]))
	x.Equal([]string{app.CredentialService_Issue_FullMethodName}, boxUnique(wire["reset again"]))
	x.Equal([]string{app.CredentialService_Unlock_FullMethodName}, boxUnique(wire["unlock"]))
	x.Equal([]string{app.ApiKeyService_Issue_FullMethodName}, boxUnique(wire["key"]))

	// `Set` over the wire is a person's own and nobody else's
	// (`server/core/credential.go`), so the box's is held to the RPC's name.
	atBox := func(control bool, stdin string, args ...string) []string {
		t.Helper()
		seen := boxTrailed(t, conf, control)
		_, said, err := boxRun(t, at(conf, "admin"), stdin, args...)
		x.NoError(err, "%v: %s", args, said)
		lines := boxActionsSince(t, conf, control, seen)
		x.NotEmpty(lines, "%v writes no line, so this would prove nothing", args)

		return lines
	}

	x.Equal(boxUnique(wire["reset"]), boxUnique(atBox(false, "", "vouch", "reset", "@newco/bob")), "the first reset of somebody")
	x.Equal(boxUnique(wire["reset again"]), boxUnique(atBox(false, "", "vouch", "reset", "@newco/bob")), "a reset of somebody with a password")
	x.Equal(boxUnique(wire["reset again"]), boxUnique(atBox(true, "", "control", "vouch", "reset", "@admin")), "an operator's reset")
	x.Equal([]string{app.CredentialService_Set_FullMethodName},
		boxUnique(atBox(false, "a-long-enough-password-for-bob\n", "vouch", "set", "--password-stdin", "@newco/bob")))
	x.Equal([]string{app.CredentialService_Set_FullMethodName},
		boxUnique(atBox(true, "a-long-enough-password-for-admin\n", "control", "vouch", "set", "--password-stdin", "@admin")))
	x.Equal(boxUnique(wire["unlock"]), boxUnique(atBox(false, "", "vouch", "unlock", "@newco/bob")))
	x.Equal(boxUnique(wire["unlock"]), boxUnique(atBox(true, "", "control", "vouch", "unlock", "@admin")))
	x.Equal(boxUnique(wire["key"]), boxUnique(atBox(false, "", "key", "add", "--tenant", "newco", "--holder", "bob", "--allow", "/roster.MeService/Get")))

	// The key's line is the mint; somebody made for it is a holder added.
	x.ElementsMatch([]string{app.HolderService_Add_FullMethodName, app.ApiKeyService_Issue_FullMethodName},
		boxUnique(atBox(true, "", "control", "key", "add", "--allow", "/roster.MeService/Get", "loft")))
	x.Equal([]string{app.ApiKeyService_Issue_FullMethodName},
		boxUnique(atBox(true, "", "control", "key", "add", "--name", "two", "--allow", "/roster.MeService/Get", "loft")))

	// What has no RPC is called by a name of roster's own.
	x.Equal([]string{"roster.hold.place"}, boxUnique(atBox(false, "", "hold", "place", "--why", "a claim", "@newco")))
	x.Equal([]string{"roster.hold.lift"}, boxUnique(atBox(false, "", "hold", "lift", "@newco")))

	_, _, err = boxRun(t, at(conf, "admin"), "", "tenant", "close", "@newco")
	x.NoError(err)
	freed := atBox(false, "", "forget", "--reuse", "@newco")
	x.Contains(freed, "roster.alias.free", "a tenant's alias freed at once")
	x.NotContains(freed, app.TenantService_Patch_FullMethodName)
}

// boxUnique is `vs` with each one once, in the order first seen.
func boxUnique(vs []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range vs {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}

	return out
}
