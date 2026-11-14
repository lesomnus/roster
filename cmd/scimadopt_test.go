package cmd_test

import (
	"testing"

	"github.com/lesomnus/xli/cfg"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestAnOperatorLinksWhoTheDirectoryMatched: `roster scim adopt` lists the
// people a tenant's directory said it calls something and roster has not
// linked -- who roster has, their address, what the directory calls them --
// and writes nothing until the tenant's alias is typed back. Then each is
// linked at the connection the directory provisions through, which is what
// lets it suspend them, and what was kept beside them is taken away. A subject
// somebody else already signs in by is refused, said, and the rest are linked.
func TestAnOperatorLinksWhoTheDirectoryMatched(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)
	// Who is at the shell: the list is people's addresses, and a read of them
	// names its reader.
	c.Client.As = "admin"

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	acme, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)
	at := app.TenantRef_builder{Id: acme.GetId()}.Build()
	_, err = s.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/acme/v2.0", ClientId: "the-app",
		SubjectClaim: "oid", Provisions: true,
	}.Build())
	x.NoError(err)

	// Three people an operator entered. The directory matched fred and gina
	// and said what it calls each; of henry it said nothing.
	person := func(alias string) *app.HolderRef {
		t.Helper()
		h, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: at, Alias: alias, Name: alias}.Build())
		x.NoError(err)
		ref := app.HolderRef_builder{Id: h.GetId()}.Build()
		_, err = s.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: ref, Address: alias + "@acme.example"}.Build())
		x.NoError(err)

		return ref
	}
	fred, gina, henry := person("fred"), person("gina"), person("henry")
	for ref, subject := range map[*app.HolderRef]string{fred: fredOid, gina: erinOid} {
		_, err = s.Ungated.Holder().Tell(ctx, app.HolderTellRequest_builder{Ref: ref, Subject: proto.String(subject)}.Build())
		x.NoError(err)
	}
	x.NoError(s.Close())

	adopt := func(args ...string) (string, error) {
		t.Helper()
		k := cli.NewCmdScim(cfg.New(cmd.Name, &c), &c)

		// The error is the test's to read, so it is carried past `captured`,
		// which would fail the test on one.
		var err error
		said := captured(t, func() error {
			err = k.Run(ctx, append([]string{"adopt", "--tenant", "acme"}, args...))

			return nil
		})

		return said, err
	}
	subjectsOf := func(ref *app.HolderRef) ([]string, string) {
		t.Helper()
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()

		ids, err := s.Ungated.Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{app.IdentityFilter_builder{Holder: ref}.Build()},
		}.Build())
		x.NoError(err)
		var out []string
		for _, v := range ids.GetItems() {
			x.Equal("entra", v.GetProvider())
			out = append(out, v.GetSubject())
		}
		h, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: ref, Select: app.HolderSelect_builder{DirectorySubject: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)

		return out, h.GetDirectorySubject()
	}

	// Listed, and nothing written.
	said, err := adopt()
	x.NoError(err)
	x.Contains(said, "fred\tfred@acme.example\t"+fredOid)
	x.Contains(said, "gina\tgina@acme.example\t"+erinOid)
	x.NotContains(said, "henry")
	ids, kept := subjectsOf(fred)
	x.Empty(ids, "a list is not a link")
	x.Equal(fredOid, kept)

	// The alias typed back is another tenant's: nothing written either.
	_, err = adopt("--confirm", "contoso")
	x.ErrorContains(err, "nothing was written")
	ids, _ = subjectsOf(fred)
	x.Empty(ids)

	// Somebody else comes to sign in by what the directory calls gina, before
	// anybody believed it of her.
	s, err = cmd.Build(ctx, c)
	x.NoError(err)
	_, err = s.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{Holder: henry, Provider: "entra", Subject: erinOid}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	// Believed: fred is linked, and gina is refused and said.
	said, err = adopt("--confirm", "acme")
	x.ErrorContains(err, "1 could not be linked")
	x.Contains(said, "fred\t")
	ids, kept = subjectsOf(fred)
	x.Equal([]string{fredOid}, ids)
	x.Empty(kept, "how roster knows him now, so nothing is kept beside it")
	ids, kept = subjectsOf(gina)
	x.Empty(ids, "what somebody else signs in by is not hers to be linked to")
	x.Equal(erinOid, kept)

	// And again lists who is left, which is gina.
	said, err = adopt()
	x.NoError(err)
	x.NotContains(said, "fred")
	x.Contains(said, "gina\t")
}
