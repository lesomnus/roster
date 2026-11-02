package cli_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
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
		"holdingOf":      "counts what an app's holder already holds, for a refusal; prints counts and role names",

		// `cmd`'s, as the commands call them.
		"cmd.ListKeys":      shown,
		"cmd.CustomerOf":    "resolves @tenant/alias to an identifier for `key add`; prints nothing of the row",
		"cmd.ControlHolder": "resolves an operator's alias to an identifier; prints nothing of the row",
		"cmd.OperatorNamed": "resolves who --as names, which the person typed; the row's alias is what they typed",
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
