package cmd_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestASiteIsErasedWithNothingInItByTwoCallersAtOnce: a site is erased when
// nothing is in it (`TestASiteIsErasedWhenNothingIsInIt`), and that was one
// caller's rule.
//
// # What it was
//
// `Site.Erase` counted what is in the site and then erased it, with nothing
// between the two; and a team, a role, a group and a binding made in a site
// each read the site and then wrote. Two callers at once, one erasing a site
// and one putting a team in it, both went through about 130 times of 150 on
// SQLite: the site read `NotFound` and a team named it. With a binding it was
// the state the rule was written against -- somebody bound in a site nobody
// can see or administer, and still answered there (#279).
//
// A count of other rows decides nothing when two take it at once
// (`server/core/only.go`). So the count and the erase are one transaction,
// begun by a write on the site's own row, and whatever is put in a site makes
// that write first: whichever of the two comes second finds what the first
// left, and is refused in the words it would have been refused in a moment
// later -- the site still has something in it, or there is no such site.
func TestASiteIsErasedWithNothingInItByTwoCallersAtOnce(t *testing.T) {
	b, ctx := build(t)

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	admin := asOverTheWire(ctx, b.ContosoUser)
	contoso := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	reader := roleRef(b.role(t, ctx, "reader", app.TeamService_List_FullMethodName))
	who := app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "bound").Bytes()}.Build()

	const sites = 40
	in := cmd.Sited(b.Ent)

	for _, put := range []struct {
		kind string
		into func(ctx context.Context, site *app.SiteRef) error
	}{
		{"team", func(ctx context.Context, site *app.SiteRef) error {
			_, err := app.NewTeamServiceClient(conn).Add(ctx, app.TeamAddRequest_builder{Tenant: contoso, Site: site, Alias: "ops"}.Build())

			return err
		}},
		{"role", func(ctx context.Context, site *app.SiteRef) error {
			_, err := app.NewRoleServiceClient(conn).Add(ctx, app.RoleAddRequest_builder{
				Tenant: contoso, Site: site, Methods: []string{app.TeamService_List_FullMethodName},
			}.Build())

			return err
		}},
		{"group", func(ctx context.Context, site *app.SiteRef) error {
			_, err := app.NewGroupServiceClient(conn).Add(ctx, app.GroupAddRequest_builder{Tenant: contoso, Site: site}.Build())

			return err
		}},
		{"binding", func(ctx context.Context, site *app.SiteRef) error {
			_, err := app.NewBindingServiceClient(conn).Add(ctx, app.BindingAddRequest_builder{Role: reader, Holder: who, Site: site}.Build())

			return err
		}},
	} {
		t.Run("a "+put.kind+" put in it", func(t *testing.T) {
			x := require.New(t)

			lost := 0
			for i := range sites {
				s, err := app.NewSiteServiceClient(conn).Add(admin, app.SiteAddRequest_builder{
					Tenant: contoso, Alias: fmt.Sprintf("%s-%d", put.kind, i),
				}.Build())
				x.NoError(err)
				site := app.SiteRef_builder{Id: s.GetId()}.Build()

				var erased *app.SiteEraseResponse
				errs := answeredByTheRule(
					func() error {
						// What is put in a site is asked more before it is
						// written than the erase is, so the erase is begun a
						// little after it: by more each time.
						time.Sleep(time.Duration(i%20) * 200 * time.Microsecond)
						var err error
						erased, err = app.NewSiteServiceClient(conn).Erase(admin, site)
						return err
					},
					func() error { return put.into(admin, site) },
				)
				eraseErr, inErr := errs[0], errs[1]

				// One of the two went through, and the other was told why not.
				x.True(eraseErr == nil || inErr == nil, "neither went through: %v, %v", eraseErr, inErr)
				if eraseErr != nil {
					x.Equal(codes.FailedPrecondition, status.Code(eraseErr), "%v", eraseErr)
					x.ErrorContains(eraseErr, put.kind)
				}
				if inErr != nil {
					x.Equal(codes.NotFound, status.Code(inErr), "%v", inErr)
				}

				left, err := in(ctx, nil, mustId(t, s.GetId()))
				x.NoError(err)
				if eraseErr == nil && erased.GetErased() && left.String() != "" {
					lost++
				}
			}

			x.Zero(lost, "%d of %d sites were erased with a %s in them", lost, sites, put.kind)
		})
	}
}

// TestAnOperatorErasesASiteWhenNothingIsInItToo: the rule is about the site
// and not about who asks, and the operators' port was asked nothing. What is
// in a customer's site is counted from the rows, and that port's rules read
// the control plane's, where the operators are: no customer's site has
// anything in it there, so an operator erased one with a team in it (#279).
// It is the data plane's rows that are counted, at every port.
func TestAnOperatorErasesASiteWhenNothingIsInItToo(t *testing.T) {
	x := require.New(t)
	s, c, out := adminDeployment(t, nil)
	conn, as := adminPort(t, s, c, out)

	of, err := app.NewHolderServiceClient(conn).Get(as, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Id: adminCustomer(t, conn, as, "contoso")}.Build(),
		Select: app.HolderSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	tenant := app.TenantRef_builder{Id: of.GetTenant().GetId()}.Build()

	seoul, err := app.NewSiteServiceClient(conn).Add(as, app.SiteAddRequest_builder{Tenant: tenant, Alias: "seoul"}.Build())
	x.NoError(err)
	site := app.SiteRef_builder{Id: seoul.GetId()}.Build()
	team, err := app.NewTeamServiceClient(conn).Add(as, app.TeamAddRequest_builder{Tenant: tenant, Site: site, Alias: "ops"}.Build())
	x.NoError(err)

	_, err = app.NewSiteServiceClient(conn).Erase(as, site)
	x.Equal(codes.FailedPrecondition, status.Code(err), "a site with a team in it: %v", err)
	x.ErrorContains(err, "a team")

	_, err = app.NewTeamServiceClient(conn).Erase(as, app.TeamRef_builder{Id: team.GetId()}.Build())
	x.NoError(err)
	v, err := app.NewSiteServiceClient(conn).Erase(as, site)
	x.NoError(err)
	x.True(v.GetErased())
}
