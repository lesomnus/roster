package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestASelectReadsAnEdgeByItsOwnRule: what a `select` reaches through an edge
// is read as the row at the other end is read by itself -- behind the wall,
// and within the sites the caller sees -- and a row they may not read is
// answered as its reference alone.
//
// # What it was
//
// A row a `Get` names is narrowed. The row at the other end of an edge its
// `select` asks for was loaded whole, with no scope (#274): the generated
// `Select`, which the generator has since mended -- it takes the context and
// the scope now, and reads each edge through the `Narrow` of what it reaches
// (`protoc-gen-orm-ent`). So somebody bound in one
// site, answered `NotFound` for a team and a role of another, read both --
// the role's methods with them -- through a membership that names them.
func TestASelectReadsAnEdgeByItsOwnRule(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	north := b.site(t, ctx, b.Contoso, "north")
	south := b.site(t, ctx, b.Contoso, "south")

	// What nora holds, in north alone.
	everything, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Site:    app.SiteRef_builder{Id: north.Bytes()}.Build(),
		Alias:   "north-admin",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)
	nora := b.holder(t, ctx, b.Contoso, "nora")
	b.binds(t, nora, mustId(t, everything.GetId()), &north)
	as := b.asNobody(ctx, nora, b.Contoso)

	// South's own: a team, a role that names what south's payroll runs, and
	// saul holding it in the team.
	team := b.team(t, ctx, south, "south-team")
	secret, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Site:    app.SiteRef_builder{Id: south.Bytes()}.Build(),
		Alias:   "south-secret",
		Methods: []string{"/ext.payroll.Admin/Run"},
		App:     app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "payroll").Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	saul := b.holder(t, ctx, b.Contoso, "saul")
	b.inTeam(t, ctx, saul, team, mustId(t, secret.GetId()))

	// Asked for by themselves, neither is there.
	_, err = b.Walled.Team().Get(as, app.TeamGetRequest_builder{Ref: app.TeamRef_builder{Id: team.Bytes()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err), "%v", err)
	_, err = b.Walled.Role().Get(as, app.RoleGetRequest_builder{Ref: app.RoleRef_builder{Id: secret.GetId()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err), "%v", err)

	// And through the membership that names them, nothing of either but that
	// it names them.
	ms, err := b.Walled.TeamMembership().List(as, app.TeamMembershipListRequest_builder{
		Filters: []*app.TeamMembershipFilter{app.TeamMembershipFilter_builder{
			Holder: app.HolderRef_builder{Id: saul.Bytes()}.Build(),
		}.Build()},
	}.Build())
	x.NoError(err)
	if len(ms.GetItems()) == 0 {
		// The membership itself out of reach is the stricter answer, and what
		// this is about is then not reachable at all.
		return
	}

	got, err := b.Walled.TeamMembership().Get(as, app.TeamMembershipGetRequest_builder{
		Ref: app.TeamMembershipRef_builder{Id: ms.GetItems()[0].GetId()}.Build(),
		Select: app.TeamMembershipSelect_builder{
			Team: app.TeamSelect_builder{All: z.Ptr(true), Site: app.SiteSelect_builder{All: z.Ptr(true)}.Build()}.Build(),
			Role: app.RoleSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build(),
	}.Build())
	x.NoError(err)

	x.Equal(team.Bytes(), got.GetTeam().GetId(), "the reference is the membership's own column")
	x.Empty(got.GetTeam().GetAlias(), "a team of another site was read through an edge")
	x.False(got.GetTeam().HasSite(), "and its site with it")
	x.Equal(secret.GetId(), got.GetRole().GetId())
	x.Empty(got.GetRole().GetAlias(), "a role of another site was read through an edge")
	x.Empty(got.GetRole().GetMethods(), "and what it allows")

	// Somebody who sees south reads the same edges whole.
	saw := b.holder(t, ctx, b.Contoso, "sees-everything")
	b.mayAnything(saw, b.Contoso)
	got, err = b.Walled.TeamMembership().Get(b.asNobody(ctx, saw, b.Contoso), app.TeamMembershipGetRequest_builder{
		Ref: app.TeamMembershipRef_builder{Id: ms.GetItems()[0].GetId()}.Build(),
		Select: app.TeamMembershipSelect_builder{
			Team: app.TeamSelect_builder{All: z.Ptr(true)}.Build(),
			Role: app.RoleSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("south-team", got.GetTeam().GetAlias())
	x.Equal([]string{"/ext.payroll.Admin/Run"}, got.GetRole().GetMethods())
}
