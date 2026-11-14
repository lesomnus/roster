package cmd_test

import (
	"testing"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
)

// TestWhatSomebodyIsToSomebodyIsARecordNothingReads: a `Relation` is two
// people of one tenant and labels, one row a pair in one direction, and roster
// reads none of it. So writing one is held to nothing but the role that names
// the write -- somebody holding only that says who an administrator reports
// to -- and it goes when either of the two is forgotten.
func TestWhatSomebodyIsToSomebodyIsARecordNothingReads(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	erin, fred := b.holder(t, ctx, b.Contoso, "erin"), b.holder(t, ctx, b.Contoso, "fred")
	ref := func(k pdid.Id) *app.HolderRef { return app.HolderRef_builder{Id: k.Bytes()}.Build() }

	relate := func(holder, other pdid.Id, labels ...string) error {
		ls := map[string]string{}
		for _, v := range labels {
			ls[v] = ""
		}
		_, err := b.Walled.Relation().Add(admin, app.RelationAddRequest_builder{
			Holder: ref(holder), Other: ref(other), Labels: ls,
		}.Build())

		return err
	}
	of := func(holder pdid.Id) map[string][]string {
		t.Helper()
		vs, err := b.Ungated.Relation().List(ctx, app.RelationListRequest_builder{
			Filters: []*app.RelationFilter{app.RelationFilter_builder{Holder: ref(holder)}.Build()},
		}.Build())
		require.NoError(t, err)

		out := map[string][]string{}
		for _, v := range vs.GetItems() {
			other, err := pdid.From(v.GetOther().GetId())
			require.NoError(t, err)
			for l := range v.GetLabels() {
				out[other.String()] = append(out[other.String()], l)
			}
		}

		return out
	}

	t.Run("kept, with how to read it", func(t *testing.T) {
		x := require.New(t)
		x.NoError(relate(erin, fred, "manager"))
		x.Equal(map[string][]string{fred.String(): {"manager"}}, of(erin))

		v, err := b.Walled.Relation().Get(admin, app.RelationGetRequest_builder{
			Ref: app.RelationRef_builder{Pair: app.RelationRefByPair_builder{Holder: ref(erin), Other: ref(fred)}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		x.Contains(v.GetLabels(), "manager")
	})

	t.Run("one row a pair, in one direction", func(t *testing.T) {
		x := require.New(t)
		err := relate(erin, fred, "mentor")
		x.Equal(codes.AlreadyExists, status.Code(err), "%v", err)
		x.NoError(relate(fred, erin, "mentor"), "what fred is to erin is another row")
	})

	t.Run("between two people, of one tenant", func(t *testing.T) {
		x := require.New(t)
		err := relate(erin, erin, "manager")
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

		// Somebody of another tenant. An administrator of this one cannot see
		// them to name them, which is the wall; the rule is for a caller who
		// sees both -- the deployment's own work, asking with no wall at all.
		stranger := b.holder(t, ctx, b.Fabrikam, "stranger")
		err = relate(erin, stranger, "manager")
		x.NotEqual(codes.OK, status.Code(err), "%v", err)
		_, err = b.Ungated.Relation().Add(ctx, app.RelationAddRequest_builder{
			Holder: ref(erin), Other: ref(stranger), Labels: map[string]string{"manager": ""},
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "a row of one tenant saying something of another's: %v", err)
		x.NotContains(of(erin), stranger.String())
	})

	t.Run("held to nothing but the role that names the write", func(t *testing.T) {
		x := require.New(t)
		clerk := b.holder(t, ctx, b.Contoso, "clerk")
		b.binds(t, clerk, b.role(t, ctx, "org-chart", app.RelationService_Add_FullMethodName), nil)

		_, err := b.Walled.Relation().Add(b.asNobody(ctx, clerk, b.Contoso), app.RelationAddRequest_builder{
			Holder: ref(b.ContosoUser), Other: ref(fred), Labels: map[string]string{"manager": ""},
		}.Build())
		x.NoError(err, "who an administrator reports to is a record, and hands the clerk nothing")
	})

	t.Run("gone with either of them", func(t *testing.T) {
		x := require.New(t)
		gina := b.holder(t, ctx, b.Contoso, "gina")
		x.NoError(relate(gina, erin, "manager"))
		x.NotEmpty(of(gina))

		_, err := b.Ungated.Holder().Erase(ctx, ref(erin))
		x.NoError(err)
		_, err = forget.Forget(ctx, b.Ent, erin, trail.Policy{}, audit.Contents{}, nil)
		x.NoError(err)

		x.Empty(of(erin), "what she was to anybody")
		x.Empty(of(gina), "and what anybody was to her")
	})
}
