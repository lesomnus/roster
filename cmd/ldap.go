package cmd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
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
func NewCmdLdap(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "ldap",
		Brief: "roster as a directory, over LDAP",

		Commands: xli.Commands{newCmdLdapServe()},
	}
}

// LdapKeyPrefix is the environment form of `--key`: `ROSTER_LDAP_KEY_<ALIAS>`.
const LdapKeyPrefix = "ROSTER_LDAP_KEY_"

func newCmdLdapServe() *xli.Command {
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

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			roster, _ := flg.Find[string](cmd, "roster")
			if roster == "" {
				return errors.New("--roster: where roster speaks gRPC")
			}
			keys, err := keysFrom(cmd, LdapKeyPrefix)
			if err != nil {
				return err
			}
			bind, _ := flg.Find[string](cmd, "bind")
			mode, err := ldap.ParseMode(bind)
			if err != nil {
				return fmt.Errorf("--bind: %w", err)
			}

			cfg := ldap.Config{Roster: roster, Keys: keys, Bind: mode, Log: log.From(ctx)}
			cfg.Insecure, _ = flg.Find[bool](cmd, "insecure")
			bases, _ := flg.Find[[]string](cmd, "base")
			for _, v := range bases {
				alias, suffix, ok := strings.Cut(v, "=")
				if !ok || alias == "" || suffix == "" {
					return fmt.Errorf("--base %q: alias=dc=…", v)
				}
				if cfg.Bases == nil {
					cfg.Bases = map[string]string{}
				}
				cfg.Bases[alias] = suffix
			}

			var tlsConfig *tls.Config
			if v, _ := flg.Find[string](cmd, "tls"); v != "" {
				certFile, keyFile, ok := strings.Cut(v, ",")
				if !ok {
					return fmt.Errorf("--tls %q: cert.pem,key.pem", v)
				}
				cert, err := tls.LoadX509KeyPair(certFile, keyFile)
				if err != nil {
					return fmt.Errorf("--tls: %w", err)
				}
				tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
			}

			plain, _ := flg.Find[string](cmd, "listen")
			secure, _ := flg.Find[string](cmd, "listen-tls")
			if secure != "" && tlsConfig == nil {
				return errors.New("--listen-tls needs --tls")
			}
			if plain == "" && secure == "" {
				plain = ":389"
			}
			require, _ := flg.Find[bool](cmd, "require-tls")
			if require && tlsConfig == nil && secure == "" {
				return errors.New("--require-tls with nothing to offer: give --tls")
			}

			d, err := ldap.New(ctx, cfg)
			if err != nil {
				return err
			}
			defer d.Close()

			s := &wire.Server{Handler: d.Handler(), TLS: tlsConfig, RequireTLS: require, Log: log.From(ctx)}
			defer s.Close()

			errs := make(chan error, 2)
			serve := func(l net.Listener, how string) {
				log.From(ctx).InfoContext(ctx, "ldap", slog.String("addr", l.Addr().String()), slog.String("how", how),
					slog.Int("tenants", len(keys)), slog.Any("suffixes", d.NamingContexts()))
				go func() {
					<-ctx.Done()
					_ = l.Close()
				}()
				errs <- s.Serve(l)
			}
			n := 0
			if plain != "" {
				l, err := net.Listen("tcp", plain)
				if err != nil {
					return err
				}
				n++
				go serve(l, "ldap")
			}
			if secure != "" {
				l, err := tls.Listen("tcp", secure, tlsConfig)
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
		}),
	}
}
