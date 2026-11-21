package cmd_test

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch/vouchtest"
)

// boxWritten is how many lines each plane's trail and access record hold:
// what a command that wrote nothing leaves as it was.
func boxWritten(t *testing.T, c cmd.Config) [4]int {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()

	var n [4]int
	for i, db := range []*ent.Client{s.Ent, s.Control.Ent} {
		n[2*i], err = db.Audit.Query().Count(ctx)
		x.NoError(err)
		n[2*i+1], err = db.Access.Query().Count(ctx)
		x.NoError(err)
	}

	return n
}

// TestANameGivenAtTheBoxIsLookedUpWhereverItIsGiven: `--as` given to any
// command at the box that opens the deployment is looked up, and a name that
// is nobody's is refused before anything is written.
//
// It was read by the entity commands, `key list`, `scim adopt` and `trail rows
// --accept`, and by nothing else: `roster --as nobody vouch reset
// @newco/alice` printed a password and exited 0, and the name was never looked
// up. Answered: a password; right is the refusal the entity commands give,
// since somebody who gave the name believes the record says it -- the reason
// the wire refuses the flag.
func TestANameGivenAtTheBoxIsLookedUpWhereverItIsGiven(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	_, _, err := boxRun(t, at(b.c, "admin"), "", "holder", "add", "@newco/alice")
	x.NoError(err)
	_, said, err := boxRun(t, at(b.c, "admin"), "", "key", "add", "--tenant", "newco", "--holder", "alice", "--allow", "/roster.MeService/Get")
	x.NoError(err)
	k := keyIdSaid(t, said)
	_, _, err = boxRun(t, at(b.c, "admin"), "", "holder", "erase", "@newco/alice")
	x.NoError(err)

	before := boxWritten(t, b.c)
	for _, v := range []struct {
		stdin string
		args  []string
	}{
		{"", []string{"vouch", "reset", "@newco/admin"}},
		{"a-long-enough-password-for-admin\n", []string{"vouch", "set", "--password-stdin", "@newco/admin"}},
		{"", []string{"vouch", "unlock", "@newco/admin"}},
		{"", []string{"control", "vouch", "reset", "@admin"}},
		{"", []string{"control", "vouch", "unlock", "@admin"}},
		{"", []string{"key", "add", "--tenant", "newco", "--holder", "admin", "--allow", "/roster.MeService/Get"}},
		{"", []string{"key", "revoke", "--id", k}},
		{"", []string{"control", "key", "add", "--allow", "/roster.MeService/Get", "loft"}},
		{"", []string{"restore", "@newco/alice"}},
		{"", []string{"forget", "--dry-run", "@newco/alice"}},
		{"", []string{"hold", "place", "--why", "a claim", "@newco"}},
		{"", []string{"hold", "ls"}},
		{"", []string{"trail", "owed"}},
		{"", []string{"init"}},
	} {
		out, _, err := boxRun(t, at(b.c, "nobody"), v.stdin, v.args...)
		x.ErrorContains(err, "--as nobody: no operator is called that", "%v", v.args)
		x.Empty(out, "%v printed something for nobody", v.args)
	}
	x.Equal(before, boxWritten(t, b.c), "a command named for nobody wrote something")

	// `trail accept` keeps who `--as` names where `--by` said it, and the two
	// together are two answers to one question.
	_, _, err = boxRun(t, at(b.c, "admin"), "", "trail", "accept", "--in", t.TempDir(), "--of", "sha256:00", "--why", "looked", "--by", "somebody")
	x.ErrorContains(err, "--by: --as says who accepts")

	t.Run("and one who is stopped is refused as well", func(t *testing.T) {
		x := require.New(t)

		_, _, err := boxRun(t, at(b.c, "admin"), "", "control", "key", "add", "--allow", "/roster.MeService/Get", "grace")
		x.NoError(err)
		_, _, err = boxRun(t, at(b.c, "admin"), "", "control", "holder", "disable", "@owner/grace")
		x.NoError(err)

		before := boxWritten(t, b.c)
		out, _, err := boxRun(t, at(b.c, "grace"), "", "vouch", "reset", "@newco/admin")
		x.ErrorContains(err, "disabled")
		x.Empty(out)
		x.Equal(before, boxWritten(t, b.c))
	})
}

// boxRows is what a command at the box left on a plane's access record: the
// rows that were not there in `before`.
func boxRows(t *testing.T, c cmd.Config, control bool, before map[pdid.Id]bool) []*ent.Access {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()

	db := s.Ent
	if control {
		db = s.Control.Ent
	}
	vs, err := db.Access.Query().All(ctx)
	x.NoError(err)

	out := []*ent.Access{}
	for _, v := range vs {
		if !before[pdid.Id(v.Id)] {
			out = append(out, v)
		}
	}

	return out
}

// boxSeen is every row of a plane's access record now, for [boxRows].
func boxSeen(t *testing.T, c cmd.Config, control bool) map[pdid.Id]bool {
	t.Helper()

	out := map[pdid.Id]bool{}
	for _, v := range boxRows(t, c, control, nil) {
		out[pdid.Id(v.Id)] = true
	}

	return out
}

// TestAWayInWrittenAtTheBoxIsOnTheRecordUnderWhoeverWasNamed: a command at
// the box that writes somebody's way in or their standing, given `--as`,
// leaves on the access record the rows the same write through the entity tree
// leaves -- a write's, naming the operator and whose row it wrote -- and no
// read's, since it shows nothing of the row; given no name, it leaves none,
// as before.
//
// `key add`, `key revoke`, `control key add`, `vouch reset`, `set` and
// `unlock` and their `control` forms, and `restore` of a person each wrote
// with nobody named and no row of the access record, where `roster --as …
// api-key add` of the same key was one. Answered: no row; right is the row
// the entity tree leaves. Whether these commands require a name is the
// owner's, and is not done here.
func TestAWayInWrittenAtTheBoxIsOnTheRecordUnderWhoeverWasNamed(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	// A key to seal an authenticator's seed with, for `control vouch enrol`.
	b.c.Vouch.Keys = []string{"one:" + strings.Repeat("QUFB", 10) + "QUE="}

	_, _, err := boxRun(t, at(b.c, "admin"), "", "holder", "add", "@newco/alice")
	x.NoError(err)
	_, _, err = boxRun(t, at(b.c, "admin"), "", "holder", "add", "@newco/bob")
	x.NoError(err)

	// What a command left on a plane, and that each row is a write naming the
	// operator --as named.
	run := func(control bool, stdin string, args ...string) ([]*ent.Access, string) {
		t.Helper()
		seen := boxSeen(t, b.c, control)
		_, said, err := boxRun(t, at(b.c, "admin"), stdin, args...)
		x.NoError(err, "%v: %s", args, said)
		rs := boxRows(t, b.c, control, seen)
		for _, r := range rs {
			x.Equal(b.operator.Uuid(), r.ActorId, "%v: %s names the operator --as named", args, r.Action)
			x.Equal(b.owner.Uuid(), r.ActorTenantId, "%v", args)
			x.Equal("ok", r.Result, "%v", args)
		}

		return rs, said
	}
	writes := func(rs []*ent.Access) []*ent.Access {
		out := []*ent.Access{}
		for _, r := range rs {
			if r.Kind == "write" {
				out = append(out, r)
			}
		}
		return out
	}
	alike := func(what string, byBox, byHand []*ent.Access) {
		t.Helper()
		x.NotEmpty(byHand, "%s: the entity tree leaves no row, so this would prove nothing", what)
		x.Len(byBox, len(writes(byHand)), "%s: the rows of the write, and no read's", what)
		for i := range byBox {
			x.Equal("write", byBox[i].Kind, what)
			x.Len(byBox[i].Subjects, len(writes(byHand)[i].Subjects), what)
		}
	}
	secret := base64.StdEncoding.EncodeToString([]byte("abcdefghabcdefghabcdefghabcdefgh"))

	// A key minted and revoked.
	{
		byBox, said := run(false, "", "key", "add", "--tenant", "newco", "--holder", "alice", "--allow", "/roster.MeService/Get")
		k := keyIdSaid(t, said)
		byHand, _ := run(false, "", "api-key", "add",
			fmt.Sprintf(`{"holder":{"slug":{"alias":"bob","tenant":{"alias":"newco"}}},"alias":"default","secret":%q,"methods":["/roster.MeService/Get"]}`, secret))
		alike("key add", byBox, byHand)
		x.Equal(app.ApiKeyService_Issue_FullMethodName, byBox[0].Action)
		x.Equal(k, pdid.Id(byBox[0].Subjects[0]).String(), "the row is of the key written")

		byBox, _ = run(false, "", "key", "revoke", "--id", k)
		vs := boxRows(t, b.c, false, nil)
		hand := ""
		for _, v := range vs {
			if v.Action == app.ApiKeyService_Add_FullMethodName && v.Kind == "write" {
				hand = pdid.Id(v.Subjects[0]).String()
			}
		}
		byHand, _ = run(false, "", "api-key", "erase", hand)
		alike("key revoke", byBox, byHand)
		x.Equal(app.ApiKeyService_Erase_FullMethodName, byBox[0].Action)
	}

	// A key of the deployment's, for somebody new.
	{
		byBox, _ := run(true, "", "control", "key", "add", "--allow", "/roster.MeService/Get", "loft")
		made, _ := run(true, "", "control", "holder", "add", "@owner/zed")
		minted, _ := run(true, "", "control", "api-key", "add",
			fmt.Sprintf(`{"holder":{"slug":{"alias":"zed","tenant":{"alias":"owner"}}},"alias":"default","secret":%q,"methods":["/roster.MeService/Get"]}`, secret))
		alike("control key add", byBox, append(made, minted...))
		x.Equal(app.HolderService_Add_FullMethodName, byBox[0].Action)
		x.Equal(app.ApiKeyService_Issue_FullMethodName, byBox[1].Action)
	}

	// A password reset, set and unlocked.
	{
		for _, plane := range []struct {
			control bool
			prefix  []string
			who     string
			id      pdid.Id
		}{
			{false, nil, "@newco/alice", pdid.Nil},
			{true, []string{"control"}, "@admin", b.operator},
		} {
			args := func(vs ...string) []string { return append(append([]string{}, plane.prefix...), vs...) }

			// A reset writes their credential and ends their sessions: a
			// write of their row.
			rs, _ := run(plane.control, "", args("vouch", "reset", plane.who)...)
			x.Len(rs, 1, "%v", plane.prefix)
			x.Equal("write", rs[0].Kind)
			x.Equal(app.CredentialService_Issue_FullMethodName, rs[0].Action)
			if plane.control {
				x.Contains(rs[0].Subjects, plane.id.Bytes())
			}

			// A password has no name, so setting or unlocking one writes no
			// field declared personal: whatever row there is, it is a write
			// naming the operator, which `run` holds it to.
			rs, _ = run(plane.control, "a-long-enough-password-of-theirs\n", args("vouch", "set", "--password-stdin", plane.who)...)
			x.Len(writes(rs), len(rs))
			rs, _ = run(plane.control, "", args("vouch", "unlock", plane.who)...)
			x.Len(writes(rs), len(rs))
		}
	}

	// An operator's second factor, enrolled and proved: each a write naming
	// the operator, which `run` holds it to, as the reset beside it is.
	{
		seen := boxSeen(t, b.c, true)
		uri, said, err := boxRun(t, at(b.c, "admin"), "", "control", "vouch", "enrol", "@admin")
		x.NoError(err, said)
		rs := boxRows(t, b.c, true, seen)
		x.Len(writes(rs), len(rs))
		for _, r := range rs {
			x.Equal(b.operator.Uuid(), r.ActorId, "enrol: %s names the operator --as named", r.Action)
		}

		u, err := url.Parse(strings.TrimSpace(uri))
		x.NoError(err)
		rs, _ = run(true, vouchtest.CodeBefore(seedOf(t, u.Query().Get("secret")))+"\n", "control", "vouch", "prove", "--code-stdin", "@admin")
		x.Len(writes(rs), len(rs))
	}

	// Somebody restored.
	{
		byHand, _ := run(false, "", "holder", "erase", "@newco/bob")
		byBox, _ := run(false, "", "restore", "@newco/bob")
		alike("restore", byBox, byHand)
		x.Equal("roster.holder.restore", byBox[0].Action)
		x.Equal(byHand[0].Subjects, byBox[0].Subjects, "the row is of the person brought back")
		x.Equal(byHand[0].TenantId, byBox[0].TenantId)
	}

	// And with no name, nothing is kept, as before.
	{
		seen := boxSeen(t, b.c, false)
		_, _, err := boxRun(t, at(b.c, ""), "", "key", "add", "--tenant", "newco", "--holder", "bob", "--name", "bare", "--allow", "/roster.MeService/Get")
		x.NoError(err)
		x.Empty(boxRows(t, b.c, false, seen))
	}
}

// TestANameGivenBeforeAnybodyExistsIsRefusedInWords: a name given at the box
// before `roster init` has made the control plane's tables is refused in
// roster's words, and nothing is made earlier than the command makes it.
//
// Once a name was looked up wherever it is given, `resources apply`, `login
// provision` and `control key add` -- each of which makes its tables where
// `migrate` says it may -- asked the control plane who was named before they
// made them, and answered the database's `no such table`. Answered: the
// database's words; right is that nobody can be named before `init`.
func TestANameGivenBeforeAnybodyExistsIsRefusedInWords(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	c.Db.Migrate = true
	c.Control.Db.Migrate = true

	f := filepath.Join(t.TempDir(), "resources.yaml")
	x.NoError(os.WriteFile(f, []byte("resources:\n  - kind: Tenant\n    alias: contoso\n"), 0o600))

	for _, args := range [][]string{
		{"resources", "apply", "--file", f},
		{"login", "provision", "--out", t.TempDir()},
		{"control", "key", "add", "--allow", "/roster.MeService/Get", "svc"},
	} {
		out, _, err := boxRun(t, at(c, "admin"), "", args...)
		x.ErrorContains(err, "--as admin: nobody can be named at this shell before `roster init` has made the first operator", "%v", args)
		x.NotContains(err.Error(), "no such table", "%v", args)
		x.Empty(out, "%v", args)
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	_, err = s.Control.Ent.Tenant.Query().Count(ctx)
	x.Error(err, "a command made the control plane's tables before it was told who is at the shell")
}
