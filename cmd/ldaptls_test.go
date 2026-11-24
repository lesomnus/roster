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
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/ldap/wire"
)

// TestLdapServeSaysStartTlsWhereItWasGivenACertificate is `roster ldap serve`
// with a certificate and without: the root DSE lists StartTLS for the one and
// not for the other, and a handshake is served.
//
// It listed StartTLS always, and with no certificate the request was then
// refused (#281). The certificate is the listener's and what the root DSE says
// is the directory's, so the command says it to both -- and this is what
// holds the two together, where a test of either alone would not.
//
// The certificate is given three ways: `--tls cert.pem,key.pem`, which names
// files as a shell does, and `ldap.tls` written as references to them or as
// the PEM itself, as a listener's `tls:` is. `ldap.tls.cert` and
// `ldap.tls.key` were paths, so a reference was refused at load and the PEM
// was read as a path (#284).
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
	certPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPem := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec})
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certFile, certPem, 0o600))
	require.NoError(t, os.WriteFile(keyFile, keyPem, 0o600))

	for _, tc := range []struct {
		name   string
		file   string
		args   []string
		listed bool
	}{
		{"with no certificate", "", nil, false},
		{"with one named by the flag", "", []string{"--tls", certFile + "," + keyFile}, true},
		{"with one written as references", "ldap:\n  tls:\n    cert: ${file:" + certFile + "}\n    key: ${file:" + keyFile + "}\n", nil, true},
		{"with one written as itself", "ldap:\n  tls:\n    cert: |\n" + ldapTlsIndented(certPem) + "    key: |\n" + ldapTlsIndented(keyPem), nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			l, addr := listening(t)
			run, cancel := context.WithCancel(cmd.Held(ctx, l))
			file := filepath.Join(t.TempDir(), "roster.yaml")
			x.NoError(os.WriteFile(file, []byte(tc.file), 0o600))
			done := make(chan error, 1)
			go func() {
				done <- cli.Cmd(&cmd.Config{}).Run(run, append([]string{
					"--config", file, "ldap", "serve", "--roster", b.Hers.Client.Addr, "--insecure", "--listen", addr,
				}, tc.args...))
			}()
			t.Cleanup(cancel)

			// The listener is held, so a dial is answered by the kernel
			// whether or not serve is still up: serve stopping is watched as
			// well, and what it said is the failure.
			var c *goldap.Conn
			var stopped error
			x.Eventually(func() bool {
				select {
				case stopped = <-done:
					return true
				default:
				}
				var err error
				c, err = goldap.DialURL("ldap://" + addr)

				return err == nil
			}, 10*time.Second, 50*time.Millisecond, "the directory did not come up on %s", addr)
			if c == nil {
				x.FailNow("serve stopped", "%v", stopped)
			}
			defer c.Close()
			c.SetTimeout(10 * time.Second)

			root, err := c.Search(goldap.NewSearchRequest("", goldap.ScopeBaseObject, goldap.NeverDerefAliases, 0, 0, false,
				"(objectClass=*)", []string{"supportedExtension"}, nil))
			if err != nil {
				select {
				case stopped = <-done:
				default:
				}
			}
			x.NoError(err, "serve: %v", stopped)
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

// ldapTlsIndented is a PEM document as a block scalar's lines under a key of
// `ldap.tls`.
func ldapTlsIndented(doc []byte) string {
	out := ""
	for _, line := range strings.Split(strings.TrimSuffix(string(doc), "\n"), "\n") {
		out += "      " + line + "\n"
	}

	return out
}

// TestADirectorysCertificateIsRefusedByItsKeys: `ldap.tls` is a value of the
// file, as a listener's `tls:` is, and what cannot be served is refused where
// the listener is built, naming the keys as the file writes them and never
// what they hold. It was `ldap.tls (--tls)` for all of it, with the value in
// the sentence where the value was not a file -- the PEM of a private key,
// into the log (#284). And `roster config` shows the certificate and leaves
// out the key, by its name.
func TestADirectorysCertificateIsRefusedByItsKeys(t *testing.T) {
	one, other := startCertificateOf(t), startCertificateOf(t)
	read := func(path string) string {
		b, err := os.ReadFile(path)
		require.NoError(t, err)

		return string(b)
	}
	block := func(tls string) string {
		return "ldap:\n  roster: 127.0.0.1:1\n  deployment_key: rk_x\n  tls:\n" + tls
	}
	ref := func(key, path string) string { return "    " + key + ": ${file:" + path + "}\n" }

	for name, tc := range map[string]struct {
		tls  string
		says []string
	}{
		"a certificate and no key":    {ref("cert", one.cert), []string{"ldap.tls.cert", "ldap.tls.key"}},
		"a key and no certificate":    {ref("key", one.key), []string{"ldap.tls.cert", "ldap.tls.key"}},
		"a pair that does not match":  {ref("cert", one.cert) + ref("key", other.key), []string{"ldap.tls.cert, ldap.tls.key"}},
		"a value with no certificate": {"    cert: NOCERTIFICATEHERE\n" + ref("key", one.key), []string{"ldap.tls.cert"}},
		"a key that is not there":     {ref("cert", one.cert) + ref("key", "/nowhere/key.pem"), []string{"ldap.tls.key"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := whichKeyRun(t, block(tc.tls), "ldap", "serve")
			require.Error(t, err)
			for _, s := range tc.says {
				require.ErrorContains(t, err, s)
			}
			require.NotContains(t, err.Error(), "NOCERTIFICATEHERE")
			require.NotContains(t, err.Error(), "BEGIN", "what a key holds was said")
		})
	}

	t.Run("roster config shows the certificate and not the key", func(t *testing.T) {
		x := require.New(t)

		pemKey := read(one.key)
		file := filepath.Join(t.TempDir(), "roster.yaml")
		x.NoError(os.WriteFile(file, []byte("ldap:\n  tls:\n    cert: ${file:"+one.cert+"}\n    key: |\n"+ldapTlsIndented([]byte(pemKey))), 0o600))
		var out strings.Builder
		k := cli.Cmd(&cmd.Config{})
		k.Writer = &out
		x.NoError(k.Run(t.Context(), []string{"--config", file, "config"}))

		x.Contains(out.String(), "${file:"+one.cert+"}")
		x.NotContains(out.String(), "PRIVATE KEY")
	})
}
