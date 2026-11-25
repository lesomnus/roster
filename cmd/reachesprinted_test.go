package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestWhatSomebodyReachesIsPrintedInIdentifiers: `roster holder reaches`
// prints the sites and the apps it answers as identifiers, as every other
// identifier is printed, in the default output.
//
// It printed each as a Go byte slice, `[1 162 18 16 97 …]`, which names
// nothing a person can type back: the text printer has a case for one
// identifier and none for a list of them, so every answer with a repeated
// identifier in it printed so -- `sites` and `apps` here and in `me get`.
func TestWhatSomebodyReachesIsPrintedInIdentifiers(t *testing.T) {
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
	alice, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: "alice"}.Build())
	x.NoError(err)
	seoul, err := s.Ungated.Site().Add(ctx, app.SiteAddRequest_builder{Tenant: at, Alias: "seoul"}.Build())
	x.NoError(err)
	reader, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{Tenant: at, Alias: "reader", Methods: []string{"/roster.HolderService/Get"}}.Build())
	x.NoError(err)
	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: reader.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: alice.GetId()}.Build(),
		Site:   app.SiteRef_builder{Id: seoul.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	// An app alice administers, which is a binding of roster's for the app.
	x.NoError(cli.NewCmdApp(&c).Run(ctx, []string{"install", "--tenant", "acme",
		"--allow", "/roster.HolderService/Reaches", "--serves", "/loft.*/*", "--administer", "alice", "loft"}))
	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	loft, err := cmd.CustomerOf(ctx, s, "acme", "loft")
	x.NoError(err)
	x.NoError(s.Close())

	out, err = entities(t, &c, "holder", "reaches", "@acme/alice", `{"roster": true}`)
	x.NoError(err, out)
	x.Contains(out, pdid.Id(seoul.GetId()).String(), "the site is not printed as its identifier:\n%s", out)
	x.Contains(out, loft.String(), "the app is not printed as its identifier:\n%s", out)
	x.NotContains(out, "[", "an identifier printed as bytes:\n%s", out)
}

// TestWhomACallReadIsPrintedAndKeptAsIdentifiers: the access record's
// `subjects`, a list in an entity read at the box, prints as identifiers in
// the default output, and saying so of the field changed nothing kept.
//
// It printed each as a byte slice, as `reaches` printed its sites and apps.
// `Access.subjects` now says its elements are identifiers (`TYPE_UUID`),
// which on an entity's list says what the elements are and not how they are
// kept: the column is the list of bytes it was, as JSON. So a row in the form
// a build before this wrote is read and printed by this one, and the row this
// build writes is the list of bytes a build before reads.
func TestWhomACallReadIsPrintedAndKeptAsIdentifiers(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	b := atTheBox(t)

	// The row this build writes: a read of the tenant's administrator.
	_, err := entities(t, at(b.c, "admin"), "holder", "get", "@newco/admin")
	x.NoError(err)
	rs := accessed(t, b.c, false, app.HolderService_Get_FullMethodName)
	x.Len(rs, 1)
	x.Equal([][]byte{b.admin.Bytes()}, rs[0].Subjects, "kept as other than a list of bytes")

	// A row as a build before this wrote it: the same column, a list of
	// bytes, with nothing of the annotation.
	s, err := cmd.Build(ctx, b.c)
	x.NoError(err)
	someone := pdid.New(1)
	_, err = s.Ent.Access.Create().
		SetId(pdid.New(1).Uuid()).
		SetTenantId(b.newco.Uuid()).
		SetActorId(b.operator.Uuid()).
		SetActorTenantId(b.owner.Uuid()).
		SetTraceId([]byte{}).
		SetValue([]byte{}).
		SetAction("/roster.HolderService/List").
		SetKind("read").
		SetResult("ok").
		SetSubjects([][]byte{someone.Bytes()}).
		SetSubjectCount(1).
		Save(ctx)
	x.NoError(err)
	x.NoError(s.Close())

	out, err := entities(t, at(b.c, "admin"), "access", "list")
	x.NoError(err, out)
	x.Contains(out, b.admin.String(), "whom this build's row read is not printed as an identifier:\n%s", out)
	x.Contains(out, someone.String(), "whom an earlier row read is not printed as an identifier:\n%s", out)
	x.NotContains(out, "[", "an identifier printed as bytes:\n%s", out)
}
