package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/xli/cfg/ref"

	"github.com/lesomnus/roster/cmd"
)

// TestACertificateThatCannotBeReadIsRefusedBeforeAnythingIsOpened: a listener
// block's certificate was read where its gRPC server was made, after the
// resources were applied, the port was bound and the loops had started, and
// its refusal named no block: `tls: both cert and key must be set`, for any
// of three. Each block's is read before anything is opened, both or neither of
// the two, and each a value that can be read, naming the key.
func TestACertificateThatCannotBeReadIsRefusedBeforeAnythingIsOpened(t *testing.T) {
	pair := startCertificateOf(t)
	cert := startCertificateFile(t, pair.cert)
	key := startCertificateFile(t, pair.key)
	gone := startCertificateFile(t, filepath.Join(t.TempDir(), "gone.pem"))
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	require.NoError(t, os.WriteFile(garbage, []byte("no certificate"), 0o600))

	for _, block := range []string{"server", "control", "admin"} {
		for _, v := range []struct {
			name string
			key  string
			tls  config.TlsConfig
		}{
			{"a certificate and no key", block + ".tls.key", config.TlsConfig{Cert: cert}},
			{"a key and no certificate", block + ".tls.cert", config.TlsConfig{Key: key}},
			{"tls and neither", block + ".tls.cert", config.TlsConfig{Enabled: true}},
			{"a certificate that is not there", block + ".tls.cert", config.TlsConfig{Cert: gone, Key: key}},
			{"a key that is not there", block + ".tls.key", config.TlsConfig{Cert: cert, Key: gone}},
			{"a certificate that is none", block + ".tls.cert", config.TlsConfig{Cert: startCertificateFile(t, garbage), Key: key}},
			{"an authority that is not there", block + ".tls.client_ca", config.TlsConfig{Cert: cert, Key: key, ClientCA: gone}},
			{"an authority that is none", block + ".tls.client_ca", config.TlsConfig{Cert: cert, Key: key, ClientCA: startCertificateFile(t, garbage)}},
		} {
			t.Run(block+", "+v.name, func(t *testing.T) {
				c := cmd.Config{
					Db:      config.DbConfig{Driver: "sqlite3", Dsn: "file:" + filepath.Join(t.TempDir(), "roster.db")},
					Control: cmd.ControlConfig{Db: config.DbConfig{Driver: "sqlite3", Dsn: "file:" + filepath.Join(t.TempDir(), "control.db")}},
				}
				listenerBlocks(&c)[block].Tls = v.tls

				// Asked of the file alone, which is what `cmd.Build` asks
				// before it opens a database.
				err := c.Meant()
				require.Error(t, err)
				require.True(t, strings.HasPrefix(err.Error(), v.key), "named: %s", err)
			})
		}
	}

	t.Run("one that can be read starts", func(t *testing.T) {
		require.NoError(t, startedWith(t, func(c *cmd.Config) {
			for _, b := range listenerBlocks(c) {
				b.Tls = pair.tls(t)
			}
		}))
	})

	// A value is read as payday reads it, so a certificate written into the
	// file is read as one named by its path is.
	t.Run("one written into the file starts", func(t *testing.T) {
		written := func(path string) ref.String {
			b, err := os.ReadFile(path)
			require.NoError(t, err)

			var v ref.String
			require.NoError(t, v.UnmarshalText(b))

			return v
		}

		require.NoError(t, startedWith(t, func(c *cmd.Config) {
			for _, b := range listenerBlocks(c) {
				b.Tls = config.TlsConfig{Cert: written(pair.cert), Key: written(pair.key), ClientCA: written(pair.cert)}
			}
		}))
	})
}
