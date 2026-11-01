package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lesomnus/mkot"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestAnOtelBlockMayNameACollector: `otel:` naming an `otlp` exporter -- how a
// deployment in a cluster whose apps send their logs to a collector on the node
// says so -- is read, and built into what `serve` logs through.
//
// It was refused as an unknown type when the file was read, because this
// binary linked no exporter by that name (#214). What was left was `pretty`,
// so roster's log stayed on stderr beside apps whose logs did not.
func TestAnOtelBlockMayNameACollector(t *testing.T) {
	x := require.New(t)

	path := filepath.Join(t.TempDir(), "roster.yaml")
	x.NoError(os.WriteFile(path, []byte(
		"otel:\n"+
			"  exporters:\n"+
			"    otlp:\n"+
			"      endpoint: 127.0.0.1:4317\n"+
			"  providers:\n"+
			"    logger:\n"+
			"      exporters: [otlp]\n"), 0o600))

	c := &cmd.Config{}
	k := Cmd(c)
	k.Writer = io.Discard
	x.NoError(k.Run(t.Context(), []string{"--config", path, "config"}), "the configuration was refused")
	x.Contains(c.Otel.Exporters, mkot.Id("otlp"))

	_, done, err := telemetry(t.Context(), c, cmd.Name)
	x.NoError(err, "what the configuration describes was not built")
	done()
}
