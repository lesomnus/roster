package cmd_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
)

// What a file a deployment starts from is held to before anything is opened
// (#284), and the helpers its tests share.

// startedWith is what `cmd.Build` answers a deployment with both planes, said
// as `with` says over them.
func startedWith(t *testing.T, with func(*cmd.Config)) error {
	t.Helper()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}
	if with != nil {
		with(&c)
	}

	s, err := cmd.Build(t.Context(), c)
	if s != nil {
		s.Close()
	}

	return err
}

// startCertificate is a certificate for 127.0.0.1 and its key, written to
// files, which serves as the authority for client certificates too.
type startCertificate struct{ cert, key string }

func startCertificateOf(t *testing.T) startCertificate {
	t.Helper()
	x := require.New(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	x.NoError(err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "roster"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	x.NoError(err)
	kder, err := x509.MarshalECPrivateKey(key)
	x.NoError(err)

	dir := t.TempDir()
	v := startCertificate{cert: filepath.Join(dir, "cert.pem"), key: filepath.Join(dir, "key.pem")}
	x.NoError(os.WriteFile(v.cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	x.NoError(os.WriteFile(v.key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600))

	return v
}

// listenerBlocks is each of the three listener blocks of a file, by the name
// the file writes it with.
func listenerBlocks(c *cmd.Config) map[string]*config.ServerConfig {
	return map[string]*config.ServerConfig{
		"server":  &c.Server,
		"control": &c.Control.ServerConfig,
		"admin":   &c.Admin.ServerConfig,
	}
}
