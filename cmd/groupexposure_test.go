package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestAGroupIsShownToTheClientsItNames: a `GroupExposure` is a client whose
// tokens carry a group, one row per client -- added by whoever may, refused
// for a client no issuer would name so, refused twice, taken back by erasing,
// and never about another tenant's group.
func TestAGroupIsShownToTheClientsItNames(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)

	g, err := b.Walled.Group().Add(admin, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "dtrack-triage",
	}.Build())
	require.NoError(t, err)
	ref := app.GroupRef_builder{Id: g.GetId()}.Build()

	expose := func(client string) (*app.GroupExposure, error) {
		return b.Walled.GroupExposure().Add(admin, app.GroupExposureAddRequest_builder{Group: ref, Client: client}.Build())
	}
	shown := func() []string {
		t.Helper()
		vs, err := b.Walled.GroupExposure().List(admin, app.GroupExposureListRequest_builder{
			Filters: []*app.GroupExposureFilter{app.GroupExposureFilter_builder{Group: ref}.Build()},
		}.Build())
		require.NoError(t, err)

		out := []string{}
		for _, v := range vs.GetItems() {
			out = append(out, v.GetClient())
		}

		return out
	}

	var row *app.GroupExposure
	t.Run("to a client, by the id its issuer gave it", func(t *testing.T) {
		x := require.New(t)
		row, err = expose("dtrack")
		x.NoError(err)
		x.Equal([]string{"dtrack"}, shown())
	})

	t.Run("once", func(t *testing.T) {
		_, err := expose("dtrack")
		require.Equal(t, codes.AlreadyExists, status.Code(err), "%v", err)
	})

	t.Run("and not to what no issuer calls a client", func(t *testing.T) {
		x := require.New(t)
		for _, client := range []string{"", "two words", "tab\there", strings.Repeat("c", 256)} {
			_, err := expose(client)
			x.Equal(codes.InvalidArgument, status.Code(err), "%q: %v", client, err)
		}
		x.Equal([]string{"dtrack"}, shown())
	})

	t.Run("taken back by erasing, and shown again", func(t *testing.T) {
		x := require.New(t)
		_, err := b.Walled.GroupExposure().Erase(admin, app.GroupExposureRef_builder{Id: row.GetId()}.Build())
		x.NoError(err)
		x.Empty(shown())

		_, err = expose("dtrack")
		x.NoError(err)
		x.Equal([]string{"dtrack"}, shown())
	})

	t.Run("never another tenant's group", func(t *testing.T) {
		x := require.New(t)
		theirs, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(),
			Alias:  "dtrack-triage",
		}.Build())
		x.NoError(err)

		_, err = b.Walled.GroupExposure().Add(admin, app.GroupExposureAddRequest_builder{
			Group: app.GroupRef_builder{Id: theirs.GetId()}.Build(), Client: "dtrack",
		}.Build())
		x.Error(err)
		x.NotEqual(codes.OK, status.Code(err))
	})
}
