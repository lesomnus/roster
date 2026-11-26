package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"github.com/lesomnus/roster/cmd"
	"log/slog"
	"net"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/roster/ldap"
	"github.com/lesomnus/roster/ldap/wire"
)

// NewCmdLdap is `roster ldap`: roster as a directory, for the clients that
// speak LDAP and nothing else, as a process of its own.
//
// It is a subcommand of this binary for `roster account`'s reason -- one thing
// to build and pin, the same `rstr` clients -- and a separate process for the
// same reason too: it holds tenant keys and faces a network of appliances, and
// roster's own listeners must not be in the process that does. It dials roster
// over the wire like any other consumer, and is told everything from the shell
// (`docs/ldap.md`).
func NewCmdLdap(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "ldap",
		Brief: "roster as a directory, over LDAP",

		Commands: xli.Commands{newCmdLdapServe(l, c)},

		Handler: xli.RequireSubcommand(),
	}
}

// LdapKeyPrefix is the environment form of `--tenant-key`:
// `ROSTER_LDAP_TENANT_KEY_<ALIAS>`.
const LdapKeyPrefix = "ROSTER_LDAP_TENANT_KEY_"

func newCmdLdapServe(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "answer LDAP binds and searches from roster's rows",

		// Those in `cfg.Bind` are `ldap:`'s settings, applied by the load over
		// the file and the environment when given; see `cli/account.go`. The
		// two that are not are read in the handler, which says why.
		Flags: flg.Flags{
			cfg.Bind(l, &c.Ldap.Addr, &flg.String{Name: "listen", Brief: "where to speak LDAP, StartTLS offered when --tls is given; :389 if empty and --listen-tls is not given"}),
			cfg.Bind(l, &c.Ldap.AddrTls, &flg.String{Name: "listen-tls", Brief: "where to speak LDAPS; needs --tls"}),
			cfg.Bind(l, &c.Ldap.Roster, &flg.String{Name: "roster", Brief: "roster's data plane, gRPC: host:port"}),
			cfg.Bind(l, &c.Ldap.Insecure, &flg.Switch{Name: "insecure", Brief: "dial roster without TLS"}),
			&flg.Strings{Name: "tenant-key", Brief: "a tenant key, as alias=rt_…; repeat per tenant fronted. Or " + LdapKeyPrefix + "<ALIAS> in the environment"},
			cfg.BindText(l, &c.Ldap.DeploymentKey, &flg.String{Name: "deployment-key", Brief: "one deployment key for every tenant that nominated it: rk_…, or one reference as the whole value, '${file:/run/key}' or '${env:NAME}' in single quotes; instead of --tenant-key"}),
			&flg.Strings{Name: "base", Brief: "a tenant's suffix, as alias=dc=…; o=<alias> if not given"},
			cfg.Bind(l, &c.Ldap.Bind, &flg.String{Name: "bind", Brief: "what a bind's password may be: key (an app password the person minted; the default), password (their own), either"}),
			cfg.BindFunc(l, &c.Ldap.Tls, &flg.String{Name: "tls", Brief: "this server's certificate and key, as cert.pem,key.pem; offers StartTLS and enables --listen-tls"}, certPair),
			cfg.Bind(l, &c.Ldap.RequireTls, &flg.Switch{Name: "require-tls", Brief: "refuse a bind in the clear; a client must StartTLS or use LDAPS first"}),
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			ctx, stop, err := telemetry(ctx, c, "roster-ldap")
			if err != nil {
				return err
			}
			defer stop()

			// The block, with the flags bound above already in it.
			lc := c.Ldap

			// `--base` adds to the block's suffixes rather than replacing
			// them, so it is read here and not bound.
			if vs, _ := flg.Find[[]string](cl, "base"); len(vs) > 0 {
				if lc.Bases == nil {
					lc.Bases = map[string]string{}
				}
				for _, v := range vs {
					alias, suffix, ok := strings.Cut(v, "=")
					if !ok || alias == "" || suffix == "" {
						return invocation(fmt.Errorf("--base %q: alias=dc=…", v))
					}
					lc.Bases[alias] = suffix
				}
			}

			// `--tenant-key` is merged with the block and the environment by
			// [keysOf], so it is read here and not bound either.
			given, _ := flg.Find[[]string](cl, "tenant-key")
			if err := oneWay("ldap", lc.DeploymentKey, lc.TenantKeys, LdapKeyPrefix, given); err != nil {
				return err
			}

			var f appKeys
			if !lc.DeploymentKey.IsZero() {
				if f.key, err = lc.DeploymentKey.Value(); err != nil {
					return fmt.Errorf("--deployment-key (ldap.deployment_key): %w", err)
				}
			} else {
				if f.keys, err = keysOf(lc.TenantKeys, LdapKeyPrefix, given); err != nil {
					return err
				}
			}

			if lc.Roster == "" {
				return errors.New("--roster (or ldap.roster): where roster speaks gRPC")
			}
			// Here and not a `Default` on `--listen`: `:389` is only for a
			// directory that names no `addr_tls` either.
			if lc.Addr == "" && lc.AddrTls == "" {
				lc.Addr = ":389"
			}

			return serveLdap(ctx, lc, f)
		}),
	}
}

// certPair is `--tls cert.pem,key.pem`, as the `ldap.tls` block it sets whole.
// Empty is an empty block, which is what `--tls=` asks for: no certificate,
// whatever the file named.
//
// Two paths, because a flag names files as a shell does; each is set as a
// reference to its file, so that `roster config` shows where they are.
func certPair(v string) (cmd.TlsConfig, error) {
	if v == "" {
		return cmd.TlsConfig{}, nil
	}

	certFile, keyFile, ok := strings.Cut(v, ",")
	if !ok {
		return cmd.TlsConfig{}, fmt.Errorf("%q: cert.pem,key.pem", v)
	}

	var c cmd.TlsConfig
	if err := c.Cert.SetFile(certFile); err != nil {
		return cmd.TlsConfig{}, err
	}
	if err := c.Key.SetFile(keyFile); err != nil {
		return cmd.TlsConfig{}, err
	}

	return c, nil
}

// directoryDefaults is what `ldap:` holds before the file is read, where `c`
// does not hold it already: the bounds on connections, which are `ldap/wire`'s
// and are the root's here so that `roster config` shows the number in effect.
// Nought is the same number wherever it is read ([wire.Server]), so this is
// what is shown and not what is served.
func directoryDefaults(c *cmd.LdapConfig) {
	if c.MaxConns == 0 {
		c.MaxConns = wire.DefaultMaxConns
	}
	if c.MaxConnsPerAddr == 0 {
		c.MaxConnsPerAddr = wire.DefaultMaxConnsPerAddr
	}
}

// directoryCertificate is `ldap.tls` as the listener's certificate, read now.
//
// Each refusal names the keys as the file writes them, with the flag beside
// them, and none says what a key holds: one of them is a private key, and the
// value may be the PEM itself.
func directoryCertificate(c cmd.TlsConfig) (tls.Certificate, error) {
	switch {
	case c.Cert.IsZero():
		return tls.Certificate{}, errors.New("ldap.tls.cert (--tls): ldap.tls.key is named, and no certificate beside it. name both, or neither")
	case c.Key.IsZero():
		return tls.Certificate{}, errors.New("ldap.tls.key (--tls): ldap.tls.cert is named, and no key beside it. name both, or neither")
	}

	cert, err := c.Cert.Value()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("ldap.tls.cert (--tls): %w", err)
	}
	key, err := c.Key.Value()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("ldap.tls.key (--tls): %w", err)
	}
	if !x509.NewCertPool().AppendCertsFromPEM([]byte(cert)) {
		return tls.Certificate{}, errors.New("ldap.tls.cert (--tls): holds no certificate")
	}
	pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("ldap.tls.cert, ldap.tls.key (--tls): not a certificate and its key: %w", err)
	}

	return pair, nil
}

// serveLdap answers LDAP until ctx is done.
//
// Told a [cmd.LdapConfig] and the keys read from it, for the reason
// `serveAccount` is:
// it is called from the command above and from `roster serve` when `ldap:`
// names an address, and both have to build the same thing. It builds no
// telemetry; both callers already have.
func serveLdap(ctx context.Context, lc cmd.LdapConfig, f appKeys) error {
	// Both names, in every refusal below. The value reached here from a block
	// or from a flag and this cannot tell which, so naming one would be right
	// half the time -- and a reader who has only ever used the other would be
	// told about a setting they do not have.
	// What has a form, held to it before anything is dialled; `roster serve`
	// asks the same in `cmd.Build`.
	if err := lc.Meant(); err != nil {
		return err
	}
	mode, err := ldap.ParseMode(lc.Bind)
	if err != nil {
		return fmt.Errorf("ldap.bind (--bind): %w", err)
	}

	var tlsConfig *tls.Config
	if lc.Tls.IsSet() {
		cert, err := directoryCertificate(lc.Tls)
		if err != nil {
			return err
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}
	if lc.AddrTls != "" && tlsConfig == nil {
		return errors.New("ldap.addr_tls (--listen-tls) needs ldap.tls (--tls)")
	}
	if lc.RequireTls && tlsConfig == nil && lc.AddrTls == "" {
		return errors.New("ldap.require_tls (--require-tls) with nothing to offer: give ldap.tls (--tls)")
	}

	cfg := ldap.Config{
		Roster:   lc.Roster,
		Keys:     f.keys,
		Key:      f.key,
		Bases:    lc.Bases,
		Bind:     mode,
		Insecure: lc.Insecure,
		Log:      log.From(ctx),

		// What the root DSE says of StartTLS is what the listener below does:
		// one certificate, said to both.
		StartTLS: tlsConfig != nil,
	}

	d, err := ldap.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer d.Close()

	s := &wire.Server{
		Handler: d.Handler(), TLS: tlsConfig, RequireTLS: lc.RequireTls, Log: log.From(ctx),
		MaxConns: lc.MaxConns, MaxConnsPerAddr: lc.MaxConnsPerAddr,
	}
	defer s.Close()

	errs := make(chan error, 2)
	serve := func(l net.Listener, how string) {
		log.From(ctx).InfoContext(ctx, "ldap", slog.String("addr", l.Addr().String()), slog.String("how", how),
			slog.Int("tenants", len(d.NamingContexts())), slog.Any("suffixes", d.NamingContexts()))
		go func() {
			<-ctx.Done()
			_ = l.Close()
		}()
		errs <- s.Serve(l)
	}
	n := 0
	if lc.Addr != "" {
		l, err := cmd.Listen(ctx, lc.Addr)
		if err != nil {
			return err
		}
		n++
		go serve(l, "ldap")
	}
	if lc.AddrTls != "" {
		l, err := cmd.Listen(ctx, lc.AddrTls)
		if err != nil {
			return err
		}
		n++
		go serve(tls.NewListener(l, tlsConfig), "ldaps")
	}
	// A listener that ends with an error ends this, and so the process: under
	// `roster serve` that is roster's own ports too, as it is for the account
	// app, SCIM and the Login App there, each of which returns what its
	// listener returned. It is not an accept that failed for a moment -- the
	// process out of descriptors, which [wire.Server.Serve] waits out and
	// which used to come back here and stop roster -- but a listener that is
	// broken, and a process whose directory is gone for good is one to start
	// again and not one to leave answering everything else.
	for range n {
		if err := <-errs; err != nil {
			return err
		}
	}

	return nil
}
