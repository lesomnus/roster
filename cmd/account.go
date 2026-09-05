package cmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth/authsession"

	"github.com/lesomnus/roster/account"
)

// NewCmdAccount is `roster account`: the front door a customer's people sign in
// at, as a process of its own.
//
// # Why it is a subcommand of this binary and not a binary of its own
//
// One thing to build, version and pin, and the same `rstr` clients the rest of
// this binary already carries. What it is **not** is the same process as
// `roster serve`: the account app holds tenant keys and faces the internet, and
// roster's own listeners -- the admin port most of all -- must not be in the
// process that does. `roster account serve` dials roster over the wire like any
// other consumer would, which is the whole of what keeps it one.
//
// # It is told everything from the shell
//
// No `roster.yaml`: the account app is a consumer, and reading the server's
// configuration file would be the first thing a consumer knows about the server
// that it should not. Flags and environment, the way any other app deployed
// beside roster is told about it.
func NewCmdAccount(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "account",
		Brief: "the front door a customer's people sign in at",

		Commands: xli.Commands{newCmdAccountServe()},
	}
}

func newCmdAccountServe() *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "serve the sign-in page and the account screens, fronting roster",

		Flags: flg.Flags{
			&flg.String{Name: "listen", Brief: "where to listen; :8090 if empty"},
			&flg.String{Name: "roster", Brief: "roster's data plane, gRPC: host:port"},
			&flg.String{Name: "connect", Brief: "the same server over HTTP (server.http), for the page's calls: http(s)://host:port"},
			&flg.Switch{Name: "insecure", Brief: "dial roster without TLS"},
			&flg.String{Name: "base", Brief: "this app's public origin, registered with every provider as the redirect"},
			&flg.String{Name: "static", Brief: "a directory to serve as the page; empty serves none"},
			&flg.String{Name: "enrol", Brief: "what happens to a stranger a provider vouches for: invited (nobody) or enrolling"},
			&flg.Strings{Name: "key", Brief: "a tenant key, as alias=rt_…; repeat per operator fronted. Or ROSTER_ACCOUNT_KEY_<ALIAS> in the environment"},
			&flg.Switch{Name: "insecure-cookie", Brief: "a cookie without Secure, for a page served over plain http in development"},
			&flg.Strings{Name: "seal", Brief: "the key sessions are sealed into the cookie under, as env:NAME holding 32 bytes base64; repeat to rotate, the first seals. Empty is a key made at start, which is one replica"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			roster, _ := flg.Find[string](cmd, "roster")
			connect, _ := flg.Find[string](cmd, "connect")
			if roster == "" || connect == "" {
				return errors.New("--roster and --connect: where roster speaks gRPC, and where the same server speaks HTTP")
			}
			target, err := url.Parse(connect)
			if err != nil {
				return fmt.Errorf("--connect: %w", err)
			}

			keys, err := keysFrom(cmd, "ROSTER_ACCOUNT_KEY_")
			if err != nil {
				return err
			}

			cfg := account.Config{
				Roster:  roster,
				Connect: target,
			}
			cfg.Insecure, _ = flg.Find[bool](cmd, "insecure")
			cfg.Keys = keys

			if v, _ := flg.Find[string](cmd, "base"); v != "" {
				cfg.Base, err = url.Parse(v)
				if err != nil {
					return fmt.Errorf("--base: %w", err)
				}
			}
			switch v, _ := flg.Find[string](cmd, "enrol"); v {
			case "", "invited":
				cfg.Enrol = account.Invited()
			case "enrolling":
				cfg.Enrol = account.Enrolling()
			default:
				return fmt.Errorf("--enrol: %q is not one of invited, enrolling", v)
			}
			if dir, _ := flg.Find[string](cmd, "static"); dir != "" {
				cfg.Static = http.FileServer(http.Dir(dir))
			}

			// The cookie is this app's, and the session is **in** it:
			// sealed under a key every replica holds, so there is no store
			// and no replica a browser is anonymous on. What the session
			// carries is roster's delegation for that person, which roster
			// ends -- a sign-out, "sign out everywhere", an operator -- so
			// nothing here has to be able to. See `authsession.Sealed` for
			// what a sealed session gives up, and `frontdoor` for how the
			// two forms and the sign-out are written for it.
			//
			// Without `--seal` the key is made here, at start: right for one
			// replica, and a restart signs everybody out, which is the safe
			// direction.
			sealed, err := sealFrom(cmd)
			if err != nil {
				return err
			}
			opts := []authsession.Option{}
			if v, _ := flg.Find[bool](cmd, "insecure-cookie"); v {
				opts = append(opts, authsession.Insecure())
			}
			cfg.Sessions = authsession.New(sealed, opts...)

			a, err := account.New(ctx, cfg)
			if err != nil {
				return err
			}
			defer a.Close()

			addr, _ := flg.Find[string](cmd, "listen")
			if addr == "" {
				addr = ":8090"
			}
			l, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			log.From(ctx).InfoContext(ctx, "account", slog.String("addr", l.Addr().String()),
				slog.Int("tenants", len(keys)))

			srv := &http.Server{Handler: a.Handler()}
			go func() {
				<-ctx.Done()
				_ = srv.Close()
			}()
			if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}

			return nil
		}),
	}
}

// sealFrom is the key sessions are sealed under, from `--seal env:NAME`, or
// one made now.
//
// `env:NAME` rather than the key itself, for the reason `--key` has an
// environment form: a key is a secret and a flag is in the process list. And
// through [account.EnvSecret], which is the one scheme this binary knows.
func sealFrom(cmd *xli.Command) (*authsession.Sealed, error) {
	vs, _ := flg.Find[[]string](cmd, "seal")
	if len(vs) == 0 {
		k := make([]byte, authsession.KeySize)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		log.From(context.Background()).Warn("account: sessions sealed under a key made at start; a second replica cannot open them, and a restart signs everybody out. --seal env:NAME names one to share")

		return authsession.NewSealed(k)
	}

	keys := make([][]byte, 0, len(vs))
	for _, ref := range vs {
		v, err := account.EnvSecret(ref)
		if err != nil {
			return nil, fmt.Errorf("--seal: %w", err)
		}
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("--seal %s: not base64: %w", ref, err)
		}
		keys = append(keys, k)
	}

	return authsession.NewSealed(keys...)
}

// keysFrom is one tenant key per operator, from `--key alias=token` and from
// `<prefix><ALIAS>` in the environment, the flag winning where both name one.
// `roster account serve` reads `ROSTER_ACCOUNT_KEY_`, `roster ldap serve`
// `ROSTER_LDAP_KEY_`: two processes, two sets of keys, and a shell that starts
// both from one environment does not hand either the other's.
//
// The environment form is there because a key is a secret and a process list
// is not where one belongs; the flag form is there because a shell that starts
// this for one tenant should not have to export anything.
func keysFrom(cmd *xli.Command, prefix string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		alias, ok := strings.CutPrefix(name, prefix)
		if !ok || alias == "" || value == "" {
			continue
		}
		out[strings.ToLower(alias)] = value
	}

	vs, _ := flg.Find[[]string](cmd, "key")
	for _, v := range vs {
		alias, token, ok := strings.Cut(v, "=")
		if !ok || alias == "" || token == "" {
			return nil, fmt.Errorf("--key %q: alias=token", v)
		}
		out[alias] = token
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--key alias=rt_… (or %s<ALIAS>): one tenant key per operator this fronts", prefix)
	}

	return out, nil
}
