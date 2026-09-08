package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/lesomnus/roster/cmd"
	"log/slog"
	"net"
	"strings"

	"github.com/lesomnus/xli"
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
func NewCmdLdap(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "ldap",
		Brief: "roster as a directory, over LDAP",

		Commands: xli.Commands{newCmdLdapServe(c)},
	}
}

// LdapKeyPrefix is the environment form of `--key`: `ROSTER_LDAP_KEY_<ALIAS>`.
const LdapKeyPrefix = "ROSTER_LDAP_KEY_"

func newCmdLdapServe(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "answer LDAP binds and searches from roster's rows",

		Flags: flg.Flags{
			&flg.String{Name: "listen", Brief: "where to speak LDAP, StartTLS offered when --tls is given; :389 if empty and --listen-tls is not given"},
			&flg.String{Name: "listen-tls", Brief: "where to speak LDAPS; needs --tls"},
			&flg.String{Name: "roster", Brief: "roster's data plane, gRPC: host:port"},
			&flg.Switch{Name: "insecure", Brief: "dial roster without TLS"},
			&flg.Strings{Name: "key", Brief: "a tenant key, as alias=rt_…; repeat per operator fronted. Or " + LdapKeyPrefix + "<ALIAS> in the environment"},
			&flg.Strings{Name: "base", Brief: "a tenant's suffix, as alias=dc=…; o=<alias> if not given"},
			&flg.String{Name: "bind", Brief: "what a bind's password may be: key (an app password the person minted; the default), password (their own), either"},
			&flg.String{Name: "tls", Brief: "this server's certificate and key, as cert.pem,key.pem; offers StartTLS and enables --listen-tls"},
			&flg.Switch{Name: "require-tls", Brief: "refuse a bind in the clear; a client must StartTLS or use LDAPS first"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			ctx, stop, err := telemetry(ctx, c, "roster-ldap")
			if err != nil {
				return err
			}
			defer stop()

			// The block first, then the flags over it. See `cli/account.go`.
			lc := c.Ldap
			if v, _ := flg.Find[string](cl, "listen"); v != "" {
				lc.Addr = v
			}
			if v, _ := flg.Find[string](cl, "listen-tls"); v != "" {
				lc.AddrTls = v
			}
			if v, _ := flg.Find[string](cl, "roster"); v != "" {
				lc.Roster = v
			}
			if v, _ := flg.Find[bool](cl, "insecure"); v {
				lc.Insecure = true
			}
			if v, _ := flg.Find[string](cl, "bind"); v != "" {
				lc.Bind = v
			}
			if v, _ := flg.Find[bool](cl, "require-tls"); v {
				lc.RequireTls = true
			}
			if v, _ := flg.Find[string](cl, "tls"); v != "" {
				certFile, keyFile, ok := strings.Cut(v, ",")
				if !ok {
					return fmt.Errorf("--tls %q: cert.pem,key.pem", v)
				}
				lc.Tls = cmd.TlsConfig{Cert: certFile, Key: keyFile}
			}
			if vs, _ := flg.Find[[]string](cl, "base"); len(vs) > 0 {
				if lc.Bases == nil {
					lc.Bases = map[string]string{}
				}
				for _, v := range vs {
					alias, suffix, ok := strings.Cut(v, "=")
					if !ok || alias == "" || suffix == "" {
						return fmt.Errorf("--base %q: alias=dc=…", v)
					}
					lc.Bases[alias] = suffix
				}
			}

			given, _ := flg.Find[[]string](cl, "key")
			keys, err := keysOf(lc.Keys, LdapKeyPrefix, given)
			if err != nil {
				return err
			}
			lc.Keys = keys

			if lc.Roster == "" {
				return errors.New("--roster (or ldap.roster): where roster speaks gRPC")
			}
			if lc.Addr == "" && lc.AddrTls == "" {
				lc.Addr = ":389"
			}

			return serveLdap(ctx, lc)
		}),
	}
}

// serveLdap answers LDAP until ctx is done.
//
// Told a [cmd.LdapConfig] and nothing else, for the reason `serveAccount` is:
// it is called from the command above and from `roster serve` when `ldap:`
// names an address, and both have to build the same thing. It builds no
// telemetry; both callers already have.
func serveLdap(ctx context.Context, lc cmd.LdapConfig) error {
	// Both names, in every refusal below. The value reached here from a block
	// or from a flag and this cannot tell which, so naming one would be right
	// half the time -- and a reader who has only ever used the other would be
	// told about a setting they do not have.
	mode, err := ldap.ParseMode(lc.Bind)
	if err != nil {
		return fmt.Errorf("ldap.bind (--bind): %w", err)
	}

	var tlsConfig *tls.Config
	if lc.Tls.IsSet() {
		if lc.Tls.Cert == "" || lc.Tls.Key == "" {
			return errors.New("ldap.tls (--tls): both a cert and a key")
		}
		cert, err := tls.LoadX509KeyPair(lc.Tls.Cert, lc.Tls.Key)
		if err != nil {
			return fmt.Errorf("ldap.tls (--tls): %w", err)
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
		Keys:     lc.Keys,
		Bases:    lc.Bases,
		Bind:     mode,
		Insecure: lc.Insecure,
		Log:      log.From(ctx),
	}

	d, err := ldap.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer d.Close()

	s := &wire.Server{Handler: d.Handler(), TLS: tlsConfig, RequireTLS: lc.RequireTls, Log: log.From(ctx)}
	defer s.Close()

	errs := make(chan error, 2)
	serve := func(l net.Listener, how string) {
		log.From(ctx).InfoContext(ctx, "ldap", slog.String("addr", l.Addr().String()), slog.String("how", how),
			slog.Int("tenants", len(lc.Keys)), slog.Any("suffixes", d.NamingContexts()))
		go func() {
			<-ctx.Done()
			_ = l.Close()
		}()
		errs <- s.Serve(l)
	}
	n := 0
	if lc.Addr != "" {
		l, err := net.Listen("tcp", lc.Addr)
		if err != nil {
			return err
		}
		n++
		go serve(l, "ldap")
	}
	if lc.AddrTls != "" {
		l, err := tls.Listen("tcp", lc.AddrTls, tlsConfig)
		if err != nil {
			return err
		}
		n++
		go serve(l, "ldaps")
	}
	for range n {
		if err := <-errs; err != nil {
			return err
		}
	}

	return nil
}
