package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

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
	b := keyFor(t, "/roster.AuditService/List")
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
// So `roster key add` says it once, at the moment there is still a choice. Not
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

			v := cmd.Widest(tc.methods)
			if !tc.said {
				x.Empty(v, "a key that does not reach the trail was warned about")

				return
			}

			x.NotEmpty(v, "a key that reads every tenant's history was minted quietly")
			x.Contains(v, "audit trail")
		})
	}
}
