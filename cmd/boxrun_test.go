package cmd_test

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
)

// boxRun runs a whole command line as a shell at the box runs it, with
// `stdin` on standard input, and answers the two streams apart: what a script
// reading `$(roster …)` is handed, and what is said to the person beside it.
//
// The process's own streams are swapped, since the commands write to them as
// well as to their writer, so no test that calls this runs beside another.
func boxRun(t *testing.T, c *cmd.Config, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	x := require.New(t)

	or, ow, e := os.Pipe()
	x.NoError(e)
	er, ew, e := os.Pipe()
	x.NoError(e)
	ir, iw, e := os.Pipe()
	x.NoError(e)

	wasOut, wasErr, wasIn := os.Stdout, os.Stderr, os.Stdin
	os.Stdout, os.Stderr, os.Stdin = ow, ew, ir
	defer func() { os.Stdout, os.Stderr, os.Stdin = wasOut, wasErr, wasIn }()

	go func() {
		io.WriteString(iw, stdin)
		iw.Close()
	}()
	outc, errc := make(chan string, 1), make(chan string, 1)
	go func() { b, _ := io.ReadAll(or); outc <- string(b) }()
	go func() { b, _ := io.ReadAll(er); errc <- string(b) }()

	// The root's writers are left for `Run` to set, which it does from the
	// process's streams as they are now: the swapped ones.
	err = cli.Cmd(c).Run(t.Context(), args)

	ow.Close()
	ew.Close()
	ir.Close()

	return <-outc, <-errc, err
}
