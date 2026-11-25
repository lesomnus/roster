package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// organisationSaid is what contoso says of somebody, as it was written for
// them before a test changes any of it.
var organisationSaid = app.Profile_builder{
	DisplayName: "Erin Park", Department: "Payroll", EmployeeNo: "E-1001", Title: "Accountant",
}

// organisationSays writes somebody's profile at the box, which is held to no
// rule about whose it is.
func (b *built) organisationSays(t *testing.T, ctx context.Context, who pdid.Id, p *app.Profile) {
	t.Helper()
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()
	got, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref}.Build())
	require.NoError(t, err)
	_, err = b.Ungated.Holder().Update(ctx, app.HolderUpdateRequest_builder{Ref: ref, DateUpdated: got.GetDateUpdated(), Profile: p}.Build())
	require.NoError(t, err)
}

// profileOf is somebody's profile as it is stored.
func (b *built) profileOf(t *testing.T, ctx context.Context, who pdid.Id) *app.Holder {
	t.Helper()
	v, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: who.Bytes()}.Build()}.Build())
	require.NoError(t, err)

	return v
}

// organisationChanges is `HolderService/Update` of `who`'s profile, by
// `server`, as `as`, with what `change` makes of the profile stored.
func (b *built) organisationChanges(ctx context.Context, t *testing.T, server app.HolderServiceServer, who pdid.Id, change func(p *app.Profile_builder)) error {
	t.Helper()
	v := b.profileOf(t, context.Background(), who)
	p := app.Profile_builder{
		DisplayName: v.GetProfile().GetDisplayName(), Department: v.GetProfile().GetDepartment(),
		EmployeeNo: v.GetProfile().GetEmployeeNo(), Title: v.GetProfile().GetTitle(), Locale: v.GetProfile().GetLocale(),
	}
	change(&p)
	_, err := server.Update(ctx, app.HolderUpdateRequest_builder{
		Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(), DateUpdated: v.GetDateUpdated(), Profile: p.Build(),
	}.Build())

	return err
}

// theOrganisationsThree is each field of a profile that is what an
// organisation says of somebody, and a change of it.
var theOrganisationsThree = []struct {
	field  string
	change func(p *app.Profile_builder)
}{
	{"profile.department", func(p *app.Profile_builder) { p.Department = "Treasury" }},
	{"profile.employee_no", func(p *app.Profile_builder) { p.EmployeeNo = "E-9999" }},
	{"profile.title", func(p *app.Profile_builder) { p.Title = "Chief Financial Officer" }},
}

// refusedAsTheOrganisations is the refusal of somebody changing what their
// organisation says of them: `PermissionDenied`, naming the field.
func refusedAsTheOrganisations(t *testing.T, err error, field string) {
	t.Helper()
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), field+": ")
	require.Contains(t, status.Convert(err).Message(), "what your organisation says of you")
}

// TestSomebodyChangingTheirOwnProfileKeepsWhatTheirOrganisationSaysOfThem.
//
// `HolderService/Update` replaces the whole profile, and everybody of a
// tenant holds it over their own row (`self-service`), so a person changed
// their own department, employee number and title with the call that
// changes their display name: answered OK, and read back as they wrote it
// (#368). A directory that provisions them writes those again the next time
// it sends them; a tenant with none never did. Right is `PermissionDenied`,
// naming the field, and the row as it was.
//
// The three are what an organisation says of somebody, fixed in roster and no
// tenant's setting. A person changing their own row keeps them as they are,
// and a profile carrying them as they are stored passes, so a page that reads
// the profile, changes the name and sends it all back is not refused; one
// that leaves one out where one is stored would clear it, and is refused.
//
// Not held by it: whoever holds the verb otherwise than over their own row
// -- an administrator, across the tenant, of somebody else's and of their
// own; a directory's provisioner; the deployment at the box. And an app
// acting for a person through a delegation is that person, so the three are
// kept from it.
func TestSomebodyChangingTheirOwnProfileKeepsWhatTheirOrganisationSaysOfThem(t *testing.T) {
	b, ctx := build(t)
	erin := b.holder(t, ctx, b.Contoso, "erin")
	b.organisationSays(t, ctx, erin, organisationSaid.Build())
	holders := b.Walled.Holder()
	asErin := b.asNobody(ctx, erin, b.Contoso)

	t.Run("a person changing their own is refused, naming the field", func(t *testing.T) {
		for _, tc := range theOrganisationsThree {
			t.Run(tc.field, func(t *testing.T) {
				err := b.organisationChanges(asErin, t, holders, erin, tc.change)
				refusedAsTheOrganisations(t, err, tc.field)

				got := b.profileOf(t, ctx, erin).GetProfile()
				require.Equal(t, "Payroll", got.GetDepartment())
				require.Equal(t, "E-1001", got.GetEmployeeNo())
				require.Equal(t, "Accountant", got.GetTitle())
			})
		}
	})

	t.Run("and their name, carrying the three as they are, is written", func(t *testing.T) {
		x := require.New(t)
		err := b.organisationChanges(asErin, t, holders, erin, func(p *app.Profile_builder) { p.DisplayName = "Erin K. Park" })
		x.NoError(err)

		got := b.profileOf(t, ctx, erin).GetProfile()
		x.Equal("Erin K. Park", got.GetDisplayName())
		x.Equal("Payroll", got.GetDepartment())
		x.Equal("E-1001", got.GetEmployeeNo())
		x.Equal("Accountant", got.GetTitle())
	})

	t.Run("and carrying one of them empty where one is stored is refused", func(t *testing.T) {
		err := b.organisationChanges(asErin, t, holders, erin, func(p *app.Profile_builder) {
			p.DisplayName = "Erin"
			p.Title = ""
		})
		refusedAsTheOrganisations(t, err, "profile.title")
		require.Equal(t, "Accountant", b.profileOf(t, ctx, erin).GetProfile().GetTitle())
	})

	t.Run("an app acting for them through a delegation is them", func(t *testing.T) {
		foo := b.holder(t, ctx, b.Contoso, "foo")
		f := frame.New(erin, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso))
		f.Caller = &frame.Principal{Actor: foo, Tenant: b.Contoso, Name: "foo"}
		f.Delegated = true
		asFoo := frame.Into(ctx, f)

		for _, tc := range theOrganisationsThree {
			refusedAsTheOrganisations(t, b.organisationChanges(asFoo, t, holders, erin, tc.change), tc.field)
		}
		require.Equal(t, "Payroll", b.profileOf(t, ctx, erin).GetProfile().GetDepartment())
	})

	t.Run("an administrator of the tenant changes somebody else's, and their own", func(t *testing.T) {
		x := require.New(t)
		admin := b.holder(t, ctx, b.Contoso, "people-admin")
		b.binds(t, admin, b.role(t, ctx, "people", app.HolderService_Get_FullMethodName, app.HolderService_Update_FullMethodName), nil)
		b.organisationSays(t, ctx, admin, organisationSaid.Build())
		asAdmin := b.asNobody(ctx, admin, b.Contoso)

		for _, who := range []pdid.Id{erin, admin} {
			for _, tc := range theOrganisationsThree {
				x.NoError(b.organisationChanges(asAdmin, t, holders, who, tc.change), tc.field)
			}
			got := b.profileOf(t, ctx, who).GetProfile()
			x.Equal("Treasury", got.GetDepartment())
			x.Equal("E-9999", got.GetEmployeeNo())
			x.Equal("Chief Financial Officer", got.GetTitle())
		}
		b.organisationSays(t, ctx, erin, organisationSaid.Build())
	})

	t.Run("a directory's provisioner changes them", func(t *testing.T) {
		x := require.New(t)
		directory := b.holder(t, ctx, b.Contoso, "scim")
		b.binds(t, directory, b.role(t, ctx, "scim", scim.Bound()...), nil)
		asDirectory := b.asNobody(ctx, directory, b.Contoso)

		for _, tc := range theOrganisationsThree {
			x.NoError(b.organisationChanges(asDirectory, t, holders, erin, tc.change), tc.field)
		}
		x.Equal("Treasury", b.profileOf(t, ctx, erin).GetProfile().GetDepartment())
		b.organisationSays(t, ctx, erin, organisationSaid.Build())
	})

	t.Run("the box changes them", func(t *testing.T) {
		x := require.New(t)
		for _, tc := range theOrganisationsThree {
			x.NoError(b.organisationChanges(ctx, t, b.Ungated.Holder(), erin, tc.change), tc.field)
		}
		x.Equal("Chief Financial Officer", b.profileOf(t, ctx, erin).GetProfile().GetTitle())
		b.organisationSays(t, ctx, erin, organisationSaid.Build())
	})
}

// TestSomebodySignedInKeepsWhatTheirOrganisationSaysOfThem is the rule over
// the wire, as a person signed in at their tenant's name with a session:
// their own department is refused, naming it, and their own name, sent with
// the rest of the profile as it was read, is written.
func TestSomebodySignedInKeepsWhatTheirOrganisationSaysOfThem(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, func(c *cmd.Config) { c.SignIn.Enabled = true })
	in := signedInAtTheData(t, b.Server)
	alice := mustId(t, in.watched)
	b.organisationSays(t, ctx, alice, organisationSaid.Build())
	holders := app.NewHolderServiceClient(in.conn)
	ref := app.HolderRef_builder{Id: alice.Bytes()}.Build()

	send := func(change func(p *app.Profile)) error {
		got := b.profileOf(t, ctx, alice)
		p := got.GetProfile()
		change(p)
		_, err := holders.Update(in.as, app.HolderUpdateRequest_builder{Ref: ref, DateUpdated: got.GetDateUpdated(), Profile: p}.Build())

		return err
	}

	refusedAsTheOrganisations(t, send(func(p *app.Profile) { p.SetDepartment("Treasury") }), "profile.department")
	x.Equal("Payroll", b.profileOf(t, ctx, alice).GetProfile().GetDepartment())

	x.NoError(send(func(p *app.Profile) { p.SetDisplayName("Alice Kim") }))
	x.Equal("Alice Kim", b.profileOf(t, ctx, alice).GetProfile().GetDisplayName())
	x.Equal("Payroll", b.profileOf(t, ctx, alice).GetProfile().GetDepartment())
}
