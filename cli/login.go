package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/z"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth/authsession"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
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

		Commands: xli.Commands{newCmdLoginServe(c), newCmdLoginProvision(c)},
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
			&flg.String{Name: "base", Brief: "this app's public origin, registered with every provider as the redirect. One for the whole app"},
			&flg.String{Name: "enrol", Brief: "who a provider may sign in: invited (only somebody already linked), expected (somebody entered by address), enrolling (anybody)"},
			&flg.Strings{Name: "seal", Brief: "the key sessions are sealed under, as env:NAME; repeat to rotate"},
			&flg.Switch{Name: "insecure-cookie", Brief: "drop Secure from the cookies, for plain http in development"},
			&flg.String{Name: "static", Brief: "the built sign-in page (ts/dist/login)"},
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
			if v, _ := flg.Find[string](cl, "base"); v != "" {
				lc.Base = v
			}
			if v, _ := flg.Find[string](cl, "enrol"); v != "" {
				lc.Enrol = v
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

// newCmdLoginProvision is `roster login provision`: the credential this app
// needs, made where it will be used and living exactly as long.
//
// # The ordering it exists for
//
// `roster login serve` refuses to start without a tenant key, and a tenant key
// cannot exist before this deployment has run once: minting one takes a tenant,
// and a customer is made afterwards. So a deployment either does it by hand and
// keeps the answer in a Secret, or runs this beside the process -- in the same
// pod, on the same volume, before the server -- and points `login.keys` at the
// file it writes.
//
// The second is better than a Secret and not only shorter. The key never leaves
// the machine it was minted on, there is nothing to rotate because it is
// replaced every time this runs, and a pod that is gone takes its credential
// with it.
//
// # What it makes, and what it refuses to
//
// **Its own front door and nothing else.** For each operator named in
// `login.clients` it ensures the holder `login-app`, a role holding exactly
// what the app calls as itself, the binding between them, and a key -- then
// writes the key to a file. What it does **not** do is make a tenant: a
// customer is the operator's, and a command that made one by mentioning it
// would be a way to write rows into somebody else's by typo.
//
// A tenant that is not there is **skipped and said**, not refused. This runs
// beside the server on every start, and a fresh volume has no customers -- so a
// refusal here is a deployment that cannot come up until somebody has run
// something inside a pod that is not running. The Login App stays off until
// `login.addr` names it, which is the line that waits.
//
// # It replaces rather than adds
//
// A key's alias is unique per holder, and a key cannot be read back -- so a
// second run cannot reuse the first one's and must not leave it behind. The old
// row is erased and a new one written, which is why a restart is a rotation and
// why nothing accumulates.
func newCmdLoginProvision(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "provision",
		Brief: "mint this deployment's own Login App key into a file, for `login.keys: file:…`",

		Flags: flg.Flags{
			&flg.String{Name: "out", Brief: "the directory to write <alias>.key into; /run/roster-login if empty"},
			&flg.Strings{Name: "client", Brief: "which OAuth clients are an operator's, as alias=client-id[,…]; the `login.clients` block otherwise"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			given, _ := flg.Find[[]string](cl, "client")
			clients, err := clientsOf(c.Login.Clients, LoginClientPrefix, given)
			if err != nil {
				return err
			}
			if len(clients) == 0 {
				return errors.New("login.clients (--client alias=…): which operators this app fronts, and there are none")
			}

			out, _ := flg.Find[string](cl, "out")
			if out == "" {
				out = "/run/roster-login"
			}
			if err := os.MkdirAll(out, 0o700); err != nil {
				return err
			}

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// The schema, said the way `serve` says it. This runs **before**
			// the server on a fresh volume -- that is the whole point of it --
			// so the tables are not there yet, and a command that assumed they
			// were would fail on exactly the first boot it exists for.
			// `db.migrate: false` is a deployment saying this process may not
			// alter tables, and it is answered here rather than worked around.
			if err := ready(ctx, s, c.Db); err != nil {
				return err
			}

			// What the key is allowed, which is [LoginMethods] plus the one
			// grant a policy asks for. `enrolling` makes people, and making
			// people is wider than signing them in -- so it is granted only
			// where a deployment wrote `login.enrol: enrolling` down, and never
			// by default.
			methods := LoginMethods
			if c.Login.Enrol == "enrolling" {
				methods = append(append([]string{}, LoginMethods...), rstr.HolderService_Add_FullMethodName)
			}

			for alias := range clients {
				switch err := provision(ctx, s, alias, out, methods); {
				case err == nil:
				case errors.Is(err, errNoCustomer):
					// **Skipped and not refused**, which is the difference
					// between a command and a gate. This runs beside the
					// server on every start -- an init container, a line in a
					// unit -- and a fresh volume has no customers at all, so
					// refusing here would be a deployment that cannot come up
					// until somebody has run something inside a pod that is
					// not running. Said loudly, once per operator, and the
					// Login App stays off until `login.addr` names it.
					// To stderr rather than through `log`: this command
					// stands up no telemetry, and what it says is read in
					// `kubectl logs` of an init container.
					fmt.Fprintf(os.Stderr,
						"roster: %s: no such customer, so no key for it. `roster tenant add @%s` first;"+
							" the Login App stays off until it is there.\n", alias, alias)

				default:
					return fmt.Errorf("%s: %w", alias, err)
				}
			}

			return nil
		}),
	}
}

// LoginMethods is what the Login App calls as itself, and the whole of it.
//
// The password half is the flow: resolve the operator, check a secret, mint the
// delegation, end it. The provider half is the other way in -- read the
// operator's `Connection` rows to draw the buttons and to be the relying party,
// find the `Identity` a directory's answer names, link one for somebody the
// policy enrolled, and hand the claim over. `Sync.Watch` is how it hears that
// somebody has been signed out everywhere so that Hydra can be told to forget
// them. `login.Methods` is `Me.Get`, which is the claims that go in the token.
//
// `Email.Get` is the invitation: an operator who entered somebody in advance
// knows their address and not the subject a directory will assert, so the first
// sign-in is matched by the one and linked to the other.
//
// **`HolderService.Add` is not here, and that is the point of the list.** It is
// what `enrol: enrolling` needs, and making people is a wider grant than
// signing them in: a key that holds it can write a row into an operator's
// tenant for anybody a directory will vouch for. `provision` adds it when --
// and only when -- a deployment has written `login.enrol: enrolling` down, so
// the grant follows a line somebody typed rather than a default.
//
// `enrol: expected`, which admits the people an operator entered and nobody
// else, needs nothing beyond this list: it reads an `Email` row and writes an
// `Identity`.
//
// The app draws no account screens and reads nobody's rows but the person it is
// signing in, which is why this list is short and why it is written here rather
// than left to whoever runs the command.
var LoginMethods = append([]string{
	rstr.TenantService_Get_FullMethodName,
	rstr.VouchService_Verify_FullMethodName,
	rstr.VouchService_Delegate_FullMethodName,
	rstr.VouchService_Accept_FullMethodName,
	rstr.DelegationService_Revoke_FullMethodName,
	rstr.SyncService_Watch_FullMethodName,
	rstr.ConnectionService_Get_FullMethodName,
	rstr.ConnectionService_List_FullMethodName,
	rstr.IdentityService_Get_FullMethodName,
	rstr.IdentityService_Add_FullMethodName,
	rstr.EmailService_Get_FullMethodName,
}, login.Methods...)

// provision is one operator's front door.
func provision(ctx context.Context, s *cmd.Server, alias, out string, methods []string) error {
	tn, err := s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
		Ref:    rstr.TenantRef_builder{Alias: &alias}.Build(),
		Select: rstr.TenantSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Said rather than made. A customer is the operator's, and a
			// command that made one by mentioning it would be a way to write
			// rows into somebody else's tenant by typo.
			return errNoCustomer
		}

		return err
	}
	at := rstr.TenantRef_builder{Id: tn.GetId()}.Build()

	who, err := ensureHolder(ctx, s, at, alias)
	if err != nil {
		return err
	}
	role, err := ensureRole(ctx, s, at, methods)
	if err != nil {
		return err
	}
	if _, err := s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: role}.Build(),
		Holder: rstr.HolderRef_builder{Id: who}.Build(),
	}.Build()); err != nil && status.Code(err) != codes.AlreadyExists {
		return err
	}

	// Erased first: a key's alias is unique per holder and this one cannot be
	// read back, so the row from the last run is of no use to anybody and is
	// one more thing that would answer if it leaked.
	if v, err := s.Ungated.ApiKey().Get(ctx, rstr.ApiKeyGetRequest_builder{
		Ref: rstr.ApiKeyRef_builder{
			Slug: rstr.ApiKeyRefBySlug_builder{Holder: rstr.HolderRef_builder{Id: who}.Build(), Alias: z.Ptr(provisioned)}.Build(),
		}.Build(),
		Select: rstr.ApiKeySelect_builder{}.Build(),
	}.Build()); err == nil {
		if _, err := s.Ungated.ApiKey().Erase(ctx, rstr.ApiKeyRef_builder{Id: v.GetId()}.Build()); err != nil {
			return err
		}
	} else if status.Code(err) != codes.NotFound {
		return err
	}

	token, sum, err := keys.Mint(keys.PrefixTenant)
	if err != nil {
		return err
	}
	// **The same list the role got**, and that is the whole of the fix this
	// line once needed: a delegation is the intersection of what the key allows
	// and what the holder may do, so a key carrying the base list under a role
	// carrying the wider one allows the base list. The role had
	// `HolderService.Add` for `enrol: enrolling` and the key did not, so the
	// first person Entra vouched for reached the end of a whole sign-in and was
	// refused at the one write that makes them somebody.
	if _, err := s.Ungated.ApiKey().Add(ctx, rstr.ApiKeyAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: who}.Build(), Alias: provisioned,
		Secret: sum, Methods: methods,
	}.Build()); err != nil {
		return err
	}

	// `0600` and a directory this command made at `0700`: what is written is a
	// credential, and the only reader is the process beside it.
	path := filepath.Join(out, alias+".key")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return err
	}

	// `methods` and not `LoginMethods`: the count is the one that was written,
	// and a line reporting the base list while a policy widened it is a line
	// that says twelve on the run that granted thirteen.
	fmt.Fprintf(os.Stderr, "roster: %s: key for @%s/%s written to %s, allowing %d method(s).\n",
		alias, alias, provisioned, path, len(methods))

	return nil
}

// errNoCustomer is a tenant this deployment does not have. Not a failure: see
// where it is caught.
var errNoCustomer = errors.New("no such customer")

// provisioned is what this command's rows are called, so that a later run finds
// them and a person reading the console can tell them from somebody's.
const provisioned = "login-app"

func ensureHolder(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, alias string) ([]byte, error) {
	v, err := s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: provisioned}.Build())
	if err == nil {
		return v.GetId(), nil
	}
	if status.Code(err) != codes.AlreadyExists {
		return nil, err
	}

	got, err := s.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{
			Slug: rstr.HolderRefBySlug_builder{Alias: z.Ptr(provisioned), Tenant: at}.Build(),
		}.Build(),
		Select: rstr.HolderSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	return got.GetId(), nil
}

func ensureRole(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, methods []string) ([]byte, error) {
	// Patched when it is already there rather than left alone: the list above
	// grows with the app, and a role written by an older version is a Login App
	// that starts and then refuses one thing.
	v, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: at, Alias: provisioned, Methods: methods,
	}.Build())
	if err == nil {
		return v.GetId(), nil
	}
	if status.Code(err) != codes.AlreadyExists {
		return nil, err
	}

	got, err := s.Ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref: rstr.RoleRef_builder{
			Slug: rstr.RoleRefBySlug_builder{Alias: z.Ptr(provisioned), Tenant: at}.Build(),
		}.Build(),
		// `date_updated` because a patch is refused without the version it is
		// against -- which is the rule keeping two writers from each thinking
		// they wrote last.
		Select: rstr.RoleSelect_builder{DateUpdated: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if _, err := s.Ungated.Role().Patch(ctx, rstr.RolePatchRequest_builder{
		Ref:         rstr.RoleRef_builder{Id: got.GetId()}.Build(),
		Methods:     methods,
		DateUpdated: got.GetDateUpdated(),
	}.Build()); err != nil {
		return nil, err
	}

	return got.GetId(), nil
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

	sealed, err := sealOf("login", lc.Seal)
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

	var base *url.URL
	if lc.Base != "" {
		base, err = url.Parse(lc.Base)
		if err != nil {
			return fmt.Errorf("login.base (--base): %w", err)
		}
	}

	// The same two words the account app takes, and the same default. What is
	// **not** the same is what `enrolling` costs here: the key this deployment
	// mints for itself holds no `HolderService.Add`, so an operator asking for
	// it has a key of their own to mint. Said at start rather than at the first
	// stranger's sign-in.
	var enrol arrives.Enrol
	switch lc.Enrol {
	case "", "invited":
		enrol = arrives.Invited()
	case "expected":
		enrol = arrives.Expected()
	case "enrolling":
		enrol = arrives.Enrolling()
	default:
		return fmt.Errorf("login.enrol (--enrol): %q is not one of invited, expected, enrolling", lc.Enrol)
	}

	cfg := login.Config{
		Consent:        how,
		Roster:         lc.Roster,
		Insecure:       lc.Insecure,
		Hydra:          strings.TrimSuffix(lc.Hydra.Admin, "/"),
		Sessions:       authsession.New(sealed, opts...),
		Remember:       lc.Remember,
		InsecureCookie: lc.InsecureCookie,
		Base:           base,
		Enrol:          enrol,

		// `env:NAME`, roster's one vocabulary for a reference to a secret. The
		// same function the account app and the directory resolve theirs with,
		// and its refusals name no app for that reason.
		Secret:    account.EnvSecret,
		Operators: map[string]login.Operator{},
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

	// The sign-in and the sign-out, in one errgroup: an app that answers
	// challenges while roster cannot tell it who has been signed out is an app
	// serving a token it should not have. Whichever stops first stops the
	// other. `App.Watch` decides for itself what is worth stopping for --
	// a dropped stream is a reconnect, and a deployment with no broker is loud
	// and not fatal.
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return a.Watch(ctx) })
	g.Go(func() error {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}

		return nil
	})

	return g.Wait()
}
