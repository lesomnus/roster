package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestABlockThatAsksForACertificateHasNoPlainPortBesideIt: a listener block's
// `tls:` is its gRPC listener's alone, and the `http` listener beside it is
// plain. So a block whose `tls.client_ca` requires a certificate of every
// caller answered a caller with none on its `http.addr`: the gRPC port refused
// the handshake, and the same calls, transcoded, answered 401 from the server.
// The second listener answers whoever the first keeps out, and the two cannot
// both be meant; the file is refused, naming both keys.
func TestABlockThatAsksForACertificateHasNoPlainPortBesideIt(t *testing.T) {
	pair := startCertificateOf(t)

	for _, block := range []string{"server", "control", "admin"} {
		t.Run(block, func(t *testing.T) {
			x := require.New(t)
			err := startedWith(t, func(c *cmd.Config) {
				b := listenerBlocks(c)[block]
				b.Addr = "127.0.0.1:0"
				b.Tls = pair.tls(t)
				b.Tls.ClientCA = startCertificateFile(t, pair.cert)
				b.Http.Addr = "127.0.0.1:0"
				b.Http.AllowWeb = true
			})
			x.Error(err, "a certificate asked of every caller, and a plain port beside it")
			x.Contains(err.Error(), block+".tls.client_ca")
			x.Contains(err.Error(), block+".http.addr")
		})
	}

	t.Run("a certificate asked, and no plain port", func(t *testing.T) {
		require.NoError(t, startedWith(t, func(c *cmd.Config) {
			c.Server.Tls = pair.tls(t)
			c.Server.Tls.ClientCA = startCertificateFile(t, pair.cert)
		}))
	})
}

// TestTlsBesideAnHttpPortIsSaidAtStart: a block that says `tls:` and an
// `http.addr` serves the second in the clear, which the file does not say and
// nothing said. It is right where a proxy in front holds the certificate, so it
// is not refused: `serve` says once as it starts, at `WARN`, that the `http`
// listener is plain, naming the block.
func TestTlsBesideAnHttpPortIsSaidAtStart(t *testing.T) {
	const what = "is plain"
	pair := startCertificateOf(t)

	t.Run("tls and an http port", func(t *testing.T) {
		x := require.New(t)
		said := servedSaying(t, func(c *cmd.Config) {
			c.Server.Tls = pair.tls(t)
			c.Server.Http.Addr = "127.0.0.1:0"
			c.Server.Http.AllowWeb = true
		}).lines(t, what)
		x.Len(said, 1, "said once, as serve starts")
		x.Equal("WARN", said[0]["level"])
		x.Contains(said[0]["msg"], "server.tls")
		x.Contains(said[0]["msg"], "server.http.addr")
	})

	t.Run("tls and no http port", func(t *testing.T) {
		require.Empty(t, servedSaying(t, func(c *cmd.Config) {
			c.Server.Tls = pair.tls(t)
		}).lines(t, what))
	})

	t.Run("an http port and no tls", func(t *testing.T) {
		require.Empty(t, servedSaying(t, func(c *cmd.Config) {
			c.Server.Http.Addr = "127.0.0.1:0"
			c.Server.Http.AllowWeb = true
		}).lines(t, what))
	})
}
