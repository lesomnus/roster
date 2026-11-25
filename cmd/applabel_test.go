package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// appLabelKey is the label `roster app install` writes on the holder it makes
// for an app in a tenant, spelled as a directory's author reads it.
const appLabelKey = "roster.app"

// appLabelOf is the labels of the holder called `alias` in `at`.
func appLabelOf(t *testing.T, c cmd.Config, at *app.TenantRef, alias string) map[string]string {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	v, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: proto.String(alias), Tenant: at}.Build()}.Build(),
		Select: app.HolderSelect_builder{Labels: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)

	return v.GetLabels()
}

// TestAnInstalledAppsHolderSaysItIsAnApps: the holder `roster app install`
// makes for an app in a tenant carries `roster.app`, the app's name.
//
// It carried nothing, so a directory built on roster listed it as somebody,
// and an app reading roster as its directory could leave out only itself, by
// name (#451). A front door's holder carries `roster.declared` and is left
// out by it; an app's is now told apart the same way. A second run puts the
// label back on a holder that lacks it, as it puts back any row that is gone;
// a holder taken with `--adopt` is labelled too, and one that a refused
// install found is not, since it was not the app's to write; and uninstalling
// leaves it, since the holder is still an app's.
func TestAnInstalledAppsHolderSaysItIsAnApps(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)
	c.Client.As = "admin"

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	acme, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: acme.GetId()}.Build()

	// Somebody called jenkins, who holds a role of their own.
	jenkins, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: "jenkins"}.Build())
	x.NoError(err)
	staff, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{Tenant: at, Alias: "staff", Methods: []string{"/roster.HolderService/Get"}}.Build())
	x.NoError(err)
	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role: app.RoleRef_builder{Id: staff.GetId()}.Build(), Holder: app.HolderRef_builder{Id: jenkins.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	run := func(args ...string) error {
		return cli.NewCmdApp(&c).Run(ctx, args)
	}
	install := func(args ...string) error {
		return run(append([]string{"install", "--tenant", "acme", "--allow", "/roster.HolderService/Reaches"}, args...)...)
	}

	x.NoError(install("loft"))
	x.Equal("loft", appLabelOf(t, c, at, "loft")[appLabelKey], "the holder install made carried no label saying it is an app's")

	t.Run("a second run puts it on a holder that lacks it", func(t *testing.T) {
		x := require.New(t)

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		v, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: proto.String("loft"), Tenant: at}.Build()}.Build(),
			Select: app.HolderSelect_builder{DateUpdated: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		_, err = s.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
			Ref: app.HolderRef_builder{Id: v.GetId()}.Build(), Labels: map[string]string{"note": "kept"}, DateUpdated: v.GetDateUpdated(),
		}.Build())
		x.NoError(err)
		x.NoError(s.Close())
		x.NotContains(appLabelOf(t, c, at, "loft"), appLabelKey, "the label was not taken away at the box")

		x.NoError(install("loft"))
		got := appLabelOf(t, c, at, "loft")
		x.Equal("loft", got[appLabelKey])
		x.Equal("kept", got["note"], "a label of the tenant's was taken away")
	})

	t.Run("a holder taken with --adopt is labelled, and one refused is not", func(t *testing.T) {
		x := require.New(t)

		x.ErrorContains(install("jenkins"), "--adopt")
		x.NotContains(appLabelOf(t, c, at, "jenkins"), appLabelKey, "a holder a refused install found was labelled an app's")

		x.NoError(install("--adopt", "jenkins"))
		x.Equal("jenkins", appLabelOf(t, c, at, "jenkins")[appLabelKey])
	})

	t.Run("and uninstalling leaves it", func(t *testing.T) {
		x := require.New(t)

		x.NoError(run("uninstall", "--tenant", "acme", "loft"))
		x.Equal("loft", appLabelOf(t, c, at, "loft")[appLabelKey])
	})
}

// TestAdoptListsNoHolderAnAppIsInstalledAs: `adopt` lists people, and the
// holder `roster app install` made is none, whether or not the app is still
// nominated there.
//
// It left out a holder a nomination names, and an app uninstalled -- its
// nomination ended, its holder still there -- was listed and linked: an
// account at the connection for a holder nobody signs in as. Answered:
// listed; right is said apart after the list, as what an app acts as is, and
// never linked. It is told by `roster.app`, the test the directory and the
// SCIM endpoint ask.
func TestAdoptListsNoHolderAnAppIsInstalledAs(t *testing.T) {
	x := require.New(t)
	b := newAdoptBed(t)
	ctx := t.Context()

	x.NoError(cli.NewCmdApp(&b.c).Run(ctx, []string{"install", "--tenant", "acme", "--allow", "/roster.HolderService/Reaches", "loft"}))
	x.NoError(cli.NewCmdApp(&b.c).Run(ctx, []string{"uninstall", "--tenant", "acme", "loft"}))

	var fred, loft *app.HolderRef
	b.with(func(s *cmd.Server) {
		fred = b.person(s, "fred")
		b.tell(s, fred, fredOid)

		v, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: proto.String("loft"), Tenant: b.at}.Build()}.Build(),
			Select: app.HolderSelect_builder{}.Build(),
		}.Build())
		x.NoError(err)
		loft = app.HolderRef_builder{Id: v.GetId()}.Build()
		b.tell(s, loft, ginaOid)
	})

	out, said, err := b.adopt()
	x.NoError(err)
	x.Contains(out, "fred\tfred@acme.example\t"+fredOid, "a person beside it was not listed")
	x.NotContains(out, "loft", "an app's holder was listed as a person")
	x.Contains(said, "loft", "said, after the list")

	_, _, err = b.adopt("--confirm", "acme", "--of", listDigest(t, said))
	x.NoError(err)
	ids, _ := b.subjectsOf(fred)
	x.Equal([]string{fredOid}, ids)
	ids, _ = b.subjectsOf(loft)
	x.Empty(ids, "an app's holder was linked")
}
