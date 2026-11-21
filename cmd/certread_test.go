package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"

	"github.com/lesomnus/roster/cmd"
)

// TestACertificateThatCannotBeReadIsRefusedBeforeAnythingIsOpened: a listener
// block's certificate was read where its gRPC server was made, after the
// resources were applied, the port was bound and the loops had started, and
// its refusal named no block: `tls: both cert_file and key_file must be set`,
// for any of three. Each block's is read before anything is opened, both or
// neither of the two, and each a file that can be read, naming the key.
func TestACertificateThatCannotBeReadIsRefusedBeforeAnythingIsOpened(t *testing.T) {
	pair := startCertificateOf(t)
	gone := filepath.Join(t.TempDir(), "gone.pem")
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	require.NoError(t, os.WriteFile(garbage, []byte("no certificate"), 0o600))

	for _, block := range []string{"server", "control", "admin"} {
		for _, v := range []struct {
			name string
			key  string
			tls  config.TlsConfig
		}{
			{"a certificate and no key", block + ".tls.key_file", config.TlsConfig{CertFile: pair.cert}},
			{"a key and no certificate", block + ".tls.cert_file", config.TlsConfig{KeyFile: pair.key}},
			{"tls and neither", block + ".tls.cert_file", config.TlsConfig{Enabled: true}},
			{"a certificate that is not there", block + ".tls.cert_file", config.TlsConfig{CertFile: gone, KeyFile: pair.key}},
			{"a key that is not there", block + ".tls.key_file", config.TlsConfig{CertFile: pair.cert, KeyFile: gone}},
			{"a certificate that is none", block + ".tls.cert_file", config.TlsConfig{CertFile: garbage, KeyFile: pair.key}},
			{"an authority that is not there", block + ".tls.client_ca_file", config.TlsConfig{CertFile: pair.cert, KeyFile: pair.key, ClientCAFile: gone}},
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
				b.Tls = config.TlsConfig{CertFile: pair.cert, KeyFile: pair.key}
			}
		}))
	})
}
