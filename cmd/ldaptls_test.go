package cmd_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/ldap/wire"
)

// TestLdapServeSaysStartTlsWhereItWasGivenACertificate is `roster ldap serve`
// with `--tls` and without: the root DSE lists StartTLS for the one and not
// for the other.
//
// It listed StartTLS always, and with no certificate the request was then
// refused (#281). The certificate is the listener's and what the root DSE says
// is the directory's, so the command says it to both -- and this is what
// holds the two together, where a test of either alone would not.
func TestLdapServeSaysStartTlsWhereItWasGivenACertificate(t *testing.T) {
	b := cliUp(t, directoryReads...)
	ctx := t.Context()
	t.Setenv(cli.LdapKeyPrefix+"NEWCO", b.Token)

	// A certificate for this machine, in two files as `--tls` reads them.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "directory.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	sec, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec}), 0o600))

	for _, tc := range []struct {
		name   string
		args   []string
		listed bool
	}{
		{"with no certificate", nil, false},
		{"with one", []string{"--tls", certFile + "," + keyFile}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			l, addr := listening(t)
			run, cancel := context.WithCancel(cmd.Held(ctx, l))
			done := make(chan error, 1)
			go func() {
				done <- cli.Cmd(&cmd.Config{}).Run(run, append([]string{
					"--config=", "ldap", "serve", "--roster", b.Hers.Client.Addr, "--insecure", "--listen", addr,
				}, tc.args...))
			}()
			t.Cleanup(cancel)

			var c *goldap.Conn
			x.Eventually(func() bool {
				var err error
				c, err = goldap.DialURL("ldap://" + addr)

				return err == nil
			}, 10*time.Second, 50*time.Millisecond, "the directory did not come up on %s", addr)
			defer c.Close()

			root, err := c.Search(goldap.NewSearchRequest("", goldap.ScopeBaseObject, goldap.NeverDerefAliases, 0, 0, false,
				"(objectClass=*)", []string{"supportedExtension"}, nil))
			x.NoError(err)
			x.Len(root.Entries, 1)
			got := root.Entries[0].GetAttributeValues("supportedExtension")

			err = c.StartTLS(&tls.Config{InsecureSkipVerify: true})
			if tc.listed {
				x.Contains(got, wire.OidStartTLS, "a certificate was given, and the root DSE does not offer StartTLS")
				x.NoError(err, "listed, and then refused")
			} else {
				x.NotContains(got, wire.OidStartTLS, "no certificate was given, and the root DSE offers StartTLS")
				x.Error(err)
			}

			cancel()
			x.NoError(<-done, "serve did not stop cleanly when told to")
		})
	}
}
