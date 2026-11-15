package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// handedOver asks `at` what `methods` hand over where they would be `held`,
// and answers by which grant.
func handedOver(t *testing.T, ctx context.Context, at app.Server, held app.RoleHeld, methods ...string) map[string]*app.RoleHandedOver {
	t.Helper()

	v, err := at.Role().HandsOver(ctx, app.RoleHandsOverRequest_builder{Methods: methods, Held: held}.Build())
	require.NoError(t, err)

	out := map[string]*app.RoleHandedOver{}
	for _, g := range v.GetGrants() {
		require.NotContains(t, out, g.GetGrant(), "one grant was answered twice")
		require.NotEmpty(t, g.GetReadsAs(), g.GetGrant())
		require.NotEmpty(t, g.GetHandsOver(), g.GetGrant())
		require.NotEmpty(t, g.GetMethods(), g.GetGrant())

		out[g.GetGrant()] = g
	}

	return out
}

// TestAPageIsToldWhatAGrantHandsOver: `RoleService/HandsOver` answers, for a
// list of methods and where they would be held, which of them hand over more
// than they read as -- `docs/usage/wide-grants.md`, asked of one list.
//
// # What it was
//
// The page existed and nothing a program could read did. `roster key add`
// printed a note for three of its grants, from a Go constant; of a role or a
// binding, where most of them are made, nothing said anything, and a product's
// own administration had the list to copy (#310). The owner's case was two
// colleagues bound a role naming `ApiKeyService/Issue` so that each could mint
// their own keys, and each minting one on the other's account: nothing at the
// moment of binding said that is what the role was (#274).
//
// The rules each sentence stands on are tested where they are written, and
// named beside the sentence in `server/core/handsover.go`.
func TestAPageIsToldWhatAGrantHandsOver(t *testing.T) {
	b, ctx := build(t)

	const (
		tenant = app.RoleHeld_ROLE_HELD_ACROSS_THE_TENANT
		site   = app.RoleHeld_ROLE_HELD_IN_A_SITE
		own    = app.RoleHeld_ROLE_HELD_NOWHERE
	)

	t.Run("a list that means what it says is answered nothing", func(t *testing.T) {
		x := require.New(t)

		for _, held := range []app.RoleHeld{tenant, site, own} {
			x.Empty(handedOver(t, ctx, b.Ungated, held,
				app.HolderService_Get_FullMethodName,
				app.HolderService_List_FullMethodName,
				app.TeamService_Add_FullMethodName,
				app.VouchService_Redeem_FullMethodName,
				app.EmailService_Confirm_FullMethodName,
			), held)
		}
	})

	t.Run("a person's verb across the tenant is every colleague who holds no more", func(t *testing.T) {
		x := require.New(t)

		got := handedOver(t, ctx, b.Ungated, tenant,
			app.HolderService_Get_FullMethodName,
			app.ApiKeyService_Issue_FullMethodName,
			app.EmailService_Add_FullMethodName,
		)
		x.Len(got, 1)

		g := got["a-persons-verb-across-the-tenant"]
		x.NotNil(g)
		x.Equal([]string{app.ApiKeyService_Issue_FullMethodName, app.EmailService_Add_FullMethodName}, g.GetMethods(),
			"which of the methods asked about, and not the one that means what it says")
		x.Contains(g.GetHandsOver(), "anybody of the tenant who holds no more")
		x.Contains(g.GetHandsOver(), "each other")
	})

	t.Run("and in a site, the people of that site", func(t *testing.T) {
		x := require.New(t)

		g := handedOver(t, ctx, b.Ungated, site, app.CredentialService_Issue_FullMethodName)["a-persons-verb-across-the-tenant"]
		x.NotNil(g)
		x.Contains(g.GetHandsOver(), "anybody of that site who holds no more")
		x.NotContains(g.GetHandsOver(), "of the tenant")
	})

	t.Run("what is the tenant's as a whole hands over nothing in a site", func(t *testing.T) {
		x := require.New(t)

		// Each is refused, or answers only the caller's own, to somebody who
		// holds it in a site and no wider (`tenantwide.go`, `ofTheWholeTenant`,
		// `mayReadOf`): bound there it is a line of a role that does nothing,
		// and a warning beside it is one that teaches nobody to read the next.
		whole := map[string]string{
			"nominationserviceadd":     app.NominationService_Add_FullMethodName,
			"groupexposureserviceadd":  app.GroupExposureService_Add_FullMethodName,
			"sitemembershipserviceadd": app.SiteMembershipService_Add_FullMethodName,
			"auditservice":             app.AuditService_List_FullMethodName,
			"holderservicesignsin":     app.HolderService_SignsIn_FullMethodName,
		}
		for grant, method := range whole {
			x.Contains(handedOver(t, ctx, b.Ungated, tenant, method), grant, method)
			x.Empty(handedOver(t, ctx, b.Ungated, site, method), method)
		}
	})

	t.Run("a pattern is answered for everything it covers, as it was written", func(t *testing.T) {
		x := require.New(t)

		got := handedOver(t, ctx, b.Ungated, tenant, "/roster.*/*")
		for _, grant := range []string{
			"a-persons-verb-across-the-tenant",
			"vouchservicelink-with-redeem",
			"vouchserviceaccept",
			"emailserviceverify-with-confirm",
			"nominationserviceadd",
			"bindingserviceadd",
			"groupexposureserviceadd",
			"sitemembershipserviceadd",
			"a-pattern",
			"holderservicedisable",
			"auditservice",
			"holderservicesignsin",
		} {
			x.Contains(got, grant)
			x.Equal([]string{"/roster.*/*"}, got[grant].GetMethods(), grant)
		}
		x.Len(got, 12, "and nothing that is another plane's, or somebody's own")

		// A service's, for what is that service's and no more.
		got = handedOver(t, ctx, b.Ungated, tenant, "/roster.VouchService/*")
		x.Len(got, 3)
		x.Contains(got, "vouchservicelink-with-redeem")
		x.Contains(got, "vouchserviceaccept")
		x.Contains(got, "a-pattern")
		x.Contains(got["a-pattern"].GetHandsOver(), "a later release adds")
	})

	t.Run("the two a front door mails a link by are said of the one that mints", func(t *testing.T) {
		x := require.New(t)

		got := handedOver(t, ctx, b.Ungated, tenant, app.VouchService_Link_FullMethodName)
		x.Contains(got["vouchservicelink-with-redeem"].GetHandsOver(), "every account of the tenant")
		x.Contains(got["vouchservicelink-with-redeem"].GetHandsOver(), "`VouchService/Redeem`")

		got = handedOver(t, ctx, b.Ungated, tenant, app.EmailService_Verify_FullMethodName)
		x.Contains(got["emailserviceverify-with-confirm"].GetHandsOver(), "no mailbox")
	})

	t.Run("what is somebody's own reaches nobody else", func(t *testing.T) {
		x := require.New(t)

		// The tenant's `self-service` role names these, and holds them nowhere:
		// over the caller's own rows. A page drawing that role is told nothing
		// of colleagues, since none is reached (`TestWhatIsSomebodysOwnReachesNobodyElse`).
		x.Empty(handedOver(t, ctx, b.Ungated, own,
			app.ApiKeyService_Issue_FullMethodName,
			app.EmailService_Add_FullMethodName,
			app.CredentialService_Enrol_FullMethodName,
		))

		// One of them is worth knowing even so.
		got := handedOver(t, ctx, b.Ungated, own, app.IdentityService_Add_FullMethodName)
		x.Len(got, 1)
		x.Contains(got["the-self-service-role"].GetHandsOver(), "no proof")
	})

	t.Run("where it is not said, it is across the tenant", func(t *testing.T) {
		x := require.New(t)

		said := handedOver(t, ctx, b.Ungated, tenant, "/roster.*/*")
		unsaid := handedOver(t, ctx, b.Ungated, app.RoleHeld_ROLE_HELD_UNSPECIFIED, "/roster.*/*")
		x.Len(unsaid, len(said))
		for grant, g := range said {
			x.Equal(g.GetHandsOver(), unsaid[grant].GetHandsOver(), grant)
		}
	})

	t.Run("and a list of nothing is a mistake", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Role().HandsOver(ctx, app.RoleHandsOverRequest_builder{}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	})
}

// TestARoleOfTheDeploymentsOwnIsToldOfEveryTenant: asked on the control plane,
// the answer is a deployment's -- every tenant -- since that is where an
// operator's role and a deployment's key are exercised.
//
// The same reason `roster control key add` and `roster key add` print
// different notes (`TestAKeyIsToldWhatItsOwnKindReads`): a sentence written
// for one kind and said of the other is wrong every time it is said of the
// other.
func TestARoleOfTheDeploymentsOwnIsToldOfEveryTenant(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	ours := handedOver(t, ctx, b.Server.Control.Ungated, app.RoleHeld_ROLE_HELD_ACROSS_THE_TENANT, "/roster.*/*")
	theirs := handedOver(t, ctx, b.Server.Ungated, app.RoleHeld_ROLE_HELD_ACROSS_THE_TENANT, "/roster.*/*")

	// A person's verb is what `admin.addr` waives the rule about, and is said
	// as that.
	x.NotContains(ours, "a-persons-verb-across-the-tenant")
	x.Contains(ours["the-same-verbs-at-adminaddr"].GetHandsOver(), "any account of any tenant")
	x.NotContains(theirs, "the-same-verbs-at-adminaddr")

	x.Contains(ours["auditservice"].GetHandsOver(), "every tenant")
	x.NotContains(theirs["auditservice"].GetHandsOver(), "every tenant")

	x.Contains(ours["vouchservicelink-with-redeem"].GetHandsOver(), "every account of every tenant")

	// And nothing there says *the tenant* of a role that has none.
	for grant, g := range ours {
		x.NotContains(g.GetHandsOver(), "of the tenant", grant)
		x.NotContains(g.GetHandsOver(), "that site", grant)
	}

	// Where it would be held is a customer's question, and is not read here.
	in := handedOver(t, ctx, b.Server.Control.Ungated, app.RoleHeld_ROLE_HELD_IN_A_SITE, "/roster.*/*")
	x.Len(in, len(ours))
}

// TestWhatAGrantHandsOverIsARolesToAsk: it is a method like another, and a
// role names it. It reads nothing of anybody, and is still not everybody's:
// RBAC stays what it is.
func TestWhatAGrantHandsOverIsARolesToAsk(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	token := mintFor(t, ctx, b, b.Who, "page", []string{"/roster.*/*"}, time.Time{})
	c := app.NewRoleServiceClient(b.Conn)
	req := app.RoleHandsOverRequest_builder{Methods: []string{app.AuditService_List_FullMethodName}}.Build()

	_, err := c.HandsOver(bearing(ctx, token), req)
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

	mayList(t, ctx, b, b.Who, app.RoleService_HandsOver_FullMethodName)

	v, err := c.HandsOver(bearing(ctx, token), req)
	x.NoError(err)
	x.Len(v.GetGrants(), 1)
	x.Equal("auditservice", v.GetGrants()[0].GetGrant())
}
