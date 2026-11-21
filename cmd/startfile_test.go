package cmd_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
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

// loadedFrom is `over` with the file at `path` read over it, as the loader
// reads one: what `over` holds is the defaults the file is laid on.
func loadedFrom(t *testing.T, over cmd.Config, path string) cmd.Config {
	t.Helper()

	k := cli.Cmd(&over)
	k.Writer = io.Discard
	require.NoError(t, k.Run(t.Context(), []string{"--config", path, "config"}), "%s was not read", path)

	return over
}

// startableHere is `c` with what it names on the disk of the machine it was
// written for -- a page's directory, a certificate -- named by something that
// is there on this one, so that what is held of it is what the file says.
func startableHere(t *testing.T, c cmd.Config) cmd.Config {
	t.Helper()

	dirs := []*string{&c.UserConsole.Dir, &c.Admin.Console.Dir, &c.Account.Page.Dir, &c.Login.Page.Dir}
	for _, d := range dirs {
		if *d != "" {
			*d = t.TempDir()
		}
	}
	for _, b := range listenerBlocks(&c) {
		if b.Tls.Active() {
			pair := startCertificateOf(t)
			b.Tls.CertFile, b.Tls.KeyFile = pair.cert, pair.key
			if b.Tls.ClientCAFile != "" {
				b.Tls.ClientCAFile = pair.cert
			}
		}
	}

	return c
}

// TestTheFilesADeploymentStartsFromAreMeant holds what `cmd.Config.Meant`
// refuses to the files this repository ships and the blocks its pages show:
// `roster.yaml`, `deploy/config.yaml`, what `compose.yaml` says to `roster
// serve` through the environment, and every `yaml` block of the pages, each
// laid over `roster.yaml` as the page reads it, since most are a part.
func TestTheFilesADeploymentStartsFromAreMeant(t *testing.T) {
	root := filepath.Join("..")
	empty := cmd.Config{}

	for _, f := range []string{"roster.yaml", filepath.Join("deploy", "config.yaml")} {
		c := loadedFrom(t, empty, filepath.Join(root, f))
		require.NoError(t, startableHere(t, c).Meant(), f)
	}

	t.Run("compose.yaml", func(t *testing.T) {
		src, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
		require.NoError(t, err)

		// The `roster` service's environment, up to the service after it.
		body := string(src)
		body = body[strings.Index(body, "\n  roster:\n"):]
		body = body[:strings.Index(body[1:], "\n  customer:\n")]
		said := 0
		for _, m := range regexp.MustCompile(`(?m)^\s+(ROSTER_[A-Z_]+): (.+)$`).FindAllStringSubmatch(body, -1) {
			v := strings.Trim(strings.TrimSpace(m[2]), `'"`)
			if d := regexp.MustCompile(`^\$\{[A-Z_]+:-(.*)\}$`).FindStringSubmatch(v); d != nil {
				v = d[1]
			}
			t.Setenv(m[1], v)
			said++
		}
		require.NotZero(t, said, "compose.yaml says nothing to roster serve any more")

		file := filepath.Join(t.TempDir(), "roster.yaml")
		require.NoError(t, os.WriteFile(file, nil, 0o600))
		c := loadedFrom(t, empty, file)
		require.Equal(t, "pgx", c.Db.Driver, "what compose.yaml says was not read")
		require.NoError(t, startableHere(t, c).Meant())
	})

	t.Run("the pages", func(t *testing.T) {
		x := require.New(t)
		base := loadedFrom(t, empty, filepath.Join(root, "roster.yaml"))

		// A block that names a broker or a lease of PostgreSQL's is read on
		// the database the page says it needs.
		pg := base
		pg.Db = config.DbConfig{Driver: "pgx", Dsn: "postgres://roster@db/roster"}
		pg.Control.Db = config.DbConfig{Driver: "pgx", Dsn: "postgres://roster@db/control"}

		pages, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
		x.NoError(err)
		more, err := filepath.Glob(filepath.Join(root, "docs", "*", "*.md"))
		x.NoError(err)
		pages = append(pages, more...)
		pages = append(pages, filepath.Join(root, "README.md"))

		shown := 0
		for _, page := range pages {
			src, err := os.ReadFile(page)
			x.NoError(err)

			for _, m := range regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatchIndex(string(src), -1) {
				body := string(src[m[2]:m[3]])
				if !regexp.MustCompile(`(?m)^(server|control|admin|db|watch|spin|user_console|sign_in|policy|vouch|stream|holder|consent|tenant|host|account|ldap|login|scim):`).MatchString(body) {
					continue
				}
				where := page + ":" + strconv.Itoa(1+strings.Count(string(src[:m[2]]), "\n"))

				file := filepath.Join(t.TempDir(), "roster.yaml")
				x.NoError(os.WriteFile(file, []byte(body), 0o600))
				c := base
				if strings.Contains(body, "postgres") {
					c = pg
				}
				k := cli.Cmd(&c)
				k.Writer = io.Discard
				if err := k.Run(t.Context(), []string{"--config", file, "config"}); err != nil {
					// Not a file of roster's: a resource, a client's own.
					continue
				}
				x.NoError(startableHere(t, c).Meant(), where)
				shown++
			}
		}
		x.Greater(shown, 10, "the pages show fewer blocks of a deployment's file than they did")
	})
}
