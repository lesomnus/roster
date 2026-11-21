package cmd_test

import (
	"regexp"
	"testing"

	"github.com/lesomnus/xli/cfg"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// What else a directory said it calls somebody, for the people below.
const (
	ginaOid = "5e6f7081-92a3-b4c5-d6e7-f8091a2b3c4d"
	lateOid = "6f708192-a3b4-c5d6-e7f8-091a2b3c4d5e"
)

// adoptBed is a deployment with operators, `admin` at the shell, and a tenant
// `acme` whose `entra` connection is the one its directory provisions through.
type adoptBed struct {
	t  *testing.T
	c  cmd.Config
	at *app.TenantRef
}

func newAdoptBed(t *testing.T) *adoptBed {
	t.Helper()
	x := require.New(t)

	c := seedbed(t)
	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)
	// Who is at the shell: the list is people's addresses, and a read of them
	// names its reader.
	c.Client.As = "admin"

	b := &adoptBed{t: t, c: c}
	b.with(func(s *cmd.Server) {
		acme, err := s.Ungated.Tenant().Add(t.Context(), app.TenantAddRequest_builder{Alias: "acme"}.Build())
		x.NoError(err)
		b.at = app.TenantRef_builder{Id: acme.GetId()}.Build()
		_, err = s.Ungated.Connection().Add(t.Context(), app.ConnectionAddRequest_builder{
			Tenant: b.at, Name: "entra", Issuer: "https://login.microsoftonline.com/acme/v2.0", ClientId: "the-app",
			SubjectClaim: "oid", Provisions: true,
		}.Build())
		x.NoError(err)
	})

	return b
}

// with is `fn` on the deployment's database, closed before a command opens it.
func (b *adoptBed) with(fn func(s *cmd.Server)) {
	b.t.Helper()
	s, err := cmd.Build(b.t.Context(), b.c)
	require.NoError(b.t, err)
	defer s.Close()

	fn(s)
}

// person is somebody an operator entered, with an address of `acme`.
func (b *adoptBed) person(s *cmd.Server, alias string) *app.HolderRef {
	b.t.Helper()
	x := require.New(b.t)
	ctx := b.t.Context()

	h, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: b.at, Alias: alias, Name: alias}.Build())
	x.NoError(err)
	ref := app.HolderRef_builder{Id: h.GetId()}.Build()
	_, err = s.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: ref, Address: alias + "@acme.example"}.Build())
	x.NoError(err)

	return ref
}

// tell is the directory saying what it calls somebody.
func (b *adoptBed) tell(s *cmd.Server, ref *app.HolderRef, subject string) {
	b.t.Helper()
	_, err := s.Ungated.Holder().Tell(b.t.Context(), app.HolderTellRequest_builder{Ref: ref, Subject: proto.String(subject)}.Build())
	require.NoError(b.t, err)
}

// adopt is `roster scim adopt --tenant acme` and `args`: what it printed on
// stdout, what it said on stderr, and the error it ended with.
func (b *adoptBed) adopt(args ...string) (string, string, error) {
	b.t.Helper()
	k := cli.NewCmdScim(cfg.New(cmd.Name, &b.c), &b.c)

	// The error is the test's to read, so it is carried past `captured`,
	// which would fail the test on one.
	var said string
	var err error
	out := captured(b.t, func() error {
		said, err = stderrOf(b.t, k, append([]string{"adopt", "--tenant", "acme"}, args...)...)

		return nil
	})

	return out, said, err
}

// listDigest is the digest `adopt` said of the list it printed.
func listDigest(t *testing.T, said string) string {
	t.Helper()

	m := regexp.MustCompile(`(?m)^digest ([0-9a-f]{64})$`).FindStringSubmatch(said)
	require.NotNil(t, m, "no digest in what it said:\n%s", said)

	return m[1]
}

// subjectsOf is the accounts somebody has at `entra`, and what is kept of what
// the directory calls them.
func (b *adoptBed) subjectsOf(ref *app.HolderRef) ([]string, string) {
	b.t.Helper()
	x := require.New(b.t)
	ctx := b.t.Context()

	var out []string
	var kept string
	b.with(func(s *cmd.Server) {
		ids, err := s.Ungated.Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{app.IdentityFilter_builder{Holder: ref}.Build()},
		}.Build())
		x.NoError(err)
		for _, v := range ids.GetItems() {
			x.Equal("entra", v.GetProvider())
			out = append(out, v.GetSubject())
		}
		h, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: ref, Select: app.HolderSelect_builder{DirectorySubject: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		kept = h.GetDirectorySubject()
	})

	return out, kept
}

// TestAnOperatorLinksWhoTheDirectoryMatched: `roster scim adopt` lists the
// people a tenant's directory said it calls something and roster has not
// linked -- who roster has, their address, what the directory calls them --
// and writes nothing until the tenant's alias is typed back with the digest
// of the list. Then each is linked at the connection the directory provisions
// through, which is what lets it suspend them, and what was kept beside them
// is taken away. A subject somebody else already signs in by is refused,
// said, and the rest are linked.
func TestAnOperatorLinksWhoTheDirectoryMatched(t *testing.T) {
	x := require.New(t)
	b := newAdoptBed(t)

	// Three people an operator entered. The directory matched fred and gina
	// and said what it calls each; of henry it said nothing.
	var fred, gina, henry *app.HolderRef
	b.with(func(s *cmd.Server) {
		fred, gina, henry = b.person(s, "fred"), b.person(s, "gina"), b.person(s, "henry")
		b.tell(s, fred, fredOid)
		b.tell(s, gina, erinOid)
	})

	// Listed, and nothing written.
	out, said, err := b.adopt()
	x.NoError(err)
	x.Contains(out, "fred\tfred@acme.example\t"+fredOid)
	x.Contains(out, "gina\tgina@acme.example\t"+erinOid)
	x.NotContains(out, "henry")
	read := listDigest(t, said)
	x.Contains(said, "`--confirm acme --of "+read+"` links them")
	ids, kept := b.subjectsOf(fred)
	x.Empty(ids, "a list is not a link")
	x.Equal(fredOid, kept)

	// The alias typed back is another tenant's: nothing written either.
	_, _, err = b.adopt("--confirm", "contoso", "--of", read)
	x.ErrorContains(err, "nothing was written")
	ids, _ = b.subjectsOf(fred)
	x.Empty(ids)

	// Somebody else comes to sign in by what the directory calls gina, before
	// anybody believed it of her. The list is the same list: it is of who
	// roster has and what the directory calls them.
	b.with(func(s *cmd.Server) {
		_, err = s.Ungated.Identity().Add(t.Context(), app.IdentityAddRequest_builder{Holder: henry, Provider: "entra", Subject: erinOid}.Build())
		x.NoError(err)
	})

	// Believed: fred is linked, and gina is refused and said.
	out, _, err = b.adopt("--confirm", "acme", "--of", read)
	x.ErrorContains(err, "1 could not be linked")
	x.Contains(out, "fred\t")
	ids, kept = b.subjectsOf(fred)
	x.Equal([]string{fredOid}, ids)
	x.Empty(kept, "how roster knows him now, so nothing is kept beside it")
	ids, kept = b.subjectsOf(gina)
	x.Empty(ids, "what somebody else signs in by is not hers to be linked to")
	x.Equal(erinOid, kept)

	// And again lists who is left, which is gina.
	out, _, err = b.adopt()
	x.NoError(err)
	x.NotContains(out, "fred")
	x.Contains(out, "gina\t")
}

// TestAdoptLinksTheListThatWasReadOrNothing: what `--confirm` links is the
// list an operator read, and nothing else.
//
// `--confirm <alias>` carried nothing of the list, and the run that wrote read
// the matched people afresh: somebody the directory told of between the two
// runs was linked -- an account at the connection, which is a way into theirs
// -- without being on any list anybody read. Answered: linked; right is
// nothing written. So the list is named by its digest, as `roster trail rows
// --accept --of` names what it accepts, and a run that finds another list
// writes nothing, prints the list as it is now and fails.
func TestAdoptLinksTheListThatWasReadOrNothing(t *testing.T) {
	x := require.New(t)
	b := newAdoptBed(t)

	var fred, gina, late *app.HolderRef
	b.with(func(s *cmd.Server) {
		fred, gina, late = b.person(s, "fred"), b.person(s, "gina"), b.person(s, "late")
		b.tell(s, fred, fredOid)
		b.tell(s, gina, ginaOid)
	})

	out, said, err := b.adopt()
	x.NoError(err)
	x.NotContains(out, "late")
	read := listDigest(t, said)

	// Told of after the list was read.
	b.with(func(s *cmd.Server) { b.tell(s, late, lateOid) })

	out, said, err = b.adopt("--confirm", "acme", "--of", read)
	x.ErrorContains(err, read)
	x.ErrorContains(err, "nothing was written")
	now := listDigest(t, said)
	x.NotEqual(read, now)
	x.ErrorContains(err, now)
	x.Contains(out, "late\tlate@acme.example\t"+lateOid, "the list as it is now")
	for _, ref := range []*app.HolderRef{fred, gina, late} {
		ids, _ := b.subjectsOf(ref)
		x.Empty(ids, "linked from a list nobody read")
	}

	// The list read again, and its digest: linked, all three.
	_, said, err = b.adopt()
	x.NoError(err)
	x.Equal(now, listDigest(t, said))
	_, _, err = b.adopt("--confirm", "acme", "--of", now)
	x.NoError(err)
	for ref, subject := range map[*app.HolderRef]string{fred: fredOid, gina: ginaOid, late: lateOid} {
		ids, _ := b.subjectsOf(ref)
		x.Equal([]string{subject}, ids)
	}
}

// TestAdoptsConfirmAndItsDigestGoTogether: `--confirm` without `--of` is
// refused before anything is read, since it says nothing of which list was
// believed; and `--of` without `--confirm` is refused too, since a digest
// alone believes nothing.
func TestAdoptsConfirmAndItsDigestGoTogether(t *testing.T) {
	x := require.New(t)
	b := newAdoptBed(t)

	var fred *app.HolderRef
	b.with(func(s *cmd.Server) {
		fred = b.person(s, "fred")
		b.tell(s, fred, fredOid)
	})

	out, _, err := b.adopt("--confirm", "acme")
	x.ErrorContains(err, "--of: the digest `roster scim adopt` printed of the list that was read; nothing was written")
	x.Empty(out, "nothing is read, so nothing is printed")

	_, said, err := b.adopt()
	x.NoError(err)
	out, _, err = b.adopt("--of", listDigest(t, said))
	x.ErrorContains(err, "--of: goes with --confirm")
	x.Empty(out)

	ids, _ := b.subjectsOf(fred)
	x.Empty(ids)
}
