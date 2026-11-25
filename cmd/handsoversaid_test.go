package cmd_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// handsOverSaidRun is one invocation at alice's shell over the wire, and the
// two streams apart: the answer, and what is said beside it. The process's
// standard error is swapped, since a note is written there, so no test that
// calls this runs beside another.
func handsOverSaidRun(t *testing.T, c *cmd.Config, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	x := require.New(t)

	er, ew, e := os.Pipe()
	x.NoError(e)
	was := os.Stderr
	os.Stderr = ew
	defer func() { os.Stderr = was }()

	errc := make(chan string, 1)
	go func() { b, _ := io.ReadAll(er); errc <- string(b) }()

	out := &bytes.Buffer{}
	k := root(t, c)
	k.Writer = out
	err = k.Run(t.Context(), args)

	ew.Close()

	return out.String(), <-errc, err
}

// TestATerminalSaysWhatARoleOrABindingHandsOver: `roster role add`, `role
// widen` and `binding add` say on standard error, after the row they print,
// what the grant they made hands over that it does not read as -- asked of
// `RoleService/HandsOver` on the same connection, where the grant would be
// held.
//
// # What it was
//
// `roster key add` printed a note when a key's methods reached the trail, a
// credential for anybody, or a link to anybody's account. Most grants of that
// kind are not made on a key: they are made where a role is written or bound,
// by the commands the tree builds from the schema, and those printed the row
// and nothing else. A role naming `ApiKeyService/Issue` bound to two
// colleagues so that each mints their own keys is each a way into the other's
// account, and the terminal that bound it said nothing (#310).
//
// It says and does not refuse: the write was made, the exit status is the
// write's, and a caller who may not ask is told that once.
func TestATerminalSaysWhatARoleOrABindingHandsOver(t *testing.T) {
	const (
		roleAdd   = "/roster.RoleService/Add"
		roleWiden = "/roster.RoleService/Widen"
		roleGet   = "/roster.RoleService/Get"
		handsOver = "/roster.RoleService/HandsOver"
		bindAdd   = "/roster.BindingService/Add"
		issue     = "/roster.ApiKeyService/Issue"
		disable   = "/roster.HolderService/Disable"
		holderGet = "/roster.HolderService/Get"
	)

	b := cliUp(t, roleAdd, roleWiden, roleGet, handsOver, bindAdd, issue, disable, holderGet)
	ctx := t.Context()
	at := app.TenantRef_builder{Id: b.Tenant.GetId()}.Build()

	t.Run("a role with a method on the page says it, after the row", func(t *testing.T) {
		x := require.New(t)

		out, said, err := handsOverSaidRun(t, &b.Hers, "role", "add", "@newco/minting", `{"methods":["`+issue+`"]}`)
		x.NoError(err)
		x.Contains(out, "minting", "the row, as it was printed")
		x.NotContains(out, "wide-grants", "a note on standard output")

		x.True(strings.HasPrefix(said, "\n"), "a blank line before it: %q", said)
		x.Contains(said, "`"+issue+"` reads as \"may manage somebody's keys, password or addresses\"")
		x.Contains(said, "anybody of the tenant who holds no more")
		x.Contains(said, "docs/usage/wide-grants.md#a-persons-verb-across-the-tenant")
		x.Contains(said, "docs/usage/wide-grants.md#a-key-that-mints-keys")
	})

	t.Run("one with nothing on the page says nothing more", func(t *testing.T) {
		x := require.New(t)

		out, said, err := handsOverSaidRun(t, &b.Hers, "role", "add", "@newco/reader", `{"methods":["`+holderGet+`"]}`)
		x.NoError(err)
		x.Contains(out, "reader")
		x.Empty(said)
	})

	t.Run("a widen says what was added and not what was there", func(t *testing.T) {
		x := require.New(t)

		_, said, err := handsOverSaidRun(t, &b.Hers, "role", "widen", "@newco/minting", `{"methods":["`+disable+`"]}`)
		x.NoError(err)
		x.Contains(said, "docs/usage/wide-grants.md#holderservicedisable")
		x.NotContains(said, "a-persons-verb-across-the-tenant", "said of a method that was already there")
		x.NotContains(said, issue)
	})

	t.Run("a binding says what is handed over where it was made", func(t *testing.T) {
		x := require.New(t)

		seoul, err := b.Server.Ungated.Site().Add(ctx, app.SiteAddRequest_builder{Tenant: at, Alias: "seoul"}.Build())
		x.NoError(err)

		bound := `{"role":{"slug":{"alias":"minting","tenant":{"alias":"newco"}}},` +
			`"holder":{"slug":{"alias":"bob","tenant":{"alias":"newco"}}}`

		out, said, err := handsOverSaidRun(t, &b.Hers, "binding", "add", bound+`}`)
		x.NoError(err)
		x.NotEmpty(out)
		x.Contains(said, "docs/usage/wide-grants.md#a-persons-verb-across-the-tenant")
		x.Contains(said, "anybody of the tenant who holds no more")
		x.Contains(said, "docs/usage/wide-grants.md#holderservicedisable", "the role's methods as it is now")

		_, said, err = handsOverSaidRun(t, &b.Hers, "binding", "add",
			bound+`,"site":{"id":"`+base64.StdEncoding.EncodeToString(seoul.GetId())+`"}}`)
		x.NoError(err)
		x.Contains(said, "anybody of that site who holds no more")
		x.NotContains(said, "anybody of the tenant")
		x.NotContains(said, "a-key-that-mints-keys", "what is said of the tenant alone, in a site")
	})

	t.Run("an app's role and its binding say nothing", func(t *testing.T) {
		x := require.New(t)

		loft := appIn(t, b.Server, b.Tenant.GetId(), "loft")
		heldAtApp(t, b.Server, b.Tenant.GetId(), loft, b.Alice.GetId(), "operator-at-loft", "/loft.*/*")

		// A pattern, which the page answers of any role of roster's: here it
		// is Loft's methods, which are not on the page.
		out, said, err := handsOverSaidRun(t, &b.Hers, "role", "add", "@newco/loft-editor",
			`{"methods":["/loft.AssetService/*"],"app":{"id":"`+base64.StdEncoding.EncodeToString(loft)+`"}}`)
		x.NoError(err)
		x.Contains(out, "loft-editor")
		x.Empty(said)

		_, said, err = handsOverSaidRun(t, &b.Hers, "role", "widen", "@newco/loft-editor", `{"methods":["/loft.ReportService/*"]}`)
		x.NoError(err)
		x.Empty(said)

		_, said, err = handsOverSaidRun(t, &b.Hers, "binding", "add",
			`{"role":{"slug":{"alias":"loft-editor","tenant":{"alias":"newco"}}},`+
				`"holder":{"slug":{"alias":"bob","tenant":{"alias":"newco"}}}}`)
		x.NoError(err)
		x.Empty(said)
	})

	t.Run("the answer as json is the row alone", func(t *testing.T) {
		x := require.New(t)

		out, said, err := handsOverSaidRun(t, &b.Hers, "role", "add", "-o", "json", "@newco/stopper", `{"methods":["`+disable+`"]}`)
		x.NoError(err)
		x.Contains(said, "holderservicedisable")

		v := map[string]any{}
		x.NoError(json.Unmarshal([]byte(out), &v), "%q", out)
		x.Equal("stopper", v["alias"])
	})
}

// TestATerminalThatMayNotAskSaysSoAndTheWriteStands: a caller who may write a
// role but not ask `RoleService/HandsOver` is told, in one line, that what the
// write hands over could not be asked, and the command exits as the write did.
// The write was made: a note that could not be had is not the write failing,
// and a write reported as failed is one somebody makes twice.
func TestATerminalThatMayNotAskSaysSoAndTheWriteStands(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, "/roster.RoleService/Add", "/roster.ApiKeyService/Issue")

	out, said, err := handsOverSaidRun(t, &b.Hers, "role", "add", "@newco/minting", `{"methods":["/roster.ApiKeyService/Issue"]}`)
	x.NoError(err, "the exit status is the write's")
	x.Contains(out, "minting")

	lines := strings.Split(strings.TrimSpace(said), "\n")
	x.Len(lines, 1, "%q", said)
	x.Contains(lines[0], "could not be asked")
	x.Contains(lines[0], "PermissionDenied")
	x.Contains(lines[0], "docs/usage/wide-grants.md")
}

// TestAtTheBoxARoleSaysWhatItHandsOverOnEitherPlane: the same note at the box,
// on the pipe the command was answered on -- of a customer's tenant on the
// data plane's tree, and of every tenant on `roster control`'s, where an
// operator's role is exercised.
func TestAtTheBoxARoleSaysWhatItHandsOverOnEitherPlane(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	out, said, err := boxRun(t, at(b.c, "admin"), "", "role", "add", "@newco/auditor", `{"methods":["/roster.AuditService/List"]}`)
	x.NoError(err)
	x.Contains(out, "auditor")
	x.Contains(said, "docs/usage/wide-grants.md#auditservice")
	x.Contains(said, "every write in the tenant")

	_, said, err = boxRun(t, at(b.c, "admin"), "", "control", "role", "add", "@owner/auditor", `{"methods":["/roster.AuditService/List"]}`)
	x.NoError(err)
	x.Contains(said, "docs/usage/wide-grants.md#auditservice")
	x.Contains(said, "every write in every tenant")
}
