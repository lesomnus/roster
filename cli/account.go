package cli

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
	"github.com/lesomnus/roster/cmd"
)

// NewCmdAccount is `roster account`: the front door a customer's people sign in
// at, as a process of its own.
//
// # Why it is a subcommand of this binary and not a binary of its own
//
// One thing to build, version and pin, and the same `rstr` clients the rest of
// this binary already carries.
//
// # Whether it is the same process as `roster serve`
//
// It was not, and that was written here as a rule: the account app holds tenant
// keys and faces the internet, and roster's own listeners -- the admin port most
// of all -- must not be in the process that does.
//
// The reasoning is still right and it is not this file's to enforce. It is an
// argument about **blast radius**, which is a thing a deployment weighs: worth
// the pods for one that has them, and a lot of ceremony for one that is three
// containers to run what is one binary and one database. So `account:` is a
// block now (`cmd/consumers.go`) and `roster serve` opens it when it is named;
// a deployment that wants the boundary leaves it out and runs this command.
//
// What the rule was actually protecting is untouched either way. In one process
// the account app still dials roster's own listener, so it is still a caller
// with a key, still walled, and still unable to import `internal`, `cmd` or
// `server` -- which is the part `scripts/test.sh` checks and the part that
// makes it proof rather than a promise.
//
// # It is told everything from the shell, and now also from the file
//
// This said "no `roster.yaml`", because reading the server's configuration file
// would be the first thing a consumer knows about the server that it should
// not. What it read is `account:` -- its own block, not `db:` -- and it does not
// read even that: `cli` does, and hands `account.Config` over. The package below
// still knows nothing about a file, which is where the line actually was.
//
// Flags win over the block, so a deployment that was passing them keeps working
// and a shell can still override one value of a file.
func NewCmdAccount(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "account",
		Brief: "the front door a customer's people sign in at",

		Commands: xli.Commands{newCmdAccountServe(c)},
	}
}

func newCmdAccountServe(c *cmd.Config) *xli.Command {
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

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			ctx, stop, err := telemetry(ctx, c, "roster-account")
			if err != nil {
				return err
			}
			defer stop()

			// The block first, then the flags over it. A deployment that never
			// wrote one passes what it always passed; a deployment that has one
			// can still say a value at the shell, which is what a flag is for.
			ac := c.Account
			if v, _ := flg.Find[string](cl, "listen"); v != "" {
				ac.Addr = v
			}
			if ac.Addr == "" {
				ac.Addr = ":8090"
			}
			if v, _ := flg.Find[string](cl, "roster"); v != "" {
				ac.Roster = v
			}
			if v, _ := flg.Find[string](cl, "connect"); v != "" {
				ac.Connect = v
			}
			if v, _ := flg.Find[bool](cl, "insecure"); v {
				ac.Insecure = true
			}
			if v, _ := flg.Find[string](cl, "base"); v != "" {
				ac.Base = v
			}
			if v, _ := flg.Find[string](cl, "static"); v != "" {
				ac.Static = v
			}
			if v, _ := flg.Find[string](cl, "enrol"); v != "" {
				ac.Enrol = v
			}
			if v, _ := flg.Find[bool](cl, "insecure-cookie"); v {
				ac.InsecureCookie = true
			}
			if vs, _ := flg.Find[[]string](cl, "seal"); len(vs) > 0 {
				ac.Seal = vs
			}

			// `--key` is literal and the block's values are references, so the
			// two are merged rather than one replacing the other. See [keysOf].
			given, _ := flg.Find[[]string](cl, "key")
			keys, err := keysOf(ac.Keys, AccountKeyPrefix, given)
			if err != nil {
				return err
			}
			ac.Keys = keys

			if ac.Roster == "" || ac.Connect == "" {
				return errors.New("--roster and --connect (or account.roster and account.connect): where roster speaks gRPC, and where the same server speaks HTTP")
			}

			return serveAccount(ctx, ac)
		}),
	}
}

// AccountKeyPrefix is the environment form of `--key`:
// `ROSTER_ACCOUNT_KEY_<ALIAS>`.
const AccountKeyPrefix = "ROSTER_ACCOUNT_KEY_"

// serveAccount stands the front door up and answers on it until ctx is done.
//
// Told a [cmd.AccountConfig] and nothing else, because it is called from two
// places -- the command above, and `roster serve` when `account:` names an
// address -- and the whole point of the block is that both build the same
// thing from the same values.
//
// It does not build telemetry. Both callers already have; there is one process
// either way and two would be two providers.
func serveAccount(ctx context.Context, ac cmd.AccountConfig) error {
	// Both names in every refusal below, for the reason `serveLdap` gives: the
	// value reached here from a block or from a flag and this cannot tell which.
	target, err := url.Parse(ac.Connect)
	if err != nil {
		return fmt.Errorf("account.connect (--connect): %w", err)
	}

	cfg := account.Config{
		Roster:   ac.Roster,
		Connect:  target,
		Insecure: ac.Insecure,
		Keys:     ac.Keys,
	}
	if ac.Base != "" {
		cfg.Base, err = url.Parse(ac.Base)
		if err != nil {
			return fmt.Errorf("account.base (--base): %w", err)
		}
	}
	switch ac.Enrol {
	case "", "invited":
		cfg.Enrol = account.Invited()
	case "enrolling":
		cfg.Enrol = account.Enrolling()
	default:
		return fmt.Errorf("account.enrol (--enrol): %q is not one of invited, enrolling", ac.Enrol)
	}
	if ac.Static != "" {
		cfg.Static = http.FileServer(http.Dir(ac.Static))
	}

	// The cookie is this app's, and the session is **in** it: sealed under a
	// key every replica holds, so there is no store and no replica a browser is
	// anonymous on. What the session carries is roster's delegation for that
	// person, which roster ends -- a sign-out, "sign out everywhere", an
	// operator -- so nothing here has to be able to. See `authsession.Sealed`
	// for what a sealed session gives up, and `frontdoor` for how the two forms
	// and the sign-out are written for it.
	//
	// With nothing named the key is made here, at start: right for one replica,
	// and a restart signs everybody out, which is the safe direction.
	sealed, err := sealOf(ac.Seal)
	if err != nil {
		return err
	}
	opts := []authsession.Option{}
	if ac.InsecureCookie {
		opts = append(opts, authsession.Insecure())
	}
	cfg.Sessions = authsession.New(sealed, opts...)

	a, err := account.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer a.Close()

	l, err := net.Listen("tcp", ac.Addr)
	if err != nil {
		return err
	}
	log.From(ctx).InfoContext(ctx, "account", slog.String("addr", l.Addr().String()),
		slog.Int("tenants", len(ac.Keys)))

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

// sealOf is the key sessions are sealed under, from `env:NAME` references, or
// one made now.
//
// `env:NAME` rather than the key itself, for the reason `--key` has an
// environment form: a key is a secret, a flag is in the process list and a
// configuration file is a file. Through [account.EnvSecret], which is the one
// scheme this binary knows.
func sealOf(refs []string) (*authsession.Sealed, error) {
	if len(refs) == 0 {
		k := make([]byte, authsession.KeySize)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		log.From(context.Background()).Warn("account: sessions sealed under a key made at start; a second replica cannot open them, and a restart signs everybody out. --seal env:NAME (or account.seal) names one to share")

		return authsession.NewSealed(k)
	}

	keys := make([][]byte, 0, len(refs))
	for _, ref := range refs {
		v, err := account.EnvSecret(ref)
		if err != nil {
			return nil, fmt.Errorf("seal: %w", err)
		}
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("seal %s: not base64: %w", ref, err)
		}
		keys = append(keys, k)
	}

	return authsession.NewSealed(keys...)
}

// keysOf is one tenant key per operator, from three places that are three
// different kinds of thing.
//
//   - the block, whose values are **references** (`env:NAME`), because a
//     configuration file is committed and a key is a secret;
//   - `<PREFIX><ALIAS>` in the environment, which is a token, and is the shape
//     a container or a compose file already has;
//   - `--key alias=token`, which is a token in the process list and is
//     documented as such.
//
// Merged in that order, so the more specific wins. Not one replacing another:
// a deployment with two operators in the file and a third being tried at the
// shell should get three, which is the thing this is for.
//
// The prefix is read here rather than by the loader because the loader maps one
// variable to one field and this is a variable per alias -- which is why
// `pdcmd.Reads` has to be told the prefix is not a typo (`cli.go`).
func keysOf(refs map[string]string, prefix string, given []string) (map[string]string, error) {
	out := map[string]string{}
	for alias, ref := range refs {
		v, err := account.EnvSecret(ref)
		if err != nil {
			return nil, fmt.Errorf("keys.%s: %w", alias, err)
		}
		out[strings.ToLower(alias)] = v
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
		out[strings.ToLower(alias)] = value
	}
	for _, v := range given {
		alias, token, ok := strings.Cut(v, "=")
		if !ok || alias == "" || token == "" {
			return nil, fmt.Errorf("--key %q: alias=token", v)
		}
		out[strings.ToLower(alias)] = token
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--key alias=rt_… (or %s<ALIAS>, or the `keys` block): one tenant key per operator this fronts", prefix)
	}

	return out, nil
}
