package cli

import (
	"bytes"
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
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
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

		Commands: xli.Commands{newCmdLoginServe(c), newCmdLoginProvision(c), newCmdLoginDoctor(c)},
	}
}

// LoginKeyPrefix was the environment form of `--key`, one variable per tenant:
// `ROSTER_LOGIN_KEY_<ALIAS>`.
//
// It is gone with the map. This app holds one credential, so the key is one
// setting and the loader reads it like any other -- `ROSTER_LOGIN_KEY`, with no
// prefix for anything to be told is not a typo (`cli.go`). Said here rather than
// deleted silently, because a deployment reading `ROSTER_LOGIN_KEY_CONTOSO` out
// of its own manifests needs to find out what replaced it.

func newCmdLoginServe(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "answer Hydra's login and consent challenges from roster's rows",

		Flags: flg.Flags{
			&flg.String{Name: "listen", Brief: "where to serve the sign-in page; :8091 if empty"},
			&flg.String{Name: "roster", Brief: "roster's data plane, gRPC: host:port"},
			&flg.Switch{Name: "insecure", Brief: "dial roster without TLS"},
			&flg.String{Name: "hydra", Brief: "Hydra's admin API, e.g. http://hydra:4445. Private: anybody who reaches it can sign anybody in as anybody"},
			&flg.String{Name: "key", Brief: "this app's one deployment key (rk_…), or a reference to it: env:NAME, file:PATH"},
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

			if v, _ := flg.Find[string](cl, "key"); v != "" {
				lc.Key = v
			}
			// A reference or the token itself, which is what `secretRef` decides:
			// a configuration file is committed and a key is a secret, so the
			// file says `env:NAME` and a flag may say the thing.
			//
			// One key rather than one per tenant, and that removes the whole of
			// `minted`: the file a per-tenant key lived in could be absent on a
			// first start -- a fresh volume has no customers, so
			// `roster login provision` skipped them and the server then would not
			// come up, so the customer could never be made. An `rk_` is the
			// **control plane's** and needs no customer to exist, so it can be
			// minted on an empty deployment and there is no cycle to break.
			if lc.Key == "" {
				return errors.New("--key (or login.key): this app's one deployment key")
			}
			if lc.Key, err = tokenOrRef(lc.Key); err != nil {
				return fmt.Errorf("login.key: %w", err)
			}
			// A **deployment** key, and an `rt_` is refused at start rather
			// than at somebody's first sign-in.
			//
			// It would half work, which is the worst shape: an `rt_` resolves to
			// a holder in one tenant already, so `roster-at` is refused for it
			// (`server/keys/at.go`) and every flow reaches a page saying the
			// login is not working -- for every customer, including the one whose
			// key it is. Here rather than in `login.New`, because the prefix is a
			// fact `server/keys` owns and that app may import no server package
			// but `server/front`.
			if !strings.HasPrefix(lc.Key, keys.PrefixDeployment) {
				return fmt.Errorf("login.key (--key): a deployment key (%s…), and this is not one",
					keys.PrefixDeployment)
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
// **Its own front door and nothing else.** For each tenant named in
// `login.clients` it ensures the holder `login-app`, a role holding exactly
// what the app calls as itself, the binding between them, and a key -- then
// writes the key to a file. What it does **not** do is make a tenant: a
// customer is the tenant's, and a command that made one by mentioning it
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
			&flg.String{Name: "out", Brief: "the directory to write login.key into; /run/roster-login if empty"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
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

			// **And the control plane's**, which this needs since #36 and did
			// not before: the key is an `rk_` on a control-plane holder, where it
			// was an `rt_` inside each tenant. `roster control key add` says the
			// same thing one file over -- *the control database may be new: this
			// is often the first thing that writes to it* -- and it is truer
			// here, because this is an init container and runs before anything
			// else has opened that database at all.
			//
			// Left out, the first start of a fresh deployment fails in the init
			// container on `no such table: tenant`, the pod never becomes ready,
			// and what the rig reports is `timed out waiting for the condition`
			// about a Deployment. Which is how this was found.
			if s.Control != nil {
				if err := ready(ctx, s.Control, c.Control.Db); err != nil {
					return err
				}
			}

			// What the key is allowed, which is [LoginMethods] plus the one
			// grant a policy asks for. `enrolling` makes people, and making
			// people is wider than signing them in -- so it is granted only
			// where a deployment wrote `login.enrol: enrolling` down, and never
			// by default.
			methods := loginMethodsFor(c.Login.Enrol)

			// The key first, because it is the half that needs no customer.
			//
			// An `rk_` is the **control plane's**, so a fresh volume with no
			// tenants in it can still be given one -- which is what closed the
			// cycle the per-tenant keys had: their files were absent until a
			// customer existed, the server would not start without the files, and
			// the customer could not be made without the server. `deploy/` hit
			// exactly that on its first run against an empty cluster.
			_, borrower, err := provisionKey(ctx, s, out)
			if err != nil {
				return err
			}

			// And a nomination per name, which is the per-customer half.
			//
			// Walked off the `Host` rows and not off a list: a tenant that
			// registered a name is a tenant this app fronts (#42), so what used
			// to be `login.clients` is a query. A deployment with no names yet
			// is **said and not refused**, for the reason the skip above was:
			// this runs beside the server on every start, and a fresh volume
			// having nothing to nominate is not a deployment that should fail to
			// come up.
			//
			// To stderr rather than through `log`: this command stands up no
			// telemetry, and what it says is read in `kubectl logs` of an init
			// container.
			n, err := nominate(ctx, s, methods, borrower)
			if err != nil {
				return err
			}
			if n == 0 {
				fmt.Fprintln(os.Stderr,
					"roster: no tenant has a `Host` row yet, so there is nobody to front."+
						" A tenant registers a name and this runs again on the next start.")
			}

			return nil
		}),
	}
}

// newCmdLoginDoctor is the check a deployment runs before anybody clicks
// anything, and `login/doctor.go` says at length what it is for.
//
// It exits **1** when something is broken, so a Job that runs it fails rather
// than logging into a stream nobody reads. Fragile findings are printed and do
// not fail: they are things that work, and a deployment that has decided to
// live with one should not have a red sync forever.
func newCmdLoginDoctor(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "doctor",
		Brief: "ask hydra whether the clients this app fronts are registered in a way this stack works with",

		Flags: flg.Flags{
			&flg.String{Name: "hydra", Brief: "where hydra's admin API answers; the `login.hydra.admin` block otherwise"},
			&flg.String{Name: "public", Brief: "where hydra's public endpoints answer, for the half about what hydra was told; derived from --hydra otherwise"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			at, _ := flg.Find[string](cl, "hydra")
			if at == "" {
				at = c.Login.Hydra.Admin
			}
			if at == "" {
				return errors.New("login.hydra.admin (--hydra): where hydra's admin API answers")
			}

			// What Hydra was told is only answerable on its **public** port,
			// and roster has no field that says where that is -- the Login App
			// needs the admin API and nothing else. Rather than add a setting
			// for a check, this derives it from the address it already has,
			// which is `4445` -> `4444` on a Hydra left at its defaults. A
			// deployment that moved them passes `--public`, and one that does
			// neither is **told** the checks were skipped rather than left to
			// read a pass that did not happen.
			public, _ := flg.Find[string](cl, "public")
			if public == "" {
				public = strings.Replace(at, ":4445", ":4444", 1)
			}

			// Which tenant a client's redirects resolve to, which is roster's
			// answer and not Hydra's -- so this check needs the database and
			// every other one here does not.
			//
			// Local, through the server this process can open: `doctor` is a
			// command run beside a deployment, the same place `init` and
			// `key add` are, and asking over the wire would need a credential
			// for a question about rows this shell already reaches. A run that
			// cannot open it is **told** the check was skipped rather than left
			// to read a pass that did not happen, which is `--public`'s
			// arrangement one field over.
			var whose login.Whose
			if s, err := cmd.Build(ctx, *c); err == nil {
				defer s.Close()
				whose = func(ctx context.Context, host string) (string, error) {
					res, err := front.New(s.Ungated).WhoseHost(ctx, rstr.FrontWhoseHostRequest_builder{Host: host}.Build())
					if err != nil {
						return "", err
					}
					v, err := s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
						Ref:    rstr.TenantRef_builder{Id: res.GetTenant()}.Build(),
						Select: rstr.TenantSelect_builder{Alias: z.Ptr(true)}.Build(),
					}.Build())
					if err != nil {
						return "", err
					}

					return v.GetAlias(), nil
				}
			}

			found, err := login.Doctor(ctx, at, public, nil, whose)
			if err != nil {
				return err
			}

			broken := 0
			for _, f := range found {
				if f.Severity == login.Broken {
					broken++
				}
				fmt.Fprintln(os.Stdout, f)
			}

			if len(found) == 0 {
				fmt.Fprintln(os.Stdout, "ok: nothing this can see is wrong")

				return nil
			}
			if broken == 0 {
				fmt.Fprintf(os.Stdout, "nothing broken, %d thing(s) to know about\n", len(found))

				return nil
			}

			return fmt.Errorf("%d thing(s) here cannot sign anybody in", broken)
		}),
	}
}

// LoginMethods is what the Login App calls as itself, and the whole of it.
//
// The password half is the flow: resolve the tenant, check a secret, mint the
// delegation, end it. The provider half is the other way in -- read the
// tenant's `Connection` rows to draw the buttons and to be the relying party,
// find the `Identity` a directory's answer names, link one for somebody the
// policy enrolled, and hand the claim over. `Sync.Watch` is how it hears that
// somebody has been signed out everywhere so that Hydra can be told to forget
// them. `login.Methods` is `Me.Get`, which is the claims that go in the token.
//
// `Email.Get` is the invitation: an tenant who entered somebody in advance
// knows their address and not the subject a directory will assert, so the first
// sign-in is matched by the one and linked to the other. `Email.Attest` is the
// other direction -- the address the directory itself handed over, kept with
// the identity that vouched for it.
//
// **`HolderService.Add` is not here, and that is the point of the list.** It is
// what `enrol: enrolling` needs, and making people is a wider grant than
// signing them in: a key that holds it can write a row into an tenant's
// tenant for anybody a directory will vouch for. `provision` adds it when --
// and only when -- a deployment has written `login.enrol: enrolling` down, so
// the grant follows a line somebody typed rather than a default.
//
// `enrol: expected`, which admits the people an tenant entered and nobody
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

	// The address a directory hands over, written down on that directory's
	// word. Without it a person who arrived through one has an account roster
	// cannot say the address of, and the token every product reads is missing
	// the claim it asked for -- with no other route to fix it in a deployment
	// that cannot send mail.
	rstr.EmailService_Attest_FullMethodName,

	// What a tenant's profile setting fills, where a profile has nothing
	// (`TenantProfile`). Held always, because whether to fill is each tenant's
	// to say and this app fronts all of them -- and holding it costs nothing a
	// tenant did not decide: it writes only blanks, reads nothing back, and is
	// called only for a tenant that said `fill`.
	rstr.HolderService_Fill_FullMethodName,
}, login.Methods...)

// LoginResolving is what this app calls **before** it knows whose flow it is,
// and the whole of what its key allows.
//
// Three reads, and the list is short because of where the other bound is.
// `keys.At` answers a request carrying `roster-at` as the nominated holder with
// `frame.Whole()` -- the key's own method list is **not** carried through -- so
// what bounds every call inside a flow is that holder's role, which `nominate`
// writes with [LoginMethods]. What this list bounds is the calls that cannot be
// narrowed that way, because they are the calls that work out what the
// narrowing is:
//
//	FrontService.WhoseHost   which tenant claims the name a flow named
//	TenantService.Get        what that tenant is called, for the screen
//	SyncService.Watch        every tenant's sign-outs, on one stream
//
// A key with no methods at all was the first draft, on the reading that the
// policy hands an `rk_` `frame.Everything` and a list would be a narrowing
// nothing reads. Half right: `policy.Where` does answer `frame.Everything`, and
// its own comment says what the other half is -- *what narrows it is its
// methods*. An empty list is a key that may call nothing, which is what the
// tests said in as many words.
var LoginResolving = []string{
	rstr.FrontService_WhoseHost_FullMethodName,
	rstr.TenantService_Get_FullMethodName,
	rstr.SyncService_Watch_FullMethodName,
}

// provisionKey mints the one credential this app holds, into a file.
//
// A **deployment** key, on a control-plane holder, which is the change #36 is:
// it was one `rt_` per tenant on a holder inside each, and what separated
// customers was which key was picked. A `Nomination` in each tenant separates
// them now, per request, so the credential is one and the nomination is what
// narrows it.
//
// Replaced rather than added to, for the reason the per-tenant version was: a
// key's alias is unique per holder and cannot be read back, so the row from the
// last run is of no use to anybody and is one more thing that would answer if it
// leaked. A restart is a rotation.
func provisionKey(ctx context.Context, s *cmd.Server, out string) (string, pdid.Id, error) {
	return provisionDeploymentKey(ctx, s, provisioned, LoginResolving, out)
}

// provisionDeploymentKey mints the one deployment key a roster-hosted app holds,
// on a control-plane holder called `alias`, allowed `methods` as itself, into
// `<out>/<alias>.key` -- or into memory alone when `out` is empty. It answers the
// token and the holder, which is what that app's nominations are found by.
//
// The Login App's and the account app's, which are the same act for two apps
// (#76): one credential, and what it may do unnarrowed is only what it calls
// before a request names a tenant.
func provisionDeploymentKey(ctx context.Context, s *cmd.Server, alias string, methods []string, out string) (string, pdid.Id, error) {
	if s.Control == nil {
		return "", pdid.Nil, errors.New("this app's key is a deployment key, and there is no control plane to hold it")
	}

	who, err := cmd.HolderNamed(ctx, s.Control, alias)
	if err != nil {
		return "", pdid.Nil, err
	}

	// [LoginResolving] and not [LoginMethods], which is the split that matters:
	// what a call inside a flow may do is the nominated holder's role, and what
	// this key may do is the three reads that decide which holder that is.
	token, err := mintNamed(ctx, s.Control.Ungated, who.Bytes(), alias, methods, keys.PrefixDeployment)
	if err != nil {
		return "", pdid.Nil, err
	}

	// Into memory alone when nothing names a directory, which is `roster serve`
	// making the key at start; `cli/provision.go` says what that is worth.
	if out == "" {
		return token, who, nil
	}

	path := filepath.Join(out, alias+".key")
	if err := writeKey(path, token); err != nil {
		return "", pdid.Nil, err
	}

	fmt.Fprintf(os.Stderr, "roster: the deployment key for @%s written to %s.\n", alias, path)

	return token, who, nil
}

// loginMethodsFor is what the nominated holder's role allows: [LoginMethods],
// plus the one grant a policy asks for. `enrolling` makes people, and making
// people is wider than signing them in -- so it is granted only where a
// deployment wrote `login.enrol: enrolling` down, and never by default.
func loginMethodsFor(enrol string) []string {
	return policyMethods(LoginMethods, enrol)
}

// policyMethods is a front door's own list and what its enrolment policy adds
// to it -- the Login App's and the account app's alike, since the policy is the
// same word in both.
func policyMethods(own []string, enrol string) []string {
	vs := append([]string{}, own...)
	if enrol == "enrolling" {
		vs = append(vs, rstr.HolderService_Add_FullMethodName)
	}

	return vs
}

// nominate is the per-customer half: in every tenant with a name, a holder the
// app is answered as, its role, and the [rstr.Nomination] that says so for this
// app's key.
//
// It answers with how many tenants it nominated in, because none is a state
// worth saying out loud rather than a failure.
//
// # Walked off the names and not off a list
//
// A tenant that registered a `Host` row is a tenant this app fronts, so *who do
// we front* is a query rather than `login.clients`. That is what #42 made
// possible: the row is theirs to write, so nothing here has to be told about a
// customer by a roster operator.
//
// # Once per tenant, and found by the key's holder
//
// This walked the names and wrote `Host.acts_as` on each, which made two things
// wrong at once. A tenant with eight names had its binding added eight times on
// every start. And a name could nominate only one app, so this overwrote
// whatever any other roster-hosted app had put there -- a product served at the
// very name the Login App narrows to for that product's sign-in.
//
// The nomination is per tenant and per app now (`nomination.proto`): `borrower`
// is the control-plane holder this app's key hangs off, and the row says who
// that key is answered as here. The names only decide which tenants there are.
//
// # Nominating is a way in, and this is the deployment's own work
//
// A narrowed key is answered with the nominated holder's bindings and not its
// own methods, so `server/core` holds a nomination to `mayWriteAWayIn`. This
// goes through the unwalled server with no frame, which is the deployment's own
// work and passes, and the holder it nominates is the one it just made.
func nominate(ctx context.Context, s *cmd.Server, methods []string, borrower pdid.Id) (int, error) {
	return nominateAs(ctx, s, provisioned, methods, borrower, "", "config: login")
}

// nominateAs is [nominate] for the roster-hosted app called `alias`: a holder of
// that name in each tenant with a name, its role, one binding, and the
// nomination for `borrower`.
func nominateAs(ctx context.Context, s *cmd.Server, alias string, methods []string, borrower pdid.Id, legacy, declaredBy string) (int, error) {
	// Every row below is the configuration's: it is written because the
	// deployment turned this front door on, and rewritten at every start. So
	// each carries the label that makes it read-only to anybody reaching it
	// through a port (`server/core/declared.go`) -- a tenant ending the Login
	// App's nomination was everybody's sign-in stopping until the next deploy.
	labels := map[string]string{cmd.Declared: declaredBy}

	tenants, err := tenantsWithNames(ctx, s)
	if err != nil {
		return 0, err
	}

	n := 0
	for _, t := range tenants {
		at := rstr.TenantRef_builder{Id: t.Bytes()}.Build()

		tn, err := s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
			Ref:    at,
			Select: rstr.TenantSelect_builder{Alias: z.Ptr(true)}.Build(),
		}.Build())
		if err != nil {
			return n, err
		}
		name := tn.GetAlias()

		// A row of the same name that is somebody else's is said and the
		// tenant skipped, rather than failing the run: this is an init
		// container, and one that fails is every tenant's sign-in down because
		// one tenant named somebody `login-app`.
		skip := func(why string) {
			fmt.Fprintf(os.Stderr, "roster: %s: not answering @%s's key as @%s/%s: %s.\n", name, alias, name, alias, why)
		}

		who, made, err := holderNamed(ctx, s, at, alias, labels)
		if err != nil {
			return n, fmt.Errorf("%s: %w", name, err)
		}
		existing, err := roleNamed(ctx, s, at, alias)
		if err != nil {
			return n, fmt.Errorf("%s: %w", name, err)
		}
		if !made {
			h, err := holdingOf(ctx, s, who, existing)
			if err != nil {
				return n, fmt.Errorf("%s: %w", name, err)
			}
			if h.wayIn() || len(h.roles) > 0 {
				skip(fmt.Sprintf("@%s/%s is already somebody's -- it holds %s -- and this would answer the app as them", name, alias, h))
				continue
			}
		}
		if existing != nil {
			others, err := boundToOthers(ctx, s, existing, who)
			if err != nil {
				return n, fmt.Errorf("%s: %w", name, err)
			}
			if others {
				skip(fmt.Sprintf("the tenant's role %q is bound to others, and rewriting it would hand them the app's methods", alias))
				continue
			}
		}
		role, err := ensureRoleNamed(ctx, s, at, alias, methods, labels)
		if err != nil {
			return n, fmt.Errorf("%s: %w", name, err)
		}
		if err := ensureBinding(ctx, s, role, who, labels); err != nil {
			return n, fmt.Errorf("%s: %w", name, err)
		}
		if legacy != "" {
			// The key this app held in the tenant before it held one deployment
			// key (#76). Its token was made at start and thrown away, but the
			// row still opens the holder's role to anybody who kept one -- an
			// `account provision --out` file, a compose volume -- and nothing
			// rotates it any more.
			if err := eraseKeyNamed(ctx, s.Ungated, who, legacy); err != nil {
				return n, fmt.Errorf("%s: the key it held before: %w", name, err)
			}
		}

		changed, err := ensureNominated(ctx, s, at, borrower, who, alias, labels)
		if err != nil {
			return n, fmt.Errorf("%s: %w", name, err)
		}
		if changed {
			fmt.Fprintf(os.Stderr, "roster: %s: @%s's key is answered as @%s/%s, allowing %d method(s).\n",
				name, alias, name, alias, len(methods))
		}
		n++
	}

	return n, nil
}

// tenantsWithNames is every tenant that has a `Host` row, once each, in the
// order their first name was written.
//
// Paged, which the walk this replaced was not: it read the first page of names
// and stopped, so the twenty-first name a deployment registered was a tenant
// this app never fronted.
func tenantsWithNames(ctx context.Context, s *cmd.Server) ([]pdid.Id, error) {
	seen := map[pdid.Id]bool{}
	out := []pdid.Id{}

	after := ""
	for {
		vs, err := s.Ungated.Host().List(ctx, rstr.HostListRequest_builder{
			Size:  100,
			After: after,
		}.Build())
		if err != nil {
			return nil, err
		}

		for _, v := range vs.GetItems() {
			t, err := pdid.From(v.GetTenant().GetId())
			if err != nil {
				return nil, err
			}
			if seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}

		if after = vs.GetNext(); after == "" {
			return out, nil
		}
	}
}

// ensureNominated makes `borrower`'s keys answer as `who` in this tenant, and
// says whether that changed anything.
//
// Already pointing at them is nothing to write, which keeps a restart from
// being a row changed and a `Watch` event in every tenant.
func ensureNominated(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, borrower pdid.Id, who []byte, name string, labels map[string]string) (bool, error) {
	v, err := s.Ungated.Nomination().Get(ctx, rstr.NominationGetRequest_builder{
		Ref: rstr.NominationRef_builder{
			Borrower: rstr.NominationRefByBorrower_builder{Tenant: at, BorrowerId: borrower.Bytes()}.Build(),
		}.Build(),
		Select: rstr.NominationSelect_builder{
			ActsAs:      rstr.HolderSelect_builder{}.Build(),
			Labels:      z.Ptr(true),
			DateUpdated: z.Ptr(true),
		}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		_, err := s.Ungated.Nomination().Add(ctx, rstr.NominationAddRequest_builder{
			Tenant:     at,
			BorrowerId: borrower.Bytes(),
			ActsAs:     rstr.HolderRef_builder{Id: who}.Build(),
			Name:       name,
			Labels:     labels,
		}.Build())

		return err == nil, err
	}
	if err != nil {
		return false, err
	}

	next, relabel := withLabels(v.GetLabels(), labels)
	repoint := !bytes.Equal(v.GetActsAs().GetId(), who)
	if !relabel && !repoint {
		return false, nil
	}

	patch := rstr.NominationPatchRequest_builder{
		Ref:         rstr.NominationRef_builder{Id: v.GetId()}.Build(),
		DateUpdated: v.GetDateUpdated(),
	}
	if repoint {
		patch.ActsAs = rstr.HolderRef_builder{Id: who}.Build()
	}
	if relabel {
		patch.Labels = next
	}
	_, err = s.Ungated.Nomination().Patch(ctx, patch.Build())

	return err == nil && repoint, err
}

// provisioned is what this command's rows are called, so that a later run finds
// them and a person reading the admin console can tell them from somebody's.
const provisioned = "login-app"

func mustFind[T any](cl *xli.Command, name string) T {
	v, _ := flg.Find[T](cl, name)

	return v
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

	// The app's own memory of a browser, on **Hydra's clock**.
	//
	// This session used to live from the form to the consent screen and be
	// closed at the redirect, on the rule that a credential should not outlive
	// its use. The cost of that was invisible and large: the claims in a token
	// are read as the person, with the delegation this session holds, so every
	// flow after the first -- a second product, or the same one opened again --
	// found no session and put nothing but `sub` in the token. With `remember`
	// set, that is **most** flows, and a deployment's tokens carry no name and
	// no address for eight hours at a time.
	//
	// So the session lasts exactly as long as Hydra will skip the form. Hydra
	// remembering this browser and this app remembering it are one fact, and a
	// second clock under it is what produced the hole. No idle window for the
	// same reason: Hydra's `remember_for` is absolute, and an idle timeout here
	// would end the app's half early and bring the empty tokens back for
	// anybody who steps away.
	//
	// What the cookie is worth is the other half of why this is safe: the
	// delegation it holds is narrowed to [login.Methods], which is `Me.Get`
	// and nothing else, about the one person it names. Somebody who steals it
	// reads that person's own profile; they cannot sign in as them, write
	// anything, or read anybody else. And it is revoked where every other
	// delegation is -- `Holder.Invalidate`, which this app already hears
	// through `Sync.Watch`.
	if lc.Remember > 0 {
		opts = append(opts, authsession.WithLifetime(lc.Remember), authsession.WithIdle(0))
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
	// **not** the same is where `enrolling`'s cost lands: `HolderService.Add` is
	// added to the role `roster login provision` binds to this app's holder in
	// each tenant, never to the key -- so it is only as wide as the tenant that
	// role is in. Said at start rather than at the first stranger's sign-in.
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
		Secret: account.EnvSecret,

		// One credential, and the app narrows it per request. It was one `rt_`
		// per tenant in a map, with a second map saying which OAuth client was
		// whose so the right key could be picked; both are gone (#36).
		Key: lc.Key,
	}
	if len(lc.Hydra.Header) > 0 {
		cfg.HydraHeader = http.Header{}
		for k, v := range lc.Hydra.Header {
			cfg.HydraHeader.Set(k, v)
		}
	}
	if lc.Page.Dir != "" {
		cfg.Page = cmd.Page(lc.Page.Dir)
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
		slog.String("hydra", cfg.Hydra))

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
