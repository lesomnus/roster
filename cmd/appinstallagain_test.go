package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestASecondInstallGoesOnForTheAppsOwnHolderWhateverItHolds: once `roster
// key add` has minted the app's own `rt_` key, `roster app install` run again
// goes on -- with status 0, writing nothing twice, and putting back a row that
// is gone -- as `docs/usage/rules.md` says a second run does.
//
// # What it was
//
// A holder of the app's name that already holds something is taken for
// somebody else's until `--adopt` says otherwise, and the app's own key was
// counted among what it held: a second run answered *@acme/loft is already
// there and holds 1 key(s)* and failed, and an operator putting back a binding
// a tenant had ended was told to adopt the app's own holder. The holder says
// whose it is: `install` writes `roster.app` on it, the app's name (#451), so
// one that carries this app's name is this app's, whatever it holds, and one
// that carries another's, or none, is refused as before.
func TestASecondInstallGoesOnForTheAppsOwnHolderWhateverItHolds(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)
	c.Client.As = "admin" // who `init` made, named at the box (#284)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	acme, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: acme.GetId()}.Build()
	x.NoError(s.Close())

	install := func(name string, args ...string) error {
		return cli.NewCmdApp(&c).Run(ctx, append([]string{"install",
			"--tenant", "acme",
			"--allow", "/roster.HolderService/Reaches",
			"--serves", "/" + name + ".*/*",
			"--administer", "admin",
		}, append(args, name)...))
	}
	x.NoError(install("loft"))

	// The app's own key, minted for its holder as the manual says.
	stdoutOf(t, cli.NewCmdKey(&c), "add", "--tenant", "acme", "--holder", "loft", "--name", "loft",
		"--allow", "/roster.HolderService/Reaches")

	boundOf := func(s *cmd.Server, who []byte) []*app.Binding {
		t.Helper()

		bs, err := s.Ungated.Binding().List(ctx, app.BindingListRequest_builder{
			Filters: []*app.BindingFilter{app.BindingFilter_builder{Holder: app.HolderRef_builder{Id: who}.Build()}.Build()},
		}.Build())
		require.NoError(t, err)

		return bs.GetItems()
	}

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	who, err := cmd.CustomerOf(ctx, s, "acme", "loft")
	x.NoError(err)
	before := boundOf(s, who.Bytes())
	x.Len(before, 3, "the app holds its role of roster's, its own, and roster's verbs for itself")

	// A binding the tenant ended, which a second run is how an operator puts
	// back.
	r, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
		Ref:    app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: proto.String("loft-app"), Tenant: at}.Build()}.Build(),
		Select: app.RoleSelect_builder{}.Build(),
	}.Build())
	x.NoError(err)
	for _, b := range before {
		if string(b.GetRole().GetId()) == string(r.GetId()) {
			_, err := s.Ungated.Binding().Erase(ctx, app.BindingRef_builder{Id: b.GetId()}.Build())
			x.NoError(err)
		}
	}
	x.Len(boundOf(s, who.Bytes()), 2)
	x.NoError(s.Close())

	x.NoError(install("loft"), "a second run, for the app's own holder and its own key")

	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	after := boundOf(s, who.Bytes())
	x.Len(after, 3, "the binding that was gone is back, and nothing is bound twice")
	vs, err := s.Ungated.Nomination().List(ctx, app.NominationListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 1, "nominated twice")
	ks, err := s.Ungated.ApiKey().List(ctx, app.ApiKeyListRequest_builder{
		Filters: []*app.ApiKeyFilter{app.ApiKeyFilter_builder{Holder: app.HolderRef_builder{Id: who.Bytes()}.Build()}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(ks.GetItems(), 1, "the app's own key, as it was")

	t.Run("a holder of another app's, or of nobody's, is refused as before", func(t *testing.T) {
		x := require.New(t)

		// levyan's holder, holding its key, and labelled as another app's:
		// installing levyan again finds somebody else's holder.
		x.NoError(install("levyan"))
		stdoutOf(t, cli.NewCmdKey(&c), "add", "--tenant", "acme", "--holder", "levyan", "--name", "levyan",
			"--allow", "/roster.HolderService/Reaches")

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		levyan, err := cmd.CustomerOf(ctx, s, "acme", "levyan")
		x.NoError(err)
		got, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Id: levyan.Bytes()}.Build(),
			Select: app.HolderSelect_builder{Labels: proto.Bool(true), DateUpdated: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		labels := got.GetLabels()
		labels[app.LabelApp] = "somebody-else"
		_, err = s.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
			Ref: app.HolderRef_builder{Id: levyan.Bytes()}.Build(), Labels: labels, DateUpdated: got.GetDateUpdated(),
		}.Build())
		x.NoError(err)

		// And somebody called bob, who holds a role and no label.
		bob, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: "bob"}.Build())
		x.NoError(err)
		staff, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{Tenant: at, Alias: "staff", Methods: []string{"/roster.HolderService/Get"}}.Build())
		x.NoError(err)
		_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role: app.RoleRef_builder{Id: staff.GetId()}.Build(), Holder: app.HolderRef_builder{Id: bob.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		x.NoError(s.Close())

		for _, name := range []string{"levyan", "bob"} {
			x.ErrorContains(install(name), "--adopt", "%s's holder is not this app's", name)
		}

		s, err = cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		x.Len(boundOf(s, bob.GetId()), 1, "a refused install bound somebody else's holder")
	})
}
