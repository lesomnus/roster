package cmd_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/keys"
)

// TestBaselineNamesRealTests keeps docs/baseline.md honest the mechanical way.
//
// The file maps promises to the tests that pin them, and a mapping is only
// worth reading while every name in it resolves: a renamed test orphans its
// row silently, and an orphaned row is a promise that reads as pinned and is
// not -- the exact state the baseline exists to make impossible. So the doc is
// data, and this is the check, the same move as `pd gen --check`: what cannot
// be kept in step by discipline is kept in step by a diff.
//
// Only tests at the wire may be named, deliberately. The baseline is about
// what a caller experiences, and `cmd` is where the served stack is stood up
// whole -- and `account/` and `frontdoor/` are where it is stood up whole and
// then reached from outside, as the front door a customer's people arrive at,
// which is a caller's experience too, as `standing/` is the stream an app
// follows. A promise that seems to need a deeper test's name is a promise that
// should be re-stated at the wire.
// testsThatExist is every test declared where a page may name one.
func testsThatExist(t *testing.T) map[string]bool {
	t.Helper()
	x := require.New(t)

	have := map[string]bool{}
	var files []string
	for _, dir := range []string{".", filepath.Join("..", "account"), filepath.Join("..", "arrives"), filepath.Join("..", "examples", "product"), filepath.Join("..", "frontdoor"), filepath.Join("..", "wasm", "sandbox"), filepath.Join("..", "ldap"), filepath.Join("..", "ldap", "wire"), filepath.Join("..", "login"), filepath.Join("..", "scim"), filepath.Join("..", "cli"), filepath.Join("..", "standing"), filepath.Join("..", "server", "prove"), filepath.Join("..", "server", "portrait"), filepath.Join("..", "server", "core"), filepath.Join("..", "server", "vouch")} {
		vs, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		x.NoError(err)
		files = append(files, vs...)
	}

	decl := regexp.MustCompile(`(?m)^func (Test\w+)`)
	for _, f := range files {
		src, err := os.ReadFile(f)
		x.NoError(err)

		for _, m := range decl.FindAllStringSubmatch(string(src), -1) {
			have[m[1]] = true
		}
	}

	return have
}

func TestBaselineNamesRealTests(t *testing.T) {
	x := require.New(t)

	doc, err := os.ReadFile(filepath.Join("..", "docs", "baseline.md"))
	x.NoError(err)

	have := testsThatExist(t)

	named := regexp.MustCompile("`(Test\\w+)`").FindAllStringSubmatch(string(doc), -1)
	x.NotEmpty(named, "the baseline names no tests at all, which cannot be right")

	for _, m := range named {
		x.True(have[m[1]], "docs/baseline.md names %s, and no such test exists", m[1])
	}
}

// TestTheRulesPageNamesRealTests is the same check of `docs/usage/rules.md`,
// which lists every rule a call is held to beside the test that holds it. A
// rule whose test was renamed away reads as held and is not, and that page is
// what a deployment, a page and the next change are checked against.
func TestTheRulesPageNamesRealTests(t *testing.T) {
	x := require.New(t)

	doc, err := os.ReadFile(filepath.Join("..", "docs", "usage", "rules.md"))
	x.NoError(err)

	have := testsThatExist(t)

	named := regexp.MustCompile("`(Test\\w+)`").FindAllStringSubmatch(string(doc), -1)
	x.NotEmpty(named, "the page names no tests at all, which cannot be right")

	for _, m := range named {
		x.True(have[m[1]], "docs/usage/rules.md names %s, and no such test exists", m[1])
	}
}

// TestTheWideGrantsPageSaysWhatAKeyIsWarnedOf holds `docs/usage/wide-grants.md`
// to the one place roster already says a grant is wider than it reads:
// `Widest`, which `roster key add` prints. Every method it has a note for is
// on the page, so that a note added there is a grant somebody reading the page
// before binding a role is told about too -- and the tests the page names are
// there.
func TestTheWideGrantsPageSaysWhatAKeyIsWarnedOf(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	doc, err := os.ReadFile(filepath.Join("..", "docs", "usage", "wide-grants.md"))
	x.NoError(err)

	warned := 0
	for svc, info := range b.grpc(t).GetServiceInfo() {
		for _, m := range info.Methods {
			method := "/" + svc + "/" + m.Name
			// Of either kind of key: the page is read before a role is bound,
			// and a role is a customer's.
			if cmd.Widest(keys.PrefixDeployment, []string{method}) == "" &&
				cmd.Widest(keys.PrefixTenant, []string{method}) == "" {
				continue
			}
			warned++

			// By the service, which is how the page names the trail's two
			// reads at once, or by the method.
			short := strings.TrimPrefix(svc, "roster.")
			x.True(strings.Contains(string(doc), short+"/"+m.Name) || strings.Contains(string(doc), "`"+short+"`"),
				"`roster key add` says %s is wider than it reads, and docs/usage/wide-grants.md does not", method)
		}
	}
	x.NotZero(warned, "nothing is warned of, so the loop above proved nothing")

	have := testsThatExist(t)
	for _, m := range regexp.MustCompile("`(Test\\w+)`").FindAllStringSubmatch(string(doc), -1) {
		x.True(have[m[1]], "docs/usage/wide-grants.md names %s, and no such test exists", m[1])
	}
}

// TestTheWideGrantsPageIsWhatTheWireAnswers holds `RoleService/HandsOver` and
// `docs/usage/wide-grants.md` to each other, in both directions.
//
// The method exists so that a page says what the document says, and that is a
// promise about two lists: a grant the wire answers is a heading somebody can
// read the example under, and a heading there is one a page is told of. One is
// not about a method -- a deployment key, which is a kind of credential -- and
// is named here, so that a second is a decision and not an omission.
//
// And what a terminal says of a key (`cmd.Widest`) is among what the wire
// says, for that kind: a note with no entry is a grant the page was not told.
func TestTheWideGrantsPageIsWhatTheWireAnswers(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	doc, err := os.ReadFile(filepath.Join("..", "docs", "usage", "wide-grants.md"))
	x.NoError(err)

	// A heading's anchor, as the page's own table of them links it: lower
	// case, what is not a letter, a digit, a space or a hyphen dropped, spaces
	// to hyphens.
	drop := regexp.MustCompile(`[^a-z0-9 -]`)
	headings := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^### (.+)$`).FindAllStringSubmatch(string(doc), -1) {
		anchor := strings.ReplaceAll(drop.ReplaceAllString(strings.ToLower(m[1]), ""), " ", "-")
		headings[anchor] = true
		x.Contains(string(doc), "(#"+anchor+")", "the page's table does not link %q as this reads its anchor", m[1])
	}
	x.NotEmpty(headings)

	// Everything asked of every plane and place: each method there is, and
	// the pattern over all of them.
	asked := []string{"/roster.*/*"}
	for svc, info := range b.grpc(t).GetServiceInfo() {
		for _, m := range info.Methods {
			asked = append(asked, "/"+svc+"/"+m.Name)
		}
	}

	answered := map[string]bool{}
	for _, kind := range []string{keys.PrefixTenant, keys.PrefixDeployment} {
		for _, held := range []app.RoleHeld{
			app.RoleHeld_ROLE_HELD_ACROSS_THE_TENANT,
			app.RoleHeld_ROLE_HELD_IN_A_SITE,
			app.RoleHeld_ROLE_HELD_NOWHERE,
		} {
			for _, g := range core.HandedOver(kind, held, asked) {
				answered[g.GetGrant()] = true
				x.True(headings[g.GetGrant()],
					"the wire answers %q, and docs/usage/wide-grants.md has no such heading", g.GetGrant())
			}
		}
	}

	// And the verbs a heading that names no method is about: asked of any of
	// them, across the tenant, a page is told of it. *A directory's key* is
	// whoever holds a directory's verbs, as the page says, and *a role bound
	// to a group* is handed out by whoever puts somebody in the group. They
	// were named below as about no method, and asked of their verbs answered
	// no grant.
	for anchor, methods := range map[string][]string{
		"a-directorys-key": {
			app.HolderService_Provision_FullMethodName,
			app.HolderService_Deactivate_FullMethodName,
			app.HolderService_Tell_FullMethodName,
		},
		"a-role-bound-to-a-group": {app.GroupMembershipService_Add_FullMethodName},
		"syncservicewatch":        {app.SyncService_Watch_FullMethodName},
	} {
		for _, m := range methods {
			told := false
			for _, g := range core.HandedOver(keys.PrefixTenant, app.RoleHeld_ROLE_HELD_ACROSS_THE_TENANT, []string{m}) {
				told = told || g.GetGrant() == anchor
			}
			x.True(told, "a page asking about %s is not told of %q", m, anchor)
		}
	}

	notAMethod := []string{"a-deployment-key"}
	for _, anchor := range notAMethod {
		x.True(headings[anchor], "%q is named here as a heading of the page, and is not one", anchor)
		x.False(answered[anchor], "%q is answered now, and is still named here as what is not", anchor)
	}
	for anchor := range headings {
		x.True(answered[anchor] || slices.Contains(notAMethod, anchor),
			"docs/usage/wide-grants.md has %q, and no list of methods is told of it", anchor)
	}

	// And the terminal's notes.
	for _, kind := range []string{keys.PrefixTenant, keys.PrefixDeployment} {
		for _, method := range asked {
			if cmd.Widest(kind, []string{method}) == "" {
				continue
			}

			x.NotEmpty(core.HandedOver(kind, app.RoleHeld_ROLE_HELD_ACROSS_THE_TENANT, []string{method}),
				"a key naming %s is warned of at a terminal, and a page asking about it is told nothing", method)
		}
	}
}
