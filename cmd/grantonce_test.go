package cmd_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// TestAGrantIsMadeOnce: a binding of one role to one person or one group in
// one place is one row, and making it again is refused.
//
// # What it was
//
// A binding declared no uniqueness, and an index could not have held it: a
// binding names a holder or a group and a site or none, and SQL reads two
// nulls as different. So the same binding added twice was two rows, and after
// one was erased the person was still answered -- somebody who takes a grant
// away, and sees it gone from the list they erased it from, has not taken it
// away (#279).
//
// It is held where the write is made, as a rule about other rows is
// (`server/core/only.go`): looked for and written in one transaction, after a
// write both callers make, so that two at once are one asking twice.
func TestAGrantIsMadeOnce(t *testing.T) {
	b, ctx := build(t)

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	admin := asOverTheWire(ctx, b.ContosoUser)
	bindings := app.NewBindingServiceClient(conn)

	seoul, busan := b.site(t, ctx, b.Contoso, "seoul"), b.site(t, ctx, b.Contoso, "busan")
	reader := roleRef(b.role(t, ctx, "reader", app.TeamService_List_FullMethodName))
	writer := roleRef(b.role(t, ctx, "writer", app.TeamService_Add_FullMethodName))
	park, choi := b.holder(t, ctx, b.Contoso, "park"), b.holder(t, ctx, b.Contoso, "choi")
	g, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "auditors",
	}.Build())
	require.NoError(t, err)
	auditors := mustId(t, g.GetId())

	// A binding of a role to a person or a group, in a site or in none.
	binding := func(role *app.RoleRef, to pdid.Id, site *pdid.Id) *app.BindingAddRequest {
		req := app.BindingAddRequest_builder{Role: role}
		if to == auditors {
			req.Group = app.GroupRef_builder{Id: to.Bytes()}.Build()
		} else {
			req.Holder = app.HolderRef_builder{Id: to.Bytes()}.Build()
		}
		if site != nil {
			req.Site = app.SiteRef_builder{Id: site.Bytes()}.Build()
		}

		return req.Build()
	}
	lists := func(who pdid.Id) codes.Code {
		_, err := app.NewTeamServiceClient(conn).List(asOverTheWire(ctx, who), app.TeamListRequest_builder{}.Build())

		return status.Code(err)
	}

	t.Run("the same binding a second time is refused", func(t *testing.T) {
		x := require.New(t)
		for _, req := range []*app.BindingAddRequest{
			binding(reader, park, nil),
			binding(reader, park, &seoul),
			binding(reader, auditors, nil),
			binding(reader, auditors, &busan),
		} {
			first, err := bindings.Add(admin, req)
			x.NoError(err)

			_, err = bindings.Add(admin, req)
			x.Equal(codes.AlreadyExists, status.Code(err), "the same binding was made twice: %v", err)

			// And at the box, where nobody is asked what they hold: it is
			// about the row.
			_, err = b.Ungated.Binding().Add(ctx, req)
			x.Equal(codes.AlreadyExists, status.Code(err), "%v", err)

			// Taken away, it is one that may be made again.
			_, err = bindings.Erase(admin, app.BindingRef_builder{Id: first.GetId()}.Build())
			x.NoError(err)
			_, err = bindings.Add(admin, req)
			x.NoError(err, "a binding erased is one that may be made again")
		}
	})

	t.Run("so one taken away is taken away", func(t *testing.T) {
		x := require.New(t)
		req := binding(reader, choi, nil)

		first, err := bindings.Add(admin, req)
		x.NoError(err)
		_, _ = bindings.Add(admin, req)
		x.Equal(codes.OK, lists(choi))

		_, err = bindings.Erase(admin, app.BindingRef_builder{Id: first.GetId()}.Build())
		x.NoError(err)
		x.Equal(codes.PermissionDenied, lists(choi), "the grant was erased and its holder is still answered")
	})

	t.Run("another role, another place and somebody else are other grants", func(t *testing.T) {
		x := require.New(t)
		lee := b.holder(t, ctx, b.Contoso, "lee")
		for _, req := range []*app.BindingAddRequest{
			binding(reader, lee, nil),
			binding(reader, lee, &seoul),
			binding(reader, lee, &busan),
			binding(writer, lee, nil),
			binding(writer, lee, &seoul),
		} {
			_, err := bindings.Add(admin, req)
			x.NoError(err)
		}
	})

	t.Run("and two callers at once make it once", func(t *testing.T) {
		x := require.New(t)

		const people = 20
		twice := 0
		for i := range people {
			who := b.holder(t, ctx, b.Contoso, fmt.Sprintf("person-%d", i))
			req := binding(writer, who, nil)
			if i%2 == 1 {
				req = binding(writer, who, &seoul)
			}

			var wg sync.WaitGroup
			errs := make([]error, 2)
			start := make(chan struct{})
			for n := range errs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, errs[n] = bindings.Add(admin, req)
				}()
			}
			close(start)
			wg.Wait()

			x.True(errs[0] == nil || errs[1] == nil, "neither went through: %v", errs)

			made, err := b.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
				Filters: []*app.BindingFilter{app.BindingFilter_builder{
					Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(), Role: writer,
				}.Build()},
			}.Build())
			x.NoError(err)
			if len(made.GetItems()) != 1 {
				twice++
			}
		}

		x.Zero(twice, "%d of %d bindings were made twice", twice, people)
	})
}

// TestATeamOfNoSiteIsNamedOnceInItsTenant: a team's name is unique within its
// site (`team.proto`), and a team of no site is the tenant's.
//
// # What it was
//
// The index that holds a team's name is over the name and the site, and binds
// nothing where the site is null. So two teams `board` of no site were both
// made in one tenant, and a team of no site is named by nothing but its
// identifier as it is (#279). Its name is held within its tenant, among the
// teams of no site, as a binding is held to one row: where the write is made.
func TestATeamOfNoSiteIsNamedOnceInItsTenant(t *testing.T) {
	b, ctx := build(t)

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	admin := asOverTheWire(ctx, b.ContosoUser)
	teams := app.NewTeamServiceClient(conn)

	contoso := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	seoul := app.SiteRef_builder{Id: b.site(t, ctx, b.Contoso, "seoul").Bytes()}.Build()

	t.Run("a second of the name is refused, however it is spelled", func(t *testing.T) {
		x := require.New(t)

		board, err := teams.Add(admin, app.TeamAddRequest_builder{Tenant: contoso, Alias: "board"}.Build())
		x.NoError(err)

		for _, alias := range []string{"board", " Board "} {
			_, err = teams.Add(admin, app.TeamAddRequest_builder{Tenant: contoso, Alias: alias}.Build())
			x.Equal(codes.AlreadyExists, status.Code(err), "a second team %q of no site: %v", alias, err)

			_, err = b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{Tenant: contoso, Alias: alias}.Build())
			x.Equal(codes.AlreadyExists, status.Code(err), "at the box: %v", err)
		}

		// A site's `board` is another name, and so is another tenant's.
		_, err = teams.Add(admin, app.TeamAddRequest_builder{Tenant: contoso, Site: seoul, Alias: "board"}.Build())
		x.NoError(err)
		_, err = b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(), Alias: "board",
		}.Build())
		x.NoError(err)

		// And the name of a team that was erased is free.
		_, err = teams.Erase(admin, app.TeamRef_builder{Id: board.GetId()}.Build())
		x.NoError(err)
		_, err = teams.Add(admin, app.TeamAddRequest_builder{Tenant: contoso, Alias: "board"}.Build())
		x.NoError(err)
	})

	t.Run("and two callers at once make one", func(t *testing.T) {
		x := require.New(t)

		const names = 20
		twice := 0
		for i := range names {
			alias := fmt.Sprintf("council-%d", i)

			var wg sync.WaitGroup
			errs := make([]error, 2)
			start := make(chan struct{})
			for n := range errs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, errs[n] = teams.Add(admin, app.TeamAddRequest_builder{Tenant: contoso, Alias: alias}.Build())
				}()
			}
			close(start)
			wg.Wait()

			x.True(errs[0] == nil || errs[1] == nil, "neither went through: %v", errs)
			if errs[0] == nil && errs[1] == nil {
				twice++
			}
		}

		x.Zero(twice, "%d of %d names were given to two teams of no site", twice, names)
	})
}

// TestAnOperatorMakesAGrantOnceToo is the two tests above at the operators'
// port, where what a caller holds is the control plane's to say and whether a
// customer's row is there already is the data plane's (`adminRules`): asked
// of the control plane, no customer has a binding or a team, and an operator
// made each twice.
func TestAnOperatorMakesAGrantOnceToo(t *testing.T) {
	x := require.New(t)
	s, c, out := adminDeployment(t, nil)
	conn, as := adminPort(t, s, c, out)

	who := app.HolderRef_builder{Id: adminCustomer(t, conn, as, "contoso")}.Build()
	of, err := app.NewHolderServiceClient(conn).Get(as, app.HolderGetRequest_builder{
		Ref: who, Select: app.HolderSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	tenant := app.TenantRef_builder{Id: of.GetTenant().GetId()}.Build()

	role, err := app.NewRoleServiceClient(conn).Add(as, app.RoleAddRequest_builder{
		Tenant: tenant, Alias: "reader", Methods: []string{app.TeamService_List_FullMethodName},
	}.Build())
	x.NoError(err)
	binding := app.BindingAddRequest_builder{Role: app.RoleRef_builder{Id: role.GetId()}.Build(), Holder: who}.Build()
	_, err = app.NewBindingServiceClient(conn).Add(as, binding)
	x.NoError(err)
	_, err = app.NewBindingServiceClient(conn).Add(as, binding)
	x.Equal(codes.AlreadyExists, status.Code(err), "the same binding was made twice: %v", err)

	team := app.TeamAddRequest_builder{Tenant: tenant, Alias: "board"}.Build()
	_, err = app.NewTeamServiceClient(conn).Add(as, team)
	x.NoError(err)
	_, err = app.NewTeamServiceClient(conn).Add(as, team)
	x.Equal(codes.AlreadyExists, status.Code(err), "a second team of the name in no site: %v", err)
}
