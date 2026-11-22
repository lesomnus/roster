package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/vouch"
)

// erasableRows is one row of every kind an `Erase` is served of, by the name
// of its service without `Service`, made in `tenant` and, where a row is a
// site's or a person's, in `site`.
func (b *built) erasableRows(t *testing.T, ctx context.Context, tenant, site pdid.Id, sfx string) map[string]pdid.Id {
	t.Helper()
	x := require.New(t)

	at := app.TenantRef_builder{Id: tenant.Bytes()}.Build()
	in := app.SiteRef_builder{Id: site.Bytes()}.Build()
	whose := b.holder(t, ctx, tenant, "erasable-"+sfx)
	who := app.HolderRef_builder{Id: whose.Bytes()}.Build()
	other := app.HolderRef_builder{Id: b.holder(t, ctx, tenant, "erasable-other-"+sfx).Bytes()}.Build()
	erasee := b.holder(t, ctx, tenant, "erasee-"+sfx)
	b.joins(t, ctx, erasee, site)

	rows := map[string]pdid.Id{"Holder": erasee}
	made := func(entity string, id []byte, err error) {
		t.Helper()
		x.NoError(err, entity)
		rows[entity] = mustId(t, id)
	}

	m, err := b.Ungated.SiteMembership().Add(ctx, app.SiteMembershipAddRequest_builder{Holder: who, Site: in}.Build())
	made("SiteMembership", m.GetId(), err)
	s, err := b.Ungated.Site().Add(ctx, app.SiteAddRequest_builder{Tenant: at, Alias: "erasable-" + sfx}.Build())
	made("Site", s.GetId(), err)

	e, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: who, Address: sfx + "@erasable.example"}.Build())
	made("Email", e.GetId(), err)
	p, err := b.Ungated.Phone().Add(ctx, app.PhoneAddRequest_builder{Holder: who, Number: "555 0100", Kind: "work"}.Build())
	made("Phone", p.GetId(), err)
	i, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{Holder: who, Provider: "door-" + sfx, Subject: "subject-" + sfx}.Build())
	made("Identity", i.GetId(), err)
	k, err := b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder: who, Alias: "erasable-" + sfx, Secret: sweepable(t), Methods: []string{app.MeService_Get_FullMethodName},
	}.Build())
	made("ApiKey", k.GetId(), err)
	_, err = b.Ungated.Credential().Enrol(ctx, app.CredentialEnrolRequest_builder{Holder: who, Kind: vouch.KindTotp, Name: "erasable"}.Build())
	x.NoError(err)
	cs, err := b.Ungated.Credential().List(ctx, app.CredentialListRequest_builder{
		Filters: []*app.CredentialFilter{app.CredentialFilter_builder{Holder: who}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(cs.GetItems(), 1)
	made("Credential", cs.GetItems()[0].GetId(), nil)

	// A password beside the account below, so that the account is not their
	// one way in and is erased as any row is.
	b.sets(t, ctx, whose, "a password of "+sfx+" that nobody has leaked")
	d, err := b.Ungated.Delegation().Add(ctx, app.DelegationAddRequest_builder{
		Holder: who, IssuedTo: who.GetId(), Secret: sweepable(t), Methods: []string{app.MeService_Get_FullMethodName},
		DateExpires: timestamppb.New(time.Now().Add(time.Hour)),
	}.Build())
	made("Delegation", d.GetId(), err)
	r, err := b.Ungated.Relation().Add(ctx, app.RelationAddRequest_builder{Holder: who, Other: other}.Build())
	made("Relation", r.GetId(), err)

	g, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{Tenant: at, Site: in, Alias: "erasable-" + sfx}.Build())
	made("Group", g.GetId(), err)
	group := app.GroupRef_builder{Id: g.GetId()}.Build()
	gx, err := b.Ungated.GroupExposure().Add(ctx, app.GroupExposureAddRequest_builder{Group: group, Client: "kube"}.Build())
	made("GroupExposure", gx.GetId(), err)
	gm, err := b.Ungated.GroupMembership().Add(ctx, app.GroupMembershipAddRequest_builder{Holder: who, Group: group}.Build())
	made("GroupMembership", gm.GetId(), err)
	tm, err := b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{Tenant: at, Site: in, Alias: "erasable-" + sfx}.Build())
	made("Team", tm.GetId(), err)
	tmm, err := b.Ungated.TeamMembership().Add(ctx, app.TeamMembershipAddRequest_builder{
		Holder: who, Team: app.TeamRef_builder{Id: tm.GetId()}.Build(),
	}.Build())
	made("TeamMembership", tmm.GetId(), err)
	l, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: at, Site: in, Alias: "erasable-" + sfx, Methods: []string{app.HolderService_Get_FullMethodName},
	}.Build())
	made("Role", l.GetId(), err)
	bd, err := b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role: app.RoleRef_builder{Id: l.GetId()}.Build(), Holder: who, Site: in,
	}.Build())
	made("Binding", bd.GetId(), err)

	c, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant: at, Name: "erasable-" + sfx, Issuer: "https://" + sfx + ".erasable.example", ClientId: "the-app",
	}.Build())
	made("Connection", c.GetId(), err)
	h, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{Tenant: at, Name: sfx + ".erasable.example"}.Build())
	made("Host", h.GetId(), err)
	hp, err := b.Ungated.HostProof().Add(ctx, app.HostProofAddRequest_builder{Tenant: at, Name: sfx + ".proved.example"}.Build())
	made("HostProof", hp.GetId(), err)
	md, err := b.Ungated.MailDomain().Add(ctx, app.MailDomainAddRequest_builder{Tenant: at, Name: sfx + ".mail.example"}.Build())
	made("MailDomain", md.GetId(), err)
	n, err := b.Ungated.Nomination().Add(ctx, app.NominationAddRequest_builder{
		Tenant: at, BorrowerId: pdid.New(pd.HolderDomain).Bytes(), ActsAs: who, Name: "erasable-" + sfx,
	}.Build())
	made("Nomination", n.GetId(), err)

	return rows
}

// erasingInProcess is the stack nothing gates, served, so that a row of any
// kind is asked for by its method's name.
func (b *built) erasingInProcess(t *testing.T) *grpc.ClientConn {
	t.Helper()

	g := grpc.NewServer()
	cmd.Register(g, b.Ungated)

	return pdtest.Serve(t, g)
}

// erasedTogether erases `rows` in process, each before what it hangs off.
func erasedTogether(t *testing.T, ctx context.Context, conn *grpc.ClientConn, rows map[string]pdid.Id) {
	t.Helper()

	for _, entity := range []string{
		"TeamMembership", "Team", "GroupMembership", "GroupExposure", "Group", "Binding", "Role", "SiteMembership", "Site",
		"Identity", "Email", "Phone", "ApiKey", "Credential", "Delegation", "Relation", "Nomination",
		"Connection", "Host", "HostProof", "MailDomain", "Holder",
	} {
		method := "/roster." + entity + "Service/Erase"
		in, out := failureMessages(t, method, failureRef(rows[entity]))
		require.NoError(t, conn.Invoke(ctx, method, in, out), "%s, erased first", entity)
	}
}

// TestAnEraseOfNothingIsDone: an `Erase` of a row that is not there answers
// `OK`, having erased nothing, in every service whose `Erase` is served, and
// whichever way the row is not there to the caller: nobody by that reference,
// a row of another tenant, one erased already, and one out of the caller's
// reach in a site.
//
// Eight answered `NotFound` (`Binding`, `Connection`, `Holder`, `Host`,
// `MailDomain`, `Nomination`, `Role`, `Site`), where the fourteen others
// answered `OK`. An erase is asked again after a timeout by every client, and
// `NotFound` on the second asking was a failure a page had to tell from a
// refusal. The four ways are one answer, so that nothing is told by it of a
// row the caller does not see.
//
// What is still refused is: a caller who lacks the method, first, and a verb
// about the whole tenant asked by somebody who holds it only in a site,
// whether or not the row is there.
func TestAnEraseOfNothingIsDone(t *testing.T) {
	x := require.New(t)
	_, erase := failuresOnThePage(t)

	b, ctx := build(t)
	at := failureServe(t, b, cmd.Config{})

	admin := b.holder(t, ctx, b.Contoso, "erase-admin")
	b.mayAnything(admin, b.Contoso)

	seoul := b.site(t, ctx, b.Contoso, "erase-seoul")
	frankfurt := b.site(t, ctx, b.Contoso, "erase-frankfurt")
	sited := b.holder(t, ctx, b.Contoso, "erase-sited")
	b.joins(t, ctx, sited, seoul)
	b.binds(t, sited, b.role(t, ctx, "erase-everything-in-seoul", "/roster.*/*"), &seoul)

	theirs := b.erasableRows(t, ctx, b.Fabrikam, b.site(t, ctx, b.Fabrikam, "erase-theirs"), "theirs")
	gone := b.erasableRows(t, ctx, b.Contoso, b.site(t, ctx, b.Contoso, "erase-gone"), "gone")
	ungated := b.erasingInProcess(t)
	erasedTogether(t, ctx, ungated, gone)
	away := b.erasableRows(t, ctx, b.Contoso, frankfurt, "away")
	x.Len(away, len(erase), "a row of every kind the page's Erase row names, and no other")

	// What a verb about the whole tenant answers somebody who holds it in a
	// site alone, whatever it is asked about: every write of these eight, as
	// the table of failures lists them.
	tenantwide := map[string]bool{
		"Connection": true, "Host": true, "HostProof": true, "MailDomain": true, "Nomination": true, "Site": true,
		"SiteMembership": true, "GroupExposure": true,
	}

	for _, kind := range []struct {
		what string
		as   pdid.Id
		rows map[string]pdid.Id
	}{
		{"nobody by that reference", admin, nil},
		{"a row of another tenant", admin, theirs},
		{"a row erased already", admin, gone},
		{"a row out of the caller's reach in a site", sited, away},
	} {
		t.Run(kind.what, func(t *testing.T) {
			a := assert.New(t)
			for entity := range erase {
				id := pdid.New(2)
				if kind.rows != nil {
					id = kind.rows[entity]
				}
				c := failureCall{method: "/roster." + entity + "Service/Erase", as: kind.as, req: failureRef(id)}

				if kind.as == sited && tenantwide[entity] {
					a.Equal(codes.PermissionDenied.String(), at.code(t, c).String(), "%s: a verb about the whole tenant", c.method)
					continue
				}

				code, erased := at.erased(t, c)
				a.Equal(codes.OK.String(), code.String(), "%s", c.method)
				a.False(erased, "%s: answered that something was erased", c.method)
				a.Equal(200, at.status(t, c), "%s over HTTP", c.method)
			}
		})
	}

	// And the rows of the three kinds that are there are there still, as they
	// were: nothing was erased by asking.
	for entity, id := range theirs {
		method := "/roster." + entity + "Service/Get"
		in, out := failureMessages(t, method, `{"ref":`+failureRef(id)+`}`)
		x.NoError(ungated.Invoke(ctx, method, in, out), "%s of another tenant", entity)
	}
	for entity, id := range away {
		method := "/roster." + entity + "Service/Get"
		in, out := failureMessages(t, method, `{"ref":`+failureRef(id)+`}`)
		x.NoError(ungated.Invoke(ctx, method, in, out), "%s out of reach", entity)
	}
}

// erased is the code an `Erase` was answered with over gRPC, and whether the
// answer says something was erased.
func (d failureAt) erased(t *testing.T, c failureCall) (codes.Code, bool) {
	t.Helper()

	ctx := asOverTheWire(t.Context(), c.as)
	in, out := failureMessages(t, c.method, c.req)
	err := d.conn.Invoke(ctx, c.method, in, out)
	if err != nil {
		return status.Code(err), false
	}
	f := out.ProtoReflect().Descriptor().Fields().ByName("erased")
	require.NotNil(t, f, "%s answers no `erased`", c.method)

	return codes.OK, out.ProtoReflect().Get(f).Bool()
}
