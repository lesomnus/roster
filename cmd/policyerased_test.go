package cmd_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/rbac/rbacfile"
	"github.com/lesomnus/payday/trail"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
)

// TestSomebodyErasedDoesNotStopTheDeploymentFromStarting: a policy file's
// holders are checked against the database each time the file is read, and
// the check asked for somebody who is there *and not erased*. A running server
// keeps the policy it had when a read is refused; one that starts has none to
// keep, and does not start. So erasing one person -- which their own tenant's
// administrator may do, and which they may do themselves by leaving -- decided
// whether every tenant was served after the next restart: `Build` answered
// *policy holder @…/…: no live holder in this tenant*; right is a server.
//
// What the check is for is a subject nobody is yet: a row made afterwards
// under an identifier the file already names would be handed its grant. An
// erased person's row is still there, forgotten or not, with their tenant
// destroyed or not, so nobody can be made under their identifier. Such a
// subject is granted nothing, and the log says so when it becomes so, naming
// them and the file, so that whoever keeps the file takes them out of it.
func TestSomebodyErasedDoesNotStopTheDeploymentFromStarting(t *testing.T) {
	for name, goes := range map[string]func(t *testing.T, ctx context.Context, b *built, who pdid.Id){
		"erased": func(t *testing.T, ctx context.Context, b *built, who pdid.Id) {
			_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
			require.NoError(t, err)
		},
		"forgotten": func(t *testing.T, ctx context.Context, b *built, who pdid.Id) {
			_, err := forget.Forget(ctx, b.Ent, who, trail.Policy{}, audit.Contents{}, nil)
			require.NoError(t, err)
		},
		"gone with their tenant": func(t *testing.T, ctx context.Context, b *built, who pdid.Id) {
			closes(t, ctx, b.Ungated, b.Contoso)
			_, err := forget.DestroyTenant(ctx, b.Ent, b.Contoso, trail.Policy{}, audit.Contents{}, nil)
			require.NoError(t, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			p := filepath.Join(t.TempDir(), "policy.yaml")
			x.NoError(os.WriteFile(p, []byte("{}\n"), 0600))

			var c cmd.Config
			b, ctx := build(t, func(v *cmd.Config) {
				v.Policy = rbacfile.Config{File: p}
				c = *v
			})

			// Somebody the file names twice: by a binding of their own, and as
			// one of a group's members.
			who := b.holder(t, ctx, b.Contoso, "low")
			subject := "@" + b.Contoso.String() + "/" + who.String()
			x.NoError(os.WriteFile(p, []byte(fmt.Sprintf(`roles:
  '@%[1]s/reader':
    methods: [/roster.HolderService/List]
groups:
  '@%[1]s/readers':
    members: ['%[2]s']
bindings:
  - role: '@%[1]s/reader'
    holder: '%[2]s'
  - role: '@%[1]s/reader'
    group: '@%[1]s/readers'
`, b.Contoso, subject)), 0600))
			x.NoError(b.PolicyFile.Refresh())
			x.NoError(b.PolicyFile.Refresh())

			gs, err := cmd.Granted(b.Ent, b.PolicyFile)(ctx, who)
			x.NoError(err)
			x.NotEmpty(gs, "the file granted them nothing to begin with, so this proves nothing")

			goes(t, ctx, b, who)

			// A server that starts, starts.
			said := &bytes.Buffer{}
			s, err := cmd.Build(log.Into(ctx, slog.New(slog.NewJSONHandler(said, nil))), c)
			x.NoError(err, "one person erased, and the deployment does not start")
			t.Cleanup(func() { s.Close() })

			gs, err = cmd.Granted(s.Ent, s.PolicyFile)(ctx, who)
			x.NoError(err)
			x.Empty(gs, "somebody erased holds what a file says")

			// Said where whoever keeps the file reads, naming who to take out.
			x.Contains(said.String(), `"level":"WARN"`, "nothing a person watches says the file names somebody erased")
			x.Contains(said.String(), subject, "the log does not say who to take out of the file")
			x.Contains(said.String(), p, "the log does not say which file")

			// And the one that was running reads the file again and keeps it,
			// where it kept the policy it had and said every few seconds that
			// the file was refused.
			x.NoError(b.PolicyFile.Refresh(), "a running server refused the file it was started with")
			gs, err = cmd.Granted(b.Ent, b.PolicyFile)(ctx, who)
			x.NoError(err)
			x.Empty(gs, "somebody erased holds what a file says, on the server that was running")
		})
	}

	// The file is read every few seconds (`policy.refresh`), and nothing about
	// it changes between one read and the next until somebody edits it. A
	// line at every read is a log nobody reads by the afternoon. It is said
	// when it becomes so: once at a start, and again when who the file names
	// that is erased changes -- somebody else erased, somebody restored.
	t.Run("and it is said when it becomes so, and not at every read", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "policy.yaml")
		x.NoError(os.WriteFile(p, []byte("{}\n"), 0600))

		var c cmd.Config
		b, ctx := build(t, func(v *cmd.Config) {
			v.Policy = rbacfile.Config{File: p}
			c = *v
		})

		low, mid := b.holder(t, ctx, b.Contoso, "low"), b.holder(t, ctx, b.Contoso, "mid")
		named := func(who pdid.Id) string { return "@" + b.Contoso.String() + "/" + who.String() }
		x.NoError(os.WriteFile(p, []byte(fmt.Sprintf(`roles:
  '@%[1]s/reader':
    methods: [/roster.HolderService/List]
bindings:
  - role: '@%[1]s/reader'
    holder: '%[2]s'
  - role: '@%[1]s/reader'
    holder: '%[3]s'
`, b.Contoso, named(low), named(mid))), 0600))

		erase := func(who pdid.Id) {
			_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
			x.NoError(err)
		}
		erase(low)

		said := &bytes.Buffer{}
		s, err := cmd.Build(log.Into(ctx, slog.New(slog.NewJSONHandler(said, nil))), c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })

		// lines is every time it was said, in order.
		lines := func() []string {
			var out []string
			for _, line := range strings.Split(said.String(), "\n") {
				if strings.Contains(line, "the file names somebody who has been erased") {
					out = append(out, line)
				}
			}

			return out
		}
		read := func() {
			t.Helper()
			x.NoError(s.PolicyFile.Refresh())
		}

		x.Len(lines(), 1, "not said at the start")
		x.Contains(lines()[0], named(low))
		x.NotContains(lines()[0], named(mid))

		read()
		read()
		x.Len(lines(), 1, "two reads with nothing changed between them, and it is said again")

		// Somebody else the file names is erased: said again, naming both.
		erase(mid)
		read()
		x.Len(lines(), 2, "somebody else was erased, and nothing says so")
		x.Contains(lines()[1], named(low))
		x.Contains(lines()[1], named(mid))
		read()
		x.Len(lines(), 2)

		// Somebody is restored: said again, naming who is left.
		x.NoError(forget.Restore(ctx, s.Ent, low, audit.Contents{}, nil))
		read()
		x.Len(lines(), 3, "somebody was restored, and the log still names them as erased")
		x.NotContains(lines()[2], named(low))
		x.Contains(lines()[2], named(mid))

		// Nobody is left: there is nothing to say, and nothing is said.
		x.NoError(forget.Restore(ctx, s.Ent, mid, audit.Contents{}, nil))
		read()
		read()
		x.Len(lines(), 3, "a warning that names nobody")

		// And erased again after that, it is news again.
		erase(low)
		read()
		x.Len(lines(), 4, "erased again after the set was empty, and nothing says so")
		x.Contains(lines()[3], named(low))
	})

	// The file never stopped naming them, so an erase undone while its grace
	// lasts is undone here too, with nothing for anybody to edit back.
	t.Run("and restored, they hold what the file says again", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "policy.yaml")
		x.NoError(os.WriteFile(p, []byte("{}\n"), 0600))
		b, ctx := build(t, func(v *cmd.Config) { v.Policy = rbacfile.Config{File: p} })

		who := b.holder(t, ctx, b.Contoso, "low")
		x.NoError(os.WriteFile(p, []byte(fmt.Sprintf(
			"roles:\n  '@%[1]s/reader':\n    methods: [/roster.HolderService/List]\nbindings:\n  - role: '@%[1]s/reader'\n    holder: '@%[1]s/%[2]s'\n",
			b.Contoso, who)), 0600))
		x.NoError(b.PolicyFile.Refresh())
		x.NoError(b.PolicyFile.Refresh())

		_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
		x.NoError(err)
		x.NoError(b.PolicyFile.Refresh())
		gs, err := cmd.Granted(b.Ent, b.PolicyFile)(ctx, who)
		x.NoError(err)
		x.Empty(gs)

		x.NoError(forget.Restore(ctx, b.Ent, who, audit.Contents{}, nil))
		x.NoError(b.PolicyFile.Refresh())
		gs, err = cmd.Granted(b.Ent, b.PolicyFile)(ctx, who)
		x.NoError(err)
		x.NotEmpty(gs, "somebody restored was left without what the file gives them")
	})

	// The other half stays: a subject nobody ever was is a file that is wrong,
	// and a server does not start on one.
	t.Run("and somebody who never was is still a file that is wrong", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "policy.yaml")
		x.NoError(os.WriteFile(p, []byte("{}\n"), 0600))

		var c cmd.Config
		b, ctx := build(t, func(v *cmd.Config) {
			v.Policy = rbacfile.Config{File: p}
			c = *v
		})

		// An identifier of the right shape that no row has, and somebody real
		// named in a tenant they are not of.
		for _, subject := range []string{
			"@" + b.Contoso.String() + "/" + pdid.New(b.ContosoUser.Domain()).String(),
			"@" + b.Fabrikam.String() + "/" + b.ContosoUser.String(),
		} {
			tenant := subject[1 : 1+len(b.Contoso.String())]
			x.NoError(os.WriteFile(p, []byte(fmt.Sprintf(
				"roles:\n  '@%[1]s/reader':\n    methods: [/roster.HolderService/List]\nbindings:\n  - role: '@%[1]s/reader'\n    holder: '%[2]s'\n",
				tenant, subject)), 0600))

			s, err := cmd.Build(ctx, c)
			if s != nil {
				s.Close()
			}
			x.ErrorContains(err, "policy:", "a file naming nobody was accepted at start")
			x.ErrorContains(err, subject)
		}
	})
}
