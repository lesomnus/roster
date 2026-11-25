package cli_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/roster/internal/ent"
	_ "github.com/lesomnus/roster/rstr"
)

// What holds every read made at the box to the access record (#221), the way
// `TestEveryCommandBuildsWhatItWritesQueued` holds every write to the outbox.
//
// A command reaches people's rows two ways: the entity tree, which is RPCs on
// a pipe, and Go -- an ent query, or a server called as a function -- in the
// command's own body. The pipe is held by the first test: every server a
// command stands up is the one `pipe` builds, which records. The second way is
// held by the second: every place a command reads a kind that holds anybody's
// personal data says what it does with the row, and one that shows it must do
// so in a recorded call.

// files is this package's own source, parsed: what a command is made of.
func files(t *testing.T, dir string) map[string]*ast.File {
	t.Helper()
	x := require.New(t)

	fs := token.NewFileSet()
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	x.NoError(err)

	out := map[string]*ast.File{}
	for _, f := range names {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		v, err := parser.ParseFile(fs, f, nil, 0)
		x.NoError(err)
		out[filepath.Base(f)] = v
	}

	return out
}

// TestEveryPipeIsRecorded: the one gRPC server a command stands up is `pipe`'s,
// and `pipe` puts the access record in front of it as a call at the shell. A
// second one built anywhere else would be the pipe before #221 -- a read of
// anybody's rows that nothing heard of.
func TestEveryPipeIsRecorded(t *testing.T) {
	x := require.New(t)

	servers := map[string]int{}
	for name, f := range files(t, ".") {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "grpc" && sel.Sel.Name == "NewServer" {
					servers[name+":"+fn.Name.Name]++
				}

				return true
			})
		}
	}
	x.Equal(map[string]int{"entity.go:pipe": 1}, servers,
		"a command stands its server up with `pipe`, which records what reaches it as a call at the shell")

	// And `pipe` is the recorded one: the record's interceptor, as a shell's,
	// is what the server is built with.
	f := files(t, ".")["entity.go"]
	var body *ast.BlockStmt
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "pipe" {
			body = fn.Body
		}
	}
	x.NotNil(body)

	calls := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			calls[sel.Sel.Name] = true
		}
		return true
	})
	x.True(calls["Shell"] && calls["Interceptor"], "pipe builds its server with the access record's interceptor, as a shell's")
}

// personal is the entities whose rows hold a field declared `personal`, read
// off the schema rather than written here: what a command reading one is
// reading somebody's data of.
func personal(t *testing.T) []string {
	t.Helper()

	named := map[string]bool{}
	protoregistry.GlobalFiles.RangeFilesByPackage("roster", func(fd protoreflect.FileDescriptor) bool {
		ms := fd.Messages()
		for i := range ms.Len() {
			m := ms.Get(i)
			fs := m.Fields()
			for j := range fs.Len() {
				v, _ := proto.GetExtension(fs.Get(j).Options(), pdpb.E_Field).(*pdpb.Field)
				if v.GetPersonal() {
					named[string(m.Name())] = true
				}
			}
		}
		return true
	})

	// Entities only: what ent has a client for. A message beside an entity
	// that holds one of its personal fields is reached through the entity.
	out := []string{}
	c := reflect.TypeOf(ent.Client{})
	for v := range named {
		if _, ok := c.FieldByName(v); ok {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	require.NotEmpty(t, out)

	return out
}

// reads is where the functions of `dir` read one of `kinds`, by function: an
// ent query (`.Holder.Query`, an edge loaded with `WithHolder`) or a server
// called as Go (`.Holder().Get`).
func reads(t *testing.T, dir string, kinds []string) map[string]bool {
	t.Helper()

	is := func(name string) bool {
		for _, k := range kinds {
			if name == k {
				return true
			}
		}
		return false
	}
	// An edge's loader or query: `WithHolder`, `QueryEmails`, `WithIdentities`.
	edge := func(name string) bool {
		for _, p := range []string{"With", "Query"} {
			rest, ok := strings.CutPrefix(name, p)
			if !ok {
				continue
			}
			for _, k := range kinds {
				if strings.HasPrefix(rest, k) || (strings.HasSuffix(k, "y") && strings.HasPrefix(rest, strings.TrimSuffix(k, "y")+"ies")) {
					return true
				}
			}
		}
		return false
	}

	out := map[string]bool{}
	for _, f := range files(t, dir) {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch x := sel.X.(type) {
				case *ast.SelectorExpr:
					// `db.Holder.Query()`, `db.Email.Get(...)`
					if is(x.Sel.Name) && (sel.Sel.Name == "Query" || strings.HasPrefix(sel.Sel.Name, "Get")) {
						out[fn.Name.Name] = true
					}
				case *ast.CallExpr:
					// `s.Ungated.Holder().Get(...)`
					if f, ok := x.Fun.(*ast.SelectorExpr); ok && is(f.Sel.Name) && len(x.Args) == 0 {
						out[fn.Name.Name] = true
					}
				}
				if edge(sel.Sel.Name) {
					out[fn.Name.Name] = true
				}

				return true
			})
		}
	}

	return out
}

// called is the functions of package `pkg` the files of `dir` call: what a
// command reaches through a helper of `cmd`'s.
func called(t *testing.T, dir string, pkg string) map[string]bool {
	t.Helper()

	out := map[string]bool{}
	for _, f := range files(t, dir) {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if p, ok := sel.X.(*ast.Ident); ok && p.Name == pkg {
					out[sel.Sel.Name] = true
				}
			}
			return true
		})
	}

	return out
}

// shows is whether `fn` in `dir` says what it shows in a recorded call: it calls
// `AtTheShell`, and `access.Saw` inside it.
func shows(t *testing.T, dir string, fn string) bool {
	t.Helper()

	saw, recorded := false, false
	for _, f := range files(t, dir) {
		for _, d := range f.Decls {
			v, ok := d.(*ast.FuncDecl)
			if !ok || v.Name.Name != fn || v.Body == nil {
				continue
			}
			ast.Inspect(v.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if p, ok := sel.X.(*ast.Ident); ok && p.Name == "access" && sel.Sel.Name == "Saw" {
					saw = true
				}
				if sel.Sel.Name == "AtTheShell" {
					recorded = true
				}
				return true
			})
		}
	}

	return saw && recorded
}

// TestEveryReadOfSomebodyAtTheBoxIsRecordedOrSaysWhy: each place a command
// reads a kind that holds personal data -- in this package, or in a helper of
// `cmd`'s it calls -- is named below with what it does with the row. One that
// shows it is checked to say so in a recorded call ([cmd.Server.AtTheShell]
// and `access.Saw`); every other says why nobody sees the row.
//
// A place that is new, or gone, fails this until somebody says which it is:
// a read that prints somebody's name and is not here is the read at the box
// that #221 found nothing recorded.
//
// One level of `cmd` and not every function behind it, which would be the
// whole server: the helpers a command calls are where what it prints comes
// from.
func TestEveryReadOfSomebodyAtTheBoxIsRecordedOrSaysWhy(t *testing.T) {
	x := require.New(t)

	const shown = "shown: said with access.Saw in a call recorded at the shell"

	want := map[string]string{
		// This package.
		"whoIs":          "resolves the REF somebody typed to an identifier, including the erased; prints the identifier and nothing of the row",
		"tenantOrHolder": "tells a tenant's alias from a person's, to an identifier; prints nothing of the row",
		"whom":           "resolves the REF a vouch command names, and the operator's alias to hand to Credential.Issue; prints nothing of the row",
		"holderNamed":    "the holder a front door is answered as, made or found by the alias the command chose; prints nothing of the row",
		"labelled":       "reads a holder's labels to add the ones a provisioner or an install writes, for holderNamed and installApp; prints nothing of the row",
		"holderFound":    "whether a tenant has a holder of the alias a provisioner chose, before it decides whose they are; reads an id and prints nothing of the row",
		"holdingOf":      "counts what an app's holder already holds, for a refusal; prints counts and role names",
		"appsOwn":        "reads the label that says whether a holder of an app's name is that app's own, before install decides whose it is; prints nothing of the row",
		"somebodys":      "reads the labels of a holder of the alias a provisioner chose, to tell its own from the tenant's; prints the alias and the provisioner's own label",
		"adoptScim":      shown,
		"matched":        "gathers who a directory matched and roster has not linked, for adoptScim, which says whose it shows; prints nothing itself",

		// The kinds that hold what somebody wrote about a person or about
		// what is theirs (#280): a key's alias and description, a factor's
		// name, the labels of a binding, a relation and a nomination, and a
		// nomination's name. Each of these writes one, or reads one for an
		// identifier, and shows none of those fields.
		"boundToOthers":       "whether a role is bound to anybody but one holder, by identifier; prints nothing",
		"ensureBinding":       "finds or makes the binding a provisioner declares, with the labels the command chose; prints nothing of the row",
		"ensureBindingFor":    "finds or makes the binding of roster's for an app that install writes, by identifier; prints nothing of the row",
		"ensureNominated":     "finds or makes the nomination a front door declares, with the labels the command chose; prints nothing of the row",
		"nominationOf":        "the nomination of an app in a tenant, for install and uninstall, which read who it acts as by identifier; prints nothing itself",
		"installApp":          "makes an app's holder, role, binding and nomination under the name the operator typed, and says which keys borrow through it by identifier; prints nothing of a row",
		"newCmdAppUninstall":  "erases the nomination found by the app's name, which the operator typed; prints that name and nothing of the row",
		"eraseKeyNamed":       "finds a key by the alias the command chose, for its identifier, and erases it; prints nothing",
		"mintNamed":           "mints the key a provisioner writes to a file, under the alias the command chose; prints nothing of the row",
		"mint":                "mints a key and prints its token and its identifier, for whoever the operator typed: a secret made for the occasion, and nothing of the row",
		"newCmdKeyRevoke":     "erases the key whose identifier the operator typed; prints nothing",
		"newCmdOperatorEnrol": "enrols a second factor and prints its seed, once: a secret made for the occasion, under the name the operator typed",
		"newCmdVouchSet":      "sets a password read from the terminal; prints nothing of the row",
		"newCmdVouchReset":    "issues a one-time password and prints it, once: a secret made for the occasion",
		"newCmdVouchUnlock":   "ends a lockout and prints until when it was; nothing else of the row",
		"restoredOne":         "the tenant of somebody brought back, which the write's row is filed under; prints nothing of the row",

		// `cmd`'s, as the commands call them.
		"cmd.ListKeys":      shown,
		"cmd.CustomerOf":    "resolves @tenant/alias to an identifier for `key add`; prints nothing of the row",
		"cmd.ControlHolder": "resolves an operator's alias to an identifier; prints nothing of the row",
		"cmd.OperatorNamed": "resolves who --as names, which the person typed; the row's alias is what they typed",
		"cmd.KeyPlane":      "counts the keys of the identifier the operator typed on each plane, to find which holds it; prints nothing of the row",
		"cmd.Seed":          "binds the role `init` makes to the operator it makes; prints nothing of the row",
	}

	kinds := personal(t)
	x.Contains(kinds, "Holder")

	got := map[string]string{}
	for fn := range reads(t, ".", kinds) {
		got[fn] = want[fn]
	}

	ours := reads(t, "../cmd", kinds)
	for fn := range called(t, ".", "cmd") {
		if ours[fn] {
			got["cmd."+fn] = want["cmd."+fn]
		}
	}

	for fn, why := range got {
		x.NotEmpty(why, "%s reads a kind that holds personal data (%v): say what it does with the row, here", fn, kinds)
		if why != shown {
			continue
		}

		dir, name := ".", fn
		if v, ok := strings.CutPrefix(fn, "cmd."); ok {
			dir, name = "../cmd", v
		}
		x.True(shows(t, dir, name), "%s shows what it reads, and does not say so in a call recorded at the shell", fn)
	}
	x.Equal(want, got, "every read of somebody's rows at the box is named here, with what it does with them")
}

// calls is whether the function `fn` of this package calls one of `names`,
// as a function or as a method.
func calls(t *testing.T, fn string, names ...string) bool {
	t.Helper()

	found := false
	for _, f := range files(t, ".") {
		for _, d := range f.Decls {
			v, ok := d.(*ast.FuncDecl)
			if !ok || v.Name.Name != fn || v.Body == nil {
				continue
			}
			ast.Inspect(v.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch f := call.Fun.(type) {
				case *ast.Ident:
					name = f.Name
				case *ast.SelectorExpr:
					name = f.Sel.Name
				}
				for _, v := range names {
					found = found || name == v
				}

				return true
			})
		}
	}

	return found
}

// TestEveryCommandAtTheBoxSaysWhetherItKeepsWhoIsNamed: each function of this
// package that opens the deployment is named below, with whether it keeps who
// `--as` names. One that writes somebody's way in or their standing keeps
// them, and is checked to: its write is a call at the shell by them
// (`keptBy`), a row of the access record as the same write through the entity
// tree is. Every other says why it keeps nobody; each still looks the name up,
// in `built`, so that a name nobody has is refused wherever it is given.
//
// The test before this asked a sentence of what a command reads of anybody,
// and took any sentence for a command that showed no row: so a write of
// somebody's way in was held to nothing, and `key add`, `vouch reset` and
// `restore` of a person were each made by nobody on any record, where `--as
// … api-key add` was a row (#284).
func TestEveryCommandAtTheBoxSaysWhetherItKeepsWhoIsNamed(t *testing.T) {
	x := require.New(t)

	const kept = "kept: a write of somebody's way in or their standing, made at the shell by whoever --as names"

	want := map[string]string{
		// What writes somebody's way in or their standing.
		"newCmdKeyAdd":        kept,
		"newCmdKeyRevoke":     kept,
		"newCmdControlKeyAdd": kept,
		"newCmdVouchReset":    kept,
		"newCmdVouchSet":      kept,
		"newCmdVouchUnlock":   kept,
		"NewCmdRestore":       kept,
		"newCmdOperatorEnrol": kept,
		"newCmdOperatorProve": kept,

		// The entity tree, and the commands that show a read.
		"Connect":         "the entity tree's pipe, on either plane: every call on it is recorded as the operator's (pipe)",
		"newCmdKeyList":   "shows whose each key is, in a call recorded as the operator's (ListKeys)",
		"newCmdScimAdopt": "shows who a directory matched, and links them, each in a call recorded as the operator's (adoptScim)",
		"newCmdTrailRows": "keeps who accepts the rows it found, as the operator and the shell, in the trail and the log",

		// What writes nobody's way in. Each looks the name up, in `built`.
		"newCmdTrailAccept":      "keeps who accepts the archive's findings, as the operator and the shell where --as names one, in the receipt",
		"NewCmdInit":             "writes the first operator's way in, which no operator before them could have written; it looks the name up once the tables are there, and keeps nothing",
		"NewCmdHold":             "a hold is on a tenant, which is nobody's row; it is on the trail by the deployment",
		"onHeld":                 "a hold placed or lifted is on a tenant, which is nobody's row",
		"NewCmdForget":           "destroys what is held about somebody, past every server: a record of what was destroyed is what is being destroyed",
		"newCmdAppInstall":       "writes an app's holder, role and nomination, which are the deployment's; a person's way in is not among them",
		"newCmdAppUninstall":     "erases an app's nomination, which is the deployment's",
		"newCmdResourcesApply":   "writes what a file declares, by the provisioner, which is who the trail names",
		"newCmdScimProvision":    "writes the deployment's own front door and its key, by the provisioner",
		"newCmdLoginProvision":   "writes the deployment's own front door and its key, by the provisioner",
		"newCmdAccountProvision": "writes the deployment's own front door and its key, by the provisioner",
		"newCmdLoginDoctor":      "reads the front door's rows to say what is wrong with them; writes nothing",
		"newCmdTrailPrune":       "moves the trail's rows to the archive by the policy; writes nobody's way in",
		"newCmdTrailPurge":       "removes archived chunks by the policy; writes nobody's way in",
		"newCmdTrailVerify":      "checks the archive against its account; writes nothing",
		"newCmdTrailOwed":        "says what the trail is owed of erasures; writes nothing",
		"prune":                  "the trail's pass, for trail prune; writes nobody's way in",
	}

	// What opens the deployment: `built`, and what calls it for a command.
	openers := []string{"built", "controlled", "vouching", "archived"}
	got := map[string]string{}
	for _, f := range files(t, ".") {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || slices.Contains(openers, fn.Name.Name) {
				continue
			}
			if calls(t, fn.Name.Name, openers...) {
				got[fn.Name.Name] = want[fn.Name.Name]
			}
		}
	}

	for fn, why := range got {
		x.NotEmpty(why, "%s opens the deployment: say here whether it keeps who --as names, and why", fn)
		if why == kept {
			x.True(calls(t, fn, "keptBy", "kept", "mint"), "%s writes somebody's way in, and not as a call at the shell by whoever --as names", fn)
		}
	}
	x.True(calls(t, "mint", "keptBy"), "mint writes a key, and not as a call at the shell by whoever --as names")
	x.True(calls(t, "kept", "keptBy"))
	x.Equal(want, got, "every command that opens the deployment is named here, with whether it keeps who --as names")
}
