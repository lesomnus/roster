package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth/authsession"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/login"
)

// NewCmdLogin is `roster login`: the box Hydra hands a `login_challenge` to.
//
// A subcommand of this binary for the reason `roster account` and `roster ldap`
// are -- one thing to build and pin, the same `rstr` clients -- and a separate
// process for the same reason too: it holds tenant keys and faces the internet,
// and roster's own listeners must not be in the process that does. See
// `docs/operating.md`, "One process, or four", which says the same thing about
// all of them.
func NewCmdLogin(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "login",
		Brief: "the Login App, for a deployment with Hydra in front",

		Commands: xli.Commands{newCmdLoginServe(c)},
	}
}

// LoginKeyPrefix is the environment form of `--key`: `ROSTER_LOGIN_KEY_<ALIAS>`.
const LoginKeyPrefix = "ROSTER_LOGIN_KEY_"

func newCmdLoginServe(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "answer Hydra's login and consent challenges from roster's rows",

		Flags: flg.Flags{
			&flg.String{Name: "listen", Brief: "where to serve the sign-in page; :8091 if empty"},
			&flg.String{Name: "roster", Brief: "roster's data plane, gRPC: host:port"},
			&flg.Switch{Name: "insecure", Brief: "dial roster without TLS"},
			&flg.String{Name: "hydra", Brief: "Hydra's admin API, e.g. http://hydra:4445. Private: anybody who reaches it can sign anybody in as anybody"},
			&flg.Strings{Name: "key", Brief: "a tenant key, as alias=rt_…; repeat per operator fronted. Or " + LoginKeyPrefix + "<ALIAS> in the environment"},
			&flg.Strings{Name: "client", Brief: "which OAuth clients are an operator's, as alias=client-id[,client-id…]; repeat per operator"},
			&flg.String{Name: "consent", Brief: "what the consent hop does: skip (grant what the client asked for; the default) or ask (draw a screen)"},
			&flg.Strings{Name: "seal", Brief: "the key sessions are sealed under, as env:NAME; repeat to rotate"},
			&flg.Switch{Name: "insecure-cookie", Brief: "drop Secure from the cookies, for plain http in development"},
			&flg.String{Name: "static", Brief: "a sign-in page to serve instead of the one built in"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			ctx, stop, err := telemetry(ctx, c, "roster-login")
			if err != nil {
				return err
			}
			defer stop()

			// The block first, then the flags over it. See `cli/account.go`.
			lc := c.Login
			if v, _ := flg.Find[string](cl, "listen"); v != "" {
				lc.Addr = v
			}
			if v, _ := flg.Find[string](cl, "roster"); v != "" {
				lc.Roster = v
			}
			if v, _ := flg.Find[bool](cl, "insecure"); v {
				lc.Insecure = true
			}
			if v, _ := flg.Find[string](cl, "hydra"); v != "" {
				lc.Hydra.Admin = v
			}
			if vs, _ := flg.Find[[]string](cl, "seal"); len(vs) > 0 {
				lc.Seal = vs
			}
			if v, _ := flg.Find[bool](cl, "insecure-cookie"); v {
				lc.InsecureCookie = true
			}
			if v, _ := flg.Find[string](cl, "static"); v != "" {
				lc.Page = cmd.PageConfig{Dir: v}
			}
			if v, _ := flg.Find[string](cl, "consent"); v != "" {
				lc.Consent = v
			}

			given, _ := flg.Find[[]string](cl, "client")
			lc.Clients, err = clientsOf(lc.Clients, LoginClientPrefix, given)
			if err != nil {
				return err
			}

			lc.Keys, err = keysOf(lc.Keys, LoginKeyPrefix, mustFind[[]string](cl, "key"))
			if err != nil {
				return err
			}
			if err := whole(lc.Keys, lc.Clients); err != nil {
				return err
			}

			if lc.Roster == "" {
				return errors.New("--roster (or login.roster): where roster speaks gRPC")
			}
			if lc.Addr == "" {
				lc.Addr = ":8091"
			}

			return serveLogin(ctx, lc)
		}),
	}
}

func mustFind[T any](cl *xli.Command, name string) T {
	v, _ := flg.Find[T](cl, name)

	return v
}

// LoginClientPrefix is the environment form of `--client`.
const LoginClientPrefix = "ROSTER_LOGIN_CLIENT_"

// clientsOf is which OAuth client is whose: the block, the environment over it,
// and `--client` over that. [keysOf] with nothing secret about it, and it is
// not that function because an empty one is not an error here -- `whole` is
// what says an operator is half written, and it can say which half.
func clientsOf(refs map[string][]string, prefix string, given []string) (map[string][]string, error) {
	out := map[string][]string{}
	for alias, clients := range refs {
		out[strings.ToLower(alias)] = clients
	}
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		alias, ok := strings.CutPrefix(name, prefix)
		if !ok || alias == "" || value == "" {
			continue
		}
		out[strings.ToLower(alias)] = split(value)
	}
	for _, v := range given {
		alias, clients, ok := strings.Cut(v, "=")
		if !ok || alias == "" || clients == "" {
			return nil, fmt.Errorf("--client %q: alias=client-id[,client-id…]", v)
		}
		out[strings.ToLower(alias)] = split(clients)
	}

	return out, nil
}

// split is a comma list, which is how an operator with two products writes
// them where only one string will fit -- an environment variable, or a flag.
func split(v string) []string {
	out := []string{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}

	return out
}

// whole refuses an operator that is half written.
//
// Loudly and at start, because the failure is otherwise silent in the direction
// that matters: a key with no client is an operator no challenge ever resolves
// to, so their people reach a page that says the login is not working and
// nothing anywhere says why. This is what `cmd.LoginConfig.Clients` gives as
// the reason its two maps are two maps.
func whole(keys map[string]string, clients map[string][]string) error {
	for alias := range keys {
		if len(clients[alias]) == 0 {
			return fmt.Errorf("login.clients.%s (--client %s=…): a key with no OAuth client is an operator no challenge resolves to", alias, alias)
		}
	}
	for alias := range clients {
		if _, ok := keys[alias]; !ok {
			return fmt.Errorf("login.keys.%s (--key %s=rt_…, or %s%s): a client with no key is an operator this app cannot ask roster about", alias, alias, LoginKeyPrefix, strings.ToUpper(alias))
		}
	}

	return nil
}

// serveLogin answers login and consent challenges until ctx is done.
//
// Told a [cmd.LoginConfig] and nothing else, for the reason `serveAccount` is:
// it is called from the command above and from `roster serve` when `login:`
// names an address, and both have to build the same thing.
func serveLogin(ctx context.Context, lc cmd.LoginConfig) error {
	// Both names in every refusal, for the reason `serveLdap` gives: the value
	// reached here from a block or from a flag and this cannot tell which.
	if lc.Hydra.Admin == "" {
		return errors.New("login.hydra.admin (--hydra): where Hydra's admin API answers")
	}

	sealed, err := sealOf(lc.Seal)
	if err != nil {
		return err
	}
	opts := []authsession.Option{}
	if lc.InsecureCookie {
		opts = append(opts, authsession.Insecure())
	}

	how, err := login.ParseConsent(lc.Consent)
	if err != nil {
		return fmt.Errorf("login.consent (--consent): %w", err)
	}

	cfg := login.Config{
		Consent:        how,
		Roster:         lc.Roster,
		Insecure:       lc.Insecure,
		Hydra:          strings.TrimSuffix(lc.Hydra.Admin, "/"),
		Sessions:       authsession.New(sealed, opts...),
		Remember:       lc.Remember,
		InsecureCookie: lc.InsecureCookie,
		Operators:      map[string]login.Operator{},
	}
	for alias, key := range lc.Keys {
		cfg.Operators[alias] = login.Operator{Key: key, Clients: lc.Clients[alias]}
	}
	if len(lc.Hydra.Header) > 0 {
		cfg.HydraHeader = http.Header{}
		for k, v := range lc.Hydra.Header {
			cfg.HydraHeader.Set(k, v)
		}
	}
	if lc.Page.Dir != "" {
		cfg.Page = http.FileServer(http.Dir(lc.Page.Dir))
	}

	a, err := login.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer a.Close()

	l, err := net.Listen("tcp", lc.Addr)
	if err != nil {
		return err
	}
	log.From(ctx).InfoContext(ctx, "login", slog.String("addr", l.Addr().String()),
		slog.String("hydra", cfg.Hydra), slog.Int("operators", len(cfg.Operators)))

	srv := &http.Server{Handler: a.Handler()}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}
