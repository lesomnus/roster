package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
)

// dryCounted is how many rows each plane holds of what a run of a file, or the
// provisioner it writes as, would add: the trail, people, roles, bindings,
// tenants and the rows a file declares.
func dryCounted(t *testing.T, c cmd.Config) map[string]int {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()

	out := map[string]int{}
	for plane, db := range map[string]*ent.Client{"data": s.Ent, "control": s.Control.Ent} {
		for what, n := range map[string]func(context.Context) (int, error){
			"audit":   db.Audit.Query().Count,
			"holder":  db.Holder.Query().Count,
			"role":    db.Role.Query().Count,
			"binding": db.Binding.Query().Count,
			"tenant":  db.Tenant.Query().Count,
			"host":    db.Host.Query().Count,

			"connection":  db.Connection.Query().Count,
			"mail domain": db.MailDomain.Query().Count,
		} {
			v, err := n(ctx)
			x.NoError(err)
			out[plane+" "+what] = v
		}
	}

	return out
}

// TestADryRunWritesNothing: `roster resources apply --dry-run` writes no row
// and alters no table, on either plane.
//
// # What it was
//
// `ApplyResources` made the provisioner before it looked at `dry`, and the
// command migrated the data plane where `db.migrate` was on and never looked
// at the control plane. So a dry run on a deployment `init`ed and never served
// with a file added the holder `provisioner` and its binding to `everything`,
// two lines on the control plane's trail, under *dry run; nothing was
// written*; and on one nothing had started it made the data plane's tables and
// then failed on the control plane's.
//
// A dry run looks the provisioner up and never makes it, and asks of both
// planes what `Ready` asks without migrating: what somebody runs before a
// deployment does it for them must not be the first thing that does it.
func TestADryRunWritesNothing(t *testing.T) {
	const file = "resources:\n  - kind: Tenant\n    alias: contoso\n    name: Contoso\n  - kind: Host\n    tenant: contoso\n    name: contoso.example\n"

	t.Run("a deployment never served with a file", func(t *testing.T) {
		x := require.New(t)
		c := seedbed(t)
		out, err := initRun(t, c)
		x.NoError(err, out)
		c.Db.Migrate, c.Control.Db.Migrate = true, true

		was := dryCounted(t, c)
		out, err = entities(t, &c, "resources", "apply", "--dry-run", "--file", declare(t, file)[0])
		x.NoError(err)
		x.Contains(out, "contoso")
		x.Equal(was, dryCounted(t, c), "a dry run wrote")

		_, err = cmd.ControlHolder(t.Context(), dryBuilt(t, c).Control, cmd.Provisioner)
		x.Error(err, "a dry run made the provisioner")
	})

	t.Run("a deployment nothing has started", func(t *testing.T) {
		x := require.New(t)
		c := seedbed(t)
		c.Db.Migrate, c.Control.Db.Migrate = true, true

		_, err := entities(t, &c, "resources", "apply", "--dry-run", "--file", declare(t, file)[0])
		x.Error(err, "a dry run on no tables")

		s := dryBuilt(t, c)
		_, err = s.Ent.Tenant.Query().Count(t.Context())
		x.Error(err, "a dry run made the data plane's tables")
		_, err = s.Control.Ent.Tenant.Query().Count(t.Context())
		x.Error(err, "a dry run made the control plane's tables")
	})
}

// dryBuilt is the deployment `c` describes, closed when the test ends.
func dryBuilt(t *testing.T, c cmd.Config) *cmd.Server {
	t.Helper()
	s, err := cmd.Build(t.Context(), c)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	return s
}

// TestADryRunRefusesWhatARunRefuses: `roster resources apply --dry-run` says
// of every row what the run would say of it, a refusal as much as a write,
// with the run's exit status, and writes nothing.
//
// # What it was
//
// A dry run read each row and answered `+` where there was none and `~` where
// it differed, and passed none of them through the checks the run makes on
// the way to the write. So `+ Connection @contoso/Entra`, exit 0, for a row
// the run refused `InvalidArgument`, exit 1: a `name` that is no alias, an
// issuer `https://GitHub.com`, `subject_claim: oid` at GitHub or Google, no
// `client_id`. The same of every kind a file declares: a tenant's front door
// that is no origin, a host's and a mail domain's name not written as it is
// compared, and any row under a tenant that is not there. A dry run is what
// somebody runs before a deployment does it for them (`docs/operating.md`
// § *Rows a file declares*), and one that promised what the start then
// refused is a pod that does not come up.
//
// Each row is held to every check the run makes and stops short only of the
// write ([core.Dry]); what the run would answer, the dry run answers.
func TestADryRunRefusesWhatARunRefuses(t *testing.T) {
	x := require.New(t)
	c := seedbed(t)
	out, err := initRun(t, c)
	x.NoError(err, out)

	_, err = entities(t, &c, "resources", "apply", "--file", declare(t, "resources:\n  - kind: Tenant\n    alias: contoso\n")[0])
	x.NoError(err)

	connection := func(fields string) string {
		return "resources:\n  - kind: Connection\n    tenant: contoso\n" + fields
	}
	for _, v := range []struct {
		what, file string
	}{
		{"a name that is no alias", connection("    name: Entra\n    issuer: https://login.microsoftonline.com/x/v2.0\n    client_id: c\n")},
		{"an issuer not written as it is compared", connection("    name: github\n    issuer: https://GitHub.com\n    client_id: c\n")},
		{"oid at GitHub", connection("    name: github\n    issuer: https://github.com\n    client_id: c\n    subject_claim: oid\n")},
		{"oid at Google", connection("    name: google\n    issuer: https://accounts.google.com\n    client_id: c\n    subject_claim: oid\n")},
		{"no client_id", connection("    name: entra\n    issuer: https://login.microsoftonline.com/x/v2.0\n")},
		{"a connection under a tenant that is not there", "resources:\n  - kind: Connection\n    tenant: nosuch\n    name: entra\n    issuer: https://login.microsoftonline.com/x/v2.0\n    client_id: c\n"},
		{"a tenant's front door that is no origin", "resources:\n  - kind: Tenant\n    alias: fabrikam\n    config:\n      front_door: https://fabrikam.example/sign-in\n"},
		{"a front door changed to one that is no origin", "resources:\n  - kind: Tenant\n    alias: contoso\n    config:\n      front_door: https://contoso.example/sign-in\n"},
		{"a host's name not written as it is compared", "resources:\n  - kind: Host\n    tenant: contoso\n    name: Contoso.Example\n"},
		{"a host under a tenant that is not there", "resources:\n  - kind: Host\n    tenant: nosuch\n    name: nosuch.example\n"},
		{"a mail domain's name not written as it is compared", "resources:\n  - kind: MailDomain\n    tenant: contoso\n    name: Contoso.Example\n"},
		{"a mail domain under a tenant that is not there", "resources:\n  - kind: MailDomain\n    tenant: nosuch\n    name: nosuch.example\n"},
	} {
		file := declare(t, v.file)[0]

		was := dryCounted(t, c)
		_, dry := entities(t, &c, "resources", "apply", "--dry-run", "--file", file)
		x.Equal(was, dryCounted(t, c), "%s: a dry run wrote", v.what)

		// Each case said, rather than the first that fails alone: the cases
		// are the kinds, and a kind that is held is not the next one.
		_, run := entities(t, &c, "resources", "apply", "--file", file)
		x.Error(run, "%s: the run took it", v.what)
		if !assert.Error(t, dry, "%s: a dry run promised what the run refuses: %v", v.what, run) {
			continue
		}
		assert.Equal(t, run.Error(), dry.Error(), v.what)
		assert.Equal(t, cli.Status(run), cli.Status(dry), "%s: the exit status", v.what)
	}
}

// TestADryRunSaysWhatOnlyTheWriteCanAnswer: what a dry run cannot ask of a
// row before the writes ahead of it are made, it says under the row, and
// neither refuses the row for it nor promises it.
//
// Two of the run's rules read what an earlier row of the files writes: a
// connection that provisions reads its tenant, which an earlier row may add,
// and one of a tenant's connections provisions, which an earlier row may move.
// A dry run that writes nothing finds the tenant not there and the old
// connection still marked, and refused rows the run takes.
func TestADryRunSaysWhatOnlyTheWriteCanAnswer(t *testing.T) {
	x := require.New(t)
	c := seedbed(t)
	out, err := initRun(t, c)
	x.NoError(err, out)

	const entra = "    issuer: https://login.microsoftonline.com/x/v2.0\n    client_id: c\n"

	t.Run("a row of a tenant an earlier row adds", func(t *testing.T) {
		x := require.New(t)
		file := declare(t, "resources:\n  - kind: Tenant\n    alias: fabrikam\n"+
			"  - kind: Connection\n    tenant: fabrikam\n    name: entra\n    provisions: true\n"+entra)[0]

		out, err := entities(t, &c, "resources", "apply", "--dry-run", "--file", file)
		x.NoError(err, "refused for a tenant the run adds first")
		x.Contains(out, "+ Connection @fabrikam/entra\n    not asked yet: @fabrikam is added by an earlier row")

		_, err = entities(t, &c, "resources", "apply", "--file", file)
		x.NoError(err)
	})

	t.Run("a connection that provisions where an earlier row moves which does", func(t *testing.T) {
		x := require.New(t)
		_, err := entities(t, &c, "resources", "apply", "--file", declare(t, "resources:\n  - kind: Tenant\n    alias: contoso\n"+
			"  - kind: Connection\n    tenant: contoso\n    name: entra\n    provisions: true\n"+entra)[0])
		x.NoError(err)

		file := declare(t, "resources:\n  - kind: Tenant\n    alias: contoso\n"+
			"  - kind: Connection\n    tenant: contoso\n    name: entra\n"+entra+
			"  - kind: Connection\n    tenant: contoso\n    name: okta\n    provisions: true\n    issuer: https://contoso.okta.com\n    client_id: c\n")[0]

		out, err := entities(t, &c, "resources", "apply", "--dry-run", "--file", file)
		x.NoError(err, "refused for a mark the run takes off first")
		x.Contains(out, "+ Connection @contoso/okta\n    not asked yet: an earlier row moves which connection of @contoso provisions")
		x.Contains(out, "~ Connection @contoso/entra: provisions\n")

		_, err = entities(t, &c, "resources", "apply", "--file", file)
		x.NoError(err)
	})
}
