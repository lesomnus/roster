package cmd_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
)

// readBed is a deployment with both planes and their tables, for a file to be
// applied to.
func readBed(t *testing.T) *built {
	t.Helper()
	cdrv, cdsn := pdtest.DB(t)
	b, ctx := build(t, func(c *cmd.Config) {
		c.Control = cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}}
	})
	require.NoError(t, entmigrate.NewSchema(b.Control.Drv).Create(ctx))

	return b
}

// TestAResourcesFileIsReadStrictly: a key nothing reads is refused, by its
// file, where it is in it and its name, before anything is written; as the
// configuration file is read.
//
// # What it was
//
// The file was read with a plain `yaml.Unmarshal` into one flat struct for
// all four kinds. A file whose top-level key was `resource:` applied nothing,
// printed nothing and exited 0, and `serve` started on it without a word. A
// key no kind reads was passed over: `clientId` for `client_id` (then refused
// as empty), `secretRef`, `provision`; and a key another kind reads, `routes`
// or `alias` on a `Host`, was taken and dropped. A `kind` this does not apply
// was refused only when its turn came, so a `Tenant` before a `Role` was made
// and then the run failed. And a refusal named an index across all the files
// (`resources: [1] Connection: …`), which is no line of any of them.
func TestAResourcesFileIsReadStrictly(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		says []string
	}{
		{
			"a top-level key nothing reads",
			"resource:\n  - kind: Tenant\n    alias: contoso\n",
			[]string{":1:1: ", "resource: ", "nothing reads this key"},
		},
		{
			"a key of a Connection misspelled",
			"resources:\n  - kind: Connection\n    tenant: contoso\n    name: entra\n    issuer: https://idp.example.test\n    clientId: abc\n",
			[]string{":6:5: ", "resources[0].clientId: ", "a Connection does not read this key", "client_id"},
		},
		{
			"secretRef for secret_ref",
			"resources:\n  - kind: Connection\n    tenant: contoso\n    name: entra\n    issuer: https://idp.example.test\n    client_id: abc\n    secretRef: env:ENTRA\n",
			[]string{":7:5: ", "resources[0].secretRef: "},
		},
		{
			"provision for provisions",
			"resources:\n  - kind: Connection\n    tenant: contoso\n    name: entra\n    issuer: https://idp.example.test\n    client_id: abc\n    provision: true\n",
			[]string{"resources[0].provision: "},
		},
		{
			"a key another kind reads",
			"resources:\n  - kind: Tenant\n    alias: contoso\n  - kind: Host\n    tenant: contoso\n    name: contoso.example\n    routes: entra\n",
			[]string{":7:5: ", "resources[1].routes: ", "a Host does not read this key"},
		},
		{
			"an alias on a Host",
			"resources:\n  - kind: Host\n    tenant: contoso\n    name: contoso.example\n    alias: zzz\n",
			[]string{"resources[0].alias: ", "a Host does not read this key"},
		},
		{
			"a key of a tenant's settings misspelled",
			"resources:\n  - kind: Tenant\n    alias: contoso\n    config:\n      passwrod: false\n",
			[]string{":5:7: ", "resources[0].config.passwrod: ", "password"},
		},
		{
			"a kind in small letters",
			"resources:\n  - kind: tenant\n    alias: contoso\n",
			[]string{":2:11: ", "resources[0].kind: ", `"tenant" is not a kind this applies`, "Tenant"},
		},
		{
			"an entry with no kind",
			"resources:\n  - alias: contoso\n",
			[]string{":2:5: ", "resources[0]: ", "kind"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)
			paths := declare(t, tc.body)

			_, err := cmd.ReadResources(paths)
			x.Error(err)
			x.ErrorContains(err, paths[0], "the refusal does not name the file")
			for _, s := range tc.says {
				x.ErrorContains(err, s)
			}
		})
	}

	t.Run("a kind this does not apply is refused before anything is written", func(t *testing.T) {
		x := require.New(t)
		b := readBed(t)
		ctx := t.Context()

		paths := declare(t, "resources:\n  - kind: Tenant\n    alias: acme\n  - kind: Role\n    tenant: acme\n    name: x\n")
		_, err := cmd.ReadResources(paths)
		x.ErrorContains(err, `"Role" is not a kind this applies`)

		// And from a caller that hands the rows in without a file.
		_, err = cmd.ApplyResources(ctx, b.Server, []cmd.Resource{
			{Kind: "Tenant", Alias: "acme"},
			{Kind: "Role", Tenant: "acme", Name: "x"},
		}, false)
		x.ErrorContains(err, `"Role" is not a kind this applies`)

		_, err = b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
			Ref:    app.TenantRef_builder{Alias: proto.String("acme")}.Build(),
			Select: app.TenantSelect_builder{}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err), "the tenant before it was made")
	})

	t.Run("a refusal names the file it is of", func(t *testing.T) {
		x := require.New(t)
		b := readBed(t)
		ctx := t.Context()

		one := declare(t, "resources:\n  - kind: Tenant\n    alias: contoso\n")
		two := declare(t, "resources:\n  - kind: Tenant\n    alias: fabrikam\n  - kind: Connection\n    tenant: fabrikam\n    name: entra\n    client_id: abc\n")
		rs, err := cmd.ReadResources(append(one, two...))
		x.NoError(err)

		_, err = cmd.ApplyResources(ctx, b.Server, rs, false)
		x.Error(err, "a connection with no issuer")
		x.ErrorContains(err, two[0]+":4:5: ")
		x.ErrorContains(err, "Connection @fabrikam/entra")
		x.NotContains(err.Error(), "[1]", "an index across all the files")
	})

	t.Run("what the manual shows is read", func(t *testing.T) {
		x := require.New(t)
		doc, err := os.ReadFile(filepath.Join("..", "docs", "operating.md"))
		x.NoError(err)
		_, block, ok := strings.Cut(string(doc), "```yaml\n# resources.yaml\n")
		x.True(ok, "docs/operating.md shows no resources file")
		block, _, _ = strings.Cut(block, "```")

		rs, err := cmd.ReadResources(declare(t, block))
		x.NoError(err)
		x.Len(rs, 4)
		x.Equal("oid", rs[1].SubjectClaim)
		x.True(*rs[0].Config.Profile.Fill)
	})

	t.Run("what deploy/ ships is read", func(t *testing.T) {
		x := require.New(t)
		rs, err := cmd.ReadResources([]string{filepath.Join("..", "deploy", "resources.yaml")})
		x.NoError(err)
		x.Len(rs, 2)
	})
}

// TestAFileThatDeclaresNothingIsSaid: a file named in `resources:` that
// declares no row is said, naming the file -- in the log `serve` starts with,
// at the level of information, and by `roster resources apply`.
//
// It was passed over in silence: `serve` logged its line of counts only where
// a file declared something, and `resources apply` printed nothing and exited
// 0. A file that declares nothing is not wrong -- one may be emptied on
// purpose -- but a file somebody expected to declare a customer and that does
// not is found out only when the customer is not there.
func TestAFileThatDeclaresNothingIsSaid(t *testing.T) {
	for _, body := range []string{"", "# nothing yet\n", "resources:\n", "resources: []\n"} {
		t.Run(strings.TrimSpace(body), func(t *testing.T) {
			x := require.New(t)
			b := readBed(t)

			paths := declare(t, body)
			rs, err := cmd.ReadResources(paths)
			x.NoError(err)

			said := &heard{}
			ctx := log.Into(t.Context(), slog.New(slog.NewJSONHandler(said, nil)))
			v, err := cmd.ApplyResources(ctx, b.Server, rs, false)
			x.NoError(err)
			x.Empty(v.Added)

			lines := said.lines(t, "declares nothing")
			x.Len(lines, 1, "serve said nothing of the file")
			x.Equal("INFO", lines[0]["level"])
			x.Equal(paths[0], lines[0]["file"])
		})
	}

	t.Run("by resources apply", func(t *testing.T) {
		x := require.New(t)
		c := seedbed(t)
		out, err := initRun(t, c)
		x.NoError(err, out)

		paths := declare(t, "resources: []\n")
		w := &strings.Builder{}
		root := cli.Cmd(&c)
		root.Writer = w
		said, err := stderrOf(t, root, "resources", "apply", "--file", paths[0])
		x.NoError(err)
		x.Contains(said, paths[0])
		x.Contains(said, "declares nothing")
	})
}
