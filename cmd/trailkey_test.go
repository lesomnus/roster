package cmd_test

import (
	"os"
	"testing"
	"time"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// saidOf is what a command that mints said beside the key, and not the key:
// stderr, with what it printed thrown away.
func saidOf(t *testing.T, k *xli.Command, args ...string) string {
	t.Helper()

	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, err)
	defer null.Close()

	was := os.Stdout
	os.Stdout = null
	defer func() { os.Stdout = was }()

	said, err := stderrOf(t, k, args...)
	require.NoError(t, err, said)

	return said
}

// TestADeploymentKeyReadsEveryTenantsTrail.
//
// Not a hole, and worth a test precisely because it is not: `cmd.Policy.Where`
// answers `frame.Everything` for an `rk_` key, deliberately -- *a key belongs to
// the deployment and the deployment is every tenant in it. What narrows it is
// its methods, not its tenants.* A key allowed `/roster.HolderService/List`
// already reads every customer's people, and nobody would call that a defect.
//
// The trail is the same property with a different magnitude, and that is the
// part somebody should be told rather than discover. `Audit.value` is the row
// as each write left it, so this one method answers **every table's contents,
// across every tenant, across all time** -- including rows long since deleted.
// It is the single widest read this app has, and no role can reach it that way:
// a person is narrowed to their own tenant, so the same method on a holder is a
// different question entirely.
//
// Asserted rather than described, so that a change making it narrower is a
// failing test somebody has to think about, and a change making it wider is
// impossible.
func TestADeploymentKeyReadsEveryTenantsTrail(t *testing.T) {
	x := require.New(t)
	// Keeping rows, since what is under test is that the contents come back
	// (payday#92: the default keeps what a write said and not the row).
	b := keyedFor(t, keepingRows, "/roster.AuditService/List")
	ctx := t.Context()

	// A second customer, with writes of its own. The harness makes `contoso`; this
	// is the one the key has no relationship with at all.
	fabrikam := add(t, ctx, b.Server, "fabrikam")
	addHolder(t, ctx, b.Server, fabrikam, "somebody-else")

	c := app.NewAuditServiceClient(b.Conn)

	res, err := c.List(bearing(ctx, b.Token), app.AuditListRequest_builder{}.Build())
	x.NoError(err)
	x.NotEmpty(res.GetItems())

	tenants := map[pdid.Id]bool{}
	held := 0
	for _, v := range res.GetItems() {
		if len(v.GetTenantId()) > 0 {
			tenants[mustId(t, v.GetTenantId())] = true
		}
		if len(v.GetValue()) > 0 {
			held++
		}
	}

	x.Contains(tenants, b.Contoso)
	x.Contains(tenants, fabrikam, "a deployment key saw one tenant's trail and not another's")
	x.NotZero(held, "the rows came back without their contents, which nothing here does")

	t.Run("and a person sees only their own", func(t *testing.T) {
		x := require.New(t)

		// The same read as somebody inside a tenant, allowed everything a role
		// can allow. The wall is what differs, and it is the whole of the
		// difference.
		as := frame.Into(ctx, frame.New(b.Who, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))

		vs, err := b.Walled.Audit().List(as, app.AuditListRequest_builder{}.Build())
		x.NoError(err)
		x.NotEmpty(vs.GetItems())

		for _, v := range vs.GetItems() {
			if len(v.GetTenantId()) == 0 {
				continue
			}

			x.NotEqual(fabrikam, mustId(t, v.GetTenantId()),
				"a person read another customer's trail")
		}
	})
}

// TestMintingAKeyThatReachesTheTrailSaysSo.
//
// The read above is the design working, and the failure mode is not the design
// -- it is somebody reaching for `--allow '/roster.*/*'` because writing out
// eleven methods is tedious, and not noticing which one of them is the whole
// deployment's history.
//
// So `roster control key add` says it once, at the moment there is still a choice. Not
// a refusal: a compliance exporter is a real service and this is the method it
// needs.
func TestMintingAKeyThatReachesTheTrailSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		methods []string
		said    bool
	}{
		{name: "everything", methods: []string{"/roster.*/*"}, said: true},
		{name: "the service", methods: []string{"/roster.AuditService/*"}, said: true},
		{name: "the method itself", methods: []string{"/roster.AuditService/List"}, said: true},
		{name: "one Get", methods: []string{"/roster.AuditService/Get"}, said: true},
		{name: "a sign-in service", methods: []string{"/roster.VouchService/Verify"}},
		{name: "every holder read", methods: []string{"/roster.HolderService/*"}},
		{name: "nothing at all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			v := cmd.Widest(keys.PrefixDeployment, tc.methods)
			if !tc.said {
				x.Empty(v, "a key that does not reach the trail was warned about")

				return
			}

			x.NotEmpty(v, "a key that reads every tenant's history was minted quietly")
			x.Contains(v, "audit trail")
		})
	}
}

// TestAKeyIsToldWhatItsOwnKindReads: the note is about the key that was just
// minted, and what that key reaches is its kind's.
//
// # What it was
//
// One sentence for every key: *every write in this deployment, in every
// tenant ... A key is not walled by tenant ... other customers' history*. That
// is a deployment's key (`rk_`), which no wall narrows. `roster key add` mints
// the other kind -- a key for one of a customer's people (`rt_`), which is
// answered as that person, behind their tenant's wall -- and printed the same
// sentence. So an administrator minting a key for their own tenant's exporter
// was told it read every customer's history, which it cannot; and a sentence
// that is wrong half the time it is printed is one nobody reads the other half
// (#274).
//
// A deployment's key that links was told the opposite way: *every account of
// the tenant*, of a key that has no tenant.
func TestAKeyIsToldWhatItsOwnKindReads(t *testing.T) {
	trail := []string{"/roster.*/*"}

	t.Run("a deployment's is told of every tenant", func(t *testing.T) {
		x := require.New(t)

		v := cmd.Widest(keys.PrefixDeployment, trail)
		x.Contains(v, "in every tenant")
		x.Contains(v, "other customers' history")

		v = cmd.Widest(keys.PrefixDeployment, []string{link})
		x.Contains(v, "every account of every tenant")
	})

	t.Run("a customer's is told of its tenant", func(t *testing.T) {
		x := require.New(t)

		// A key and a delegation both: each is answered as somebody of a
		// tenant, and reads what that person does.
		for _, kind := range []string{keys.PrefixTenant, keys.PrefixDelegation} {
			v := cmd.Widest(kind, trail)
			x.Contains(v, "the audit trail of its tenant", kind)
			x.NotContains(v, "every tenant", kind)
			x.NotContains(v, "other customers", kind)
			x.NotContains(v, "not walled", kind)

			v = cmd.Widest(kind, []string{link})
			x.Contains(v, "every account of the tenant", kind)
		}
	})

	t.Run("and what was minted says which, as it begins", func(t *testing.T) {
		x := require.New(t)

		// Whoever mints over the wire holds a token and not a prefix, and the
		// plane that answered decided the kind.
		theirs, _, err := keys.Mint(keys.PrefixTenant)
		x.NoError(err)
		x.Equal(cmd.Widest(keys.PrefixTenant, trail), cmd.Widest(theirs, trail))

		ours, _, err := keys.Mint(keys.PrefixDeployment)
		x.NoError(err)
		x.Equal(cmd.Widest(keys.PrefixDeployment, trail), cmd.Widest(ours, trail))

		x.NotEqual(cmd.Widest(ours, trail), cmd.Widest(theirs, trail))
	})

	t.Run("and each command says its own", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()

		c := seedbed(t)
		out, err := initRun(t, c)
		x.NoError(err, "init: %s", out)

		// A customer, and the `admin` a tenant arrives with.
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		_, err = s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "newco"}.Build())
		x.NoError(err)
		x.NoError(s.Close())

		said := saidOf(t, cli.NewCmdKey(&c), "add",
			"--tenant", "newco", "--holder", "admin", "--allow", "/roster.*/*")
		x.Contains(said, "the audit trail of its tenant")
		x.NotContains(said, "every tenant")

		said = saidOf(t, cli.NewCmdControl(&c), "key", "add", "--allow", "/roster.*/*", "exporter")
		x.Contains(said, "in every tenant")
		x.Contains(said, "A deployment's key is not walled by tenant")
	})

	// Each sentence is held to what a served deployment does. The deployment's
	// is [TestADeploymentKeyReadsEveryTenantsTrail]; these are the other two.
	t.Run("a customer's key reads its tenant's trail and nobody else's", func(t *testing.T) {
		x := require.New(t)
		b := keyedFor(t, keepingRows)
		ctx := t.Context()

		fabrikam := add(t, ctx, b.Server, "fabrikam")
		addHolder(t, ctx, b.Server, fabrikam, "somebody-else")

		// Somebody of contoso allowed the read across their tenant, with a key
		// that lists everything a key can.
		mayList(t, ctx, b, b.Who, app.AuditService_List_FullMethodName)
		token := mintFor(t, ctx, b, b.Who, "exporter", trail, time.Time{})
		x.True(len(token) > len(keys.PrefixTenant) && token[:len(keys.PrefixTenant)] == keys.PrefixTenant)

		res, err := app.NewAuditServiceClient(b.Conn).List(bearing(ctx, token), app.AuditListRequest_builder{}.Build())
		x.NoError(err)
		x.NotEmpty(res.GetItems())

		for _, v := range res.GetItems() {
			x.Equal(b.Contoso, mustId(t, v.GetTenantId()), "a customer's key read another customer's trail")
		}
	})

	t.Run("a deployment's key links anybody of any tenant", func(t *testing.T) {
		x := require.New(t)
		b := keyFor(t, link)
		ctx := t.Context()

		fabrikam := add(t, ctx, b.Server, "fabrikam")
		other := addHolder(t, ctx, b.Server, fabrikam, "somebody-else")

		for _, who := range []pdid.Id{b.Who, other} {
			made, err := app.NewVouchServiceClient(b.Conn).Link(bearing(ctx, b.Token), app.VouchLinkRequest_builder{
				Who: app.VouchWho_builder{Id: who.Bytes()}.Build(),
			}.Build())
			x.NoError(err)
			x.NotEmpty(made.GetToken())
		}
	})
}
