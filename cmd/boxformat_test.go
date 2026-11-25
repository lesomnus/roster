package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
)

// TestAWrongFormatIsRefusedBeforeTheWrite: an entity command given an `-o` or
// an `--in` that is none of theirs is refused before anything is opened, as a
// wrong flag, so a write is not made and then reported as a failure.
//
// Every generated verb made the call and only then resolved the printer:
// `roster --as admin holder add -o yaml @acme/f1` answered `-o yaml: not a
// format` with status 1, and the person was added; `erase` the same, and the
// person was erased. Answered: a failure, after the write; right is the
// refusal, before it, with status 2. payday refuses each before its connector
// is asked now (#531), a template that does not parse naming the flag as it is
// typed, and this holds roster's tree to it.
func TestAWrongFormatIsRefusedBeforeTheWrite(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	b := atTheBox(t)

	_, _, err := boxRun(t, at(b.c, "admin"), "", "holder", "add", "@newco/kept")
	x.NoError(err)

	before := boxWritten(t, b.c)
	for _, v := range []struct {
		args []string
		says string
	}{
		{[]string{"holder", "add", "-o", "yaml", "@newco/f1"}, "-o yaml: not a format"},
		{[]string{"holder", "erase", "-o", "yaml", "@newco/kept"}, "-o yaml: not a format"},
		{[]string{"holder", "add", "-o", "template={{.alias", "@newco/f1"}, "-o template={{.alias: line 1: unclosed action"},
		{[]string{"holder", "add", "--in", "yaml", "@newco/f1", `{"name":"F"}`}, "--in yaml: not an input format"},
		{[]string{"control", "holder", "add", "-o", "yaml", "@owner/f1"}, "-o yaml: not a format"},
		{[]string{"tenant", "close", "-o", "yaml", "@newco"}, "-o yaml: not a format"},
	} {
		out, said, err := boxRun(t, at(b.c, "admin"), "", v.args...)
		x.ErrorContains(err, v.says, "%v", v.args)
		x.Equal(2, cli.Status(err), "%v: the status a wrong flag has", v.args)
		x.Empty(out, "%v", v.args)
		x.NotContains(said, "directly", "%v opened the database before it refused", v.args)
	}
	x.Equal(before, boxWritten(t, b.c), "a command refused for its format wrote something")

	s, err := cmd.Build(ctx, b.c)
	x.NoError(err)
	defer s.Close()
	_, err = cmd.CustomerOf(ctx, s, "newco", "f1")
	x.Error(err, "added, and then reported as a failure")
	_, err = cmd.CustomerOf(ctx, s, "newco", "kept")
	x.NoError(err, "erased, and then reported as a failure")

	// And every format there is is still one.
	for _, format := range []string{"pretty", "json", "protojson", "prototext", "name", "table", "wide", "template={{.alias}}"} {
		_, _, err := boxRun(t, at(b.c, "admin"), "", "holder", "get", "-o", format, "--in", "protojson", "@newco/kept")
		x.NoError(err, format)
	}
}
