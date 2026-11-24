package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth/authsession"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/mail"
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
//
// # A flag is the setting it says
//
// Each flag that says one of the block's settings is bound to it (`cfg.Bind`),
// so the load applies it -- over the file and the environment, and only when
// it is given -- and the block a handler copies already holds it. `--help`
// prints the variable beside it. The same goes for `roster ldap serve`,
// `roster login serve` and `roster scim serve`.
//
// They were copied over the block by hand after the load, and two rules differ
// from that copy, both the loader's for every layer: an empty value is a value,
// so `--listen=` clears what the file says rather than leaving it; and
// `--insecure=false` turns off a file's `true`, where a switch could only ever
// turn one on. What is still read by hand says why where it is read.
func NewCmdAccount(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "account",
		Brief: "the front door a customer's people sign in at",

		Commands: xli.Commands{newCmdAccountServe(l, c), newCmdAccountProvision(c)},

		Handler: xli.RequireSubcommand(),
	}
}

// newCmdAccountProvision is `roster account provision`: this app's own rows
// and keys, made where they are used -- `roster login provision` for the other
// front door, and `cli/provision.go` says what the four rows are and why they
// are ensured rather than written once.
//
// A process of its own reads what this writes:
//
//	account:
//	  deployment_key: ${file:/run/roster-account/account.key}
//
// Inside `roster serve` nothing runs this. `account.tenant_keys` left empty is the
// same rows and a key made at start, in memory, which is one replica.
func newCmdAccountProvision(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "provision",
		Brief: "mint the account app's deployment key into a directory, and nominate it in every tenant with a name, for `account.deployment_key: ${file:…}`",

		Flags: flg.Flags{
			&flg.String{Name: "out", Brief: "the directory to write account.key into; /run/roster-account if empty"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			out, _ := flg.Find[string](cl, "out")
			if out == "" {
				out = "/run/roster-account"
			}

			s, err := built(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// Both planes, the way `serve` says it: this runs beside the server
			// on a fresh volume, and `login provision` learned on an empty
			// cluster what a command that assumes the tables are there does on
			// the first boot it exists for.
			if err := Ready(ctx, s, *c); err != nil {
				return err
			}

			_, n, err := provisionAccount(ctx, s, c.Account.Enrol, out)
			if err != nil {
				return err
			}
			if n == 0 {
				// Said and not refused, for `login provision`'s reason: this
				// runs on every start, and a fresh volume having nobody to
				// front is not a deployment that should fail to come up.
				fmt.Fprintln(os.Stderr,
					"roster: no tenant has a `Host` row yet, so there is nobody to front."+
						" A tenant registers a name and this runs again on the next start.")
			}

			return nil
		}),
	}
}

func newCmdAccountServe(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "serve the sign-in page and the account screens, fronting roster",

		// Those in `cfg.Bind` are `account:`'s settings; see [NewCmdAccount].
		// `--tenant-key` is not one, and the handler says why.
		Flags: flg.Flags{
			cfg.Bind(l, &c.Account.Addr, &flg.String{Name: "listen", Brief: "where to listen; :8090 if empty"}),
			cfg.Bind(l, &c.Account.Roster, &flg.String{Name: "roster", Brief: "roster's data plane, gRPC: host:port"}),
			cfg.Bind(l, &c.Account.Connect, &flg.String{Name: "connect", Brief: "the same server over HTTP (server.http), for the page's calls: http(s)://host:port"}),
			cfg.Bind(l, &c.Account.Insecure, &flg.Switch{Name: "insecure", Brief: "dial roster without TLS"}),
			cfg.Bind(l, &c.Account.Base, &flg.String{Name: "base", Brief: "this app's public origin, registered with every provider as the redirect"}),
			cfg.Bind(l, &c.Account.Page.Dir, &flg.String{Name: "static", Brief: "a directory to serve as the page; empty serves none"}),
			cfg.Bind(l, &c.Account.Enrol, &flg.String{Name: "enrol", Brief: "who a provider may sign in: invited (only somebody already linked), expected (somebody entered by address), enrolling (anybody)"}),
			&flg.Strings{Name: "tenant-key", Brief: "a tenant key, as alias=rt_…; repeat per tenant fronted. Or " + AccountKeyPrefix + "<ALIAS> in the environment"},
			cfg.BindText(l, &c.Account.DeploymentKey, &flg.String{Name: "deployment-key", Brief: "one deployment key for every tenant that nominated it: rk_…, or one reference as the whole value, '${file:/run/key}' or '${env:NAME}' in single quotes; instead of --tenant-key"}),
			cfg.Bind(l, &c.Account.InsecureCookie, &flg.Switch{Name: "insecure-cookie", Brief: "a cookie without Secure, for a page served over plain http in development"}),
			cfg.Bind(l, &c.Account.Terminal, &flg.Switch{Name: "terminal", Brief: "let a machine with no browser ask for a key here (`roster sign-in`)"}),
			cfg.BindFunc(l, &c.Account.Seal, &flg.Strings{Name: "seal", Brief: "the key sessions are sealed into the cookie under, 32 bytes base64, or one reference to it as the whole value, '${env:NAME}' or '${file:/path}' in single quotes; repeat to rotate, the first seals. Empty is a key made at start, which is one replica"}, tokensOf),
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			ctx, stop, err := telemetry(ctx, c, "roster-account")
			if err != nil {
				return err
			}
			defer stop()

			// The block, with the flags bound above already in it. A
			// deployment that never wrote one passes what it always passed; a
			// deployment that has one can still say a value at the shell, which
			// is what a flag is for.
			ac := c.Account
			if ac.Addr == "" {
				ac.Addr = ":8090"
			}

			// `--tenant-key` is one per tenant and merged with the block's
			// rather than replacing them, so it is read here rather than
			// bound. See [keysOf].
			given, _ := flg.Find[[]string](cl, "tenant-key")
			if err := oneWay("account", ac.DeploymentKey, ac.TenantKeys, AccountKeyPrefix, given); err != nil {
				return err
			}

			var f appKeys
			if !ac.DeploymentKey.IsZero() {
				if f.key, err = ac.DeploymentKey.Value(); err != nil {
					return fmt.Errorf("--deployment-key (account.deployment_key): %w", err)
				}
			} else {
				if f.keys, err = keysOf(ac.TenantKeys, AccountKeyPrefix, given); err != nil {
					return err
				}
			}

			if ac.Roster == "" || ac.Connect == "" {
				return errors.New("--roster and --connect (or account.roster and account.connect): where roster speaks gRPC, and where the same server speaks HTTP")
			}

			return serveAccount(ctx, ac, f)
		}),
	}
}

// AccountKeyPrefix is the environment form of `--tenant-key`:
// `ROSTER_ACCOUNT_TENANT_KEY_<ALIAS>`.
const AccountKeyPrefix = "ROSTER_ACCOUNT_TENANT_KEY_"

// appKeys is the key an app fronts its tenants with, read: one deployment
// key, or one tenant key per tenant by alias. The tokens, and not where they
// are kept, because they are read once, where the app is built.
type appKeys struct {
	key  string
	keys map[string]string
}

// none is whether there is no key to front with.
func (f appKeys) none() bool { return f.key == "" && len(f.keys) == 0 }

// serveAccount stands the front door up and answers on it until ctx is done.
//
// Told a [cmd.AccountConfig] and the keys read from it, and nothing else,
// because it is called from two places -- the command above, and `roster
// serve` when `account:` names an address -- and the whole point of the block
// is that both build the same thing from the same values.
//
// It does not build telemetry. Both callers already have; there is one process
// either way and two would be two providers.
func serveAccount(ctx context.Context, ac cmd.AccountConfig, f appKeys) error {
	// What has a form, held to it before anything is dialled; `roster
	// serve` asks the same in `cmd.Build`.
	if err := ac.Meant(); err != nil {
		return err
	}

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
		Keys:     f.keys,
		Key:      f.key,

		// The same setting the session store is built with below, because this
		// app sets one cookie the store does not: a provider flow's state.
		InsecureCookie: ac.InsecureCookie,
		Terminal:       ac.Terminal,
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
	case "expected":
		cfg.Enrol = account.Expected()
	case "enrolling":
		cfg.Enrol = account.Enrolling()
	default:
		return fmt.Errorf("account.enrol (--enrol): %q is not one of invited, expected, enrolling", ac.Enrol)
	}
	if ac.Page.Dir != "" {
		cfg.Static = cmd.Page(ac.Page.Dir)
	}
	if cfg.Mail, err = mailOf(ac.Mail); err != nil {
		return err
	}

	// Which hosts off the internet a provider may be at, read at start: a
	// name that is not one is this file's mistake, and somebody's sign-in is
	// a poor place to learn of it.
	if cfg.PrivateHosts, err = arrives.ParsePrivateHosts(ac.PrivateHosts); err != nil {
		return fmt.Errorf("account.private_hosts: %w", err)
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
	sealed, err := sealOf("account", ac.Seal)
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

	l, err := cmd.Listen(ctx, ac.Addr)
	if err != nil {
		return err
	}
	log.From(ctx).InfoContext(ctx, "account", slog.String("addr", l.Addr().String()),
		slog.Int("tenants", a.Fronts()))

	// With clocks on what a stranger controls: how long a request's headers
	// may take to arrive, and how long a connection may sit idle. The server
	// had none, and this is the listener that faces the internet -- a
	// connection opened and left half way through a header was kept for ever.
	// Nothing bounds the answer, which for the page's road to roster may be a
	// stream.
	srv := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}

	// Stopped as roster's own listeners stop: a request in flight is answered,
	// under the one grace from the signal.
	return cmd.ServeUntil(ctx, srv, l)
}

// mailOf is `account.mail` as the account app's deliverer, or nil where it
// names no server -- a front door that cannot send, which says so.
//
// roster mints the links and this sends them: the front door's process, never
// a server of roster's, which is D19's line. Refused at start rather than at
// the first message, so that a deployment learns it when it starts: a
// reference that names nothing, a sender that is not an address, the clear to
// a host nobody named.
func mailOf(c cmd.MailConfig) (func(ctx context.Context, to, subject, link string) error, error) {
	if !c.On() {
		return nil, nil
	}

	mc, err := mailConfigOf(c)
	if err != nil {
		return nil, err
	}
	s, err := mail.New(mc)
	if err != nil {
		return nil, fmt.Errorf("account.mail.%w", err)
	}

	return func(ctx context.Context, to, subject, link string) error {
		return s.Send(ctx, to, subject, account.Letter(subject, link))
	}, nil
}

// mailConfigOf is the block with its references read.
func mailConfigOf(c cmd.MailConfig) (mail.Config, error) {
	username, err := c.Username.Value()
	if err != nil {
		return mail.Config{}, fmt.Errorf("account.mail.username: %w", err)
	}
	password, err := c.Password.Value()
	if err != nil {
		return mail.Config{}, fmt.Errorf("account.mail.password: %w", err)
	}

	return mail.Config{
		Addr:           c.Addr,
		Username:       username,
		Password:       password,
		From:           c.From,
		TLS:            mail.TLS(strings.ToLower(c.Tls)),
		PlaintextHosts: c.PlaintextHosts,
		Timeout:        c.Timeout,
	}, nil
}

// tokensOf is `--seal`, repeated, as the field it sets: each value the key
// itself or one reference as the whole value, read as the environment gives
// one. `--seal=` is none, whatever the file named.
func tokensOf(vs []string) ([]cmd.Token, error) {
	if len(vs) == 1 && vs[0] == "" {
		return nil, nil
	}

	out := make([]cmd.Token, len(vs))
	for i, v := range vs {
		if err := out[i].UnmarshalText([]byte(v)); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// sealOf is the key sessions are sealed under, from `seal`, or one made now.
//
// `who` is the block that names it, because two apps call this and an operator
// reading *account.seal* about the Login App has been told the wrong setting to
// go and change.
func sealOf(who string, seals []cmd.Token) (*authsession.Sealed, error) {
	if len(seals) == 0 {
		k := make([]byte, authsession.KeySize)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		log.From(context.Background()).Warn(fmt.Sprintf(
			"%s: sessions sealed under a key made at start; a second replica cannot open them, and a restart signs everybody out. %s.seal (or --seal) names one to share",
			who, who))

		return authsession.NewSealed(k)
	}

	keys := make([][]byte, 0, len(seals))
	for i, seal := range seals {
		v, err := seal.Value()
		if err != nil {
			return nil, fmt.Errorf("%s.seal[%d]: %w", who, i, err)
		}
		// Not said back, nor what of it failed to decode: it is the key.
		k, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("%s.seal[%d]: not base64; it is not repeated, since it is the key", who, i)
		}
		keys = append(keys, k)
	}

	return authsession.NewSealed(keys...)
}

// oneWay refuses a deployment key beside tenant keys, from the block, the
// flag or the environment: one way to front tenants (#76).
//
// Here, where the block and the flags are read and before the app is built,
// so that it is said in the file's words and the flags'. The app's own
// library refuses the two as well, in its Go names, for a caller in Go; a
// `--tenant-key` beside `--deployment-key` was not refused at all, but
// dropped. `block` is `account` or `ldap`.
func oneWay(block string, deployment cmd.Token, tenants map[string]cmd.Token, prefix string, given []string) error {
	if deployment.IsZero() {
		return nil
	}

	where := ""
	if len(tenants) == 0 && len(given) == 0 {
		for _, kv := range os.Environ() {
			name, value, _ := strings.Cut(kv, "=")
			if alias, ok := strings.CutPrefix(name, prefix); ok && alias != "" && value != "" {
				where = ", given here by " + prefix + "<ALIAS> in the environment"
				break
			}
		}
		if where == "" {
			return nil
		}
	}

	return fmt.Errorf("%[1]s.deployment_key (--deployment-key) and %[1]s.tenant_keys (--tenant-key)%[2]s: one way to front tenants, not two", block, where)
}

// errNoKeys is [keysOf] finding nothing in any of the three places: a refusal
// for a process of its own, and for `roster serve` the cue to make them.
var errNoKeys = errors.New("no tenant key")

// keysOf is one tenant key per tenant, from three places.
//
//   - the block, each a [cmd.Token] the loader has read, written where it is
//     kept because a configuration file is committed and a key is a secret;
//   - `<PREFIX><ALIAS>` in the environment, which is the key, and is the
//     shape a container or a compose file already has;
//   - `--tenant-key alias=token`, which is the key in the process list and
//     is documented as such.
//
// Merged in that order, so the more specific wins. Not one replacing another:
// a deployment with two operators in the file and a third being tried at the
// shell should get three, which is the thing this is for.
//
// The prefix is read here rather than by the loader because the loader maps one
// variable to one field and this is a variable per alias -- which is why
// `cfg.Reads` has to be told the prefix is not a typo (`cli.go`).
func keysOf(block map[string]cmd.Token, prefix string, given []string) (map[string]string, error) {
	out := map[string]string{}
	for alias, t := range block {
		v, err := t.Value()
		if err != nil {
			return nil, fmt.Errorf("tenant_keys.%s: %w", alias, err)
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
			// Not said back: what is given here is a token.
			return nil, invocation(errors.New("--tenant-key: a key is given as alias=token, and one is not; it is not said back, " +
				"since what is given there is the token"))
		}
		out[strings.ToLower(alias)] = token
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: --tenant-key alias=rt_… (or %s<ALIAS>, or tenant_keys in the block): one tenant key per tenant this fronts", errNoKeys, prefix)
	}

	return out, nil
}
