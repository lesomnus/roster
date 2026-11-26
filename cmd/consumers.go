package cmd

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/cfg/ref"
)

// The consumers, as configuration.
//
// `roster account serve`, `roster ldap serve`, `roster login serve` and
// `roster scim serve` are separate processes that reach roster over the wire,
// and `scripts/test.sh` holds them to it: none of those packages may import
// `internal`, `cmd` or `server` (`server/front` excepted, for how an address
// and a host are written as they are stored).
// That is what makes them proof that roster's wire API is enough, and none of
// it changes here.
//
// What changes is where they are **told** things. They took flags and nothing
// else, so a deployment that wanted all three wrote one configuration file and
// two command lines -- and `roster config env` could list the variables of the
// file and not of the other two.
//
// Now they are blocks, the shape `control:` and `admin:` already have: named is
// a listener, empty is nowhere. `roster serve` opens whichever are named, and
// the separate commands stay for a deployment that wants the process boundary.
// Both paths build the same thing from the same values.
//
// # Why a deployment might still want three processes
//
// The account app faces the internet and holds one tenant key per tenant.
// The control plane holds every key and the database. In one process a bug in
// the first reaches the second; in three, that is a kernel boundary rather than
// a code one. Worth the pods for a deployment that has them, and a lot of
// ceremony for one that does not -- which is why it is the deployment's answer
// and not this file's.
//
// Nothing is weakened by the shorter answer: in one process the account app
// still dials roster's own listener, so it is still a caller with a key, still
// walled, and still unable to import what it must not.

// Token is a key an app holds -- a deployment key, a tenant key, a seal --
// as the loader holds a secret: the key itself, `${env:NAME}` or
// `${file:/path}` in the file, and from a variable or a flag the key itself or
// one reference as the whole value. `roster config` prints a reference as it
// is written and the key itself as `<redacted>`.
//
// The whitespace around it goes, because what wrote a file of one almost
// certainly ended it with a newline, and nothing at the edges of a key can be
// part of it.
//
// What a file of the loader's says is read once, where the app is built, as a
// key named any other way was: a key that is replaced takes a restart.
type Token = cfg.SecretOf[string, cfg.TrimSpaceDecoder]

// AccountConfig is the front door a customer's people sign in at.
//
// Empty `addr` is nowhere, like `control.addr`: a deployment that runs the
// account app as its own process leaves this out and `roster account serve`
// reads it from flags instead.
type AccountConfig struct {
	// Addr is where it listens. Empty is nowhere.
	Addr string `yaml:"addr"`

	// Roster is where roster's data plane speaks gRPC, and Connect is where
	// the same server speaks HTTP -- the page's own calls are handed on there.
	//
	// Both default to this deployment's own listeners when the account app runs
	// inside `roster serve`, because writing them again in the file that
	// already says `server.addr` is one more place for two answers to drift.
	// It is a default and not a shortcut: the call still goes out on a socket,
	// with a key, and comes back through the wall.
	Roster  string `yaml:"roster"`
	Connect string `yaml:"connect"`

	// Insecure dials roster without TLS.
	Insecure bool `yaml:"insecure"`

	// Base is this app's public origin, which every provider has registered as
	// the redirect.
	Base string `yaml:"base"`

	// Page is the built account page, served by this app. Empty serves none,
	// which is an API and no front end -- right for a deployment that puts the
	// page somewhere else.
	//
	// A block of one field rather than `static:` beside the rest, because
	// `admin.console.dir` is already the name for "the built UI directory
	// for this listener" and a reader who has seen one should be able to guess
	// the other. The flag is still `--static`, which is what it was.
	Page PageConfig `yaml:"page"`

	// Enrol is who a provider may sign in: `invited`, `expected` or
	// `enrolling`. See [LoginConfig.Enrol], which says what each is.
	Enrol string `yaml:"enrol"`

	// PrivateHosts is the hosts off the internet this front door may ask a
	// provider at, each a name, an address or a network: `idp.corp.example`,
	// `10.20.0.5`, `10.20.0.0/16`.
	//
	// A `Connection` says where its provider is, and a connection with no
	// secret is its tenant's administrator's to write. So a front door asks a
	// provider -- for its document, at its token endpoint, for its keys and
	// its userinfo -- only at an address on the internet, and refuses this
	// machine, a private network, a link's own addresses and with them the
	// cloud's metadata service, whatever the name it was given resolves to.
	// A directory on the premises is at one of those, and which hosts inside
	// are a provider's is the deployment's to say: here, and nowhere a tenant
	// writes. A row this deployment's own file declares is held to it like
	// any other, since the rule is about the address.
	//
	// Named rather than a switch, as [MailConfig.PlaintextHosts] is and for
	// its reason: a switch would open every address inside to every tenant's
	// row. Empty is none.
	//
	// It is about what this process connects to itself. Where the environment
	// names a proxy (`HTTPS_PROXY`), a request goes out through that and where
	// the provider is, is the proxy's to refuse; this then holds only what
	// `NO_PROXY` sends direct.
	PrivateHosts []string `yaml:"private_hosts"`

	// TenantKeys is one tenant key per tenant fronted, by alias.
	//
	// Each a [Token]: a key is a secret and this file is not a place for one,
	// so a deployment writes where it is kept, `${env:NAME}` or
	// `${file:/path}`. The environment is the other form, merged with these:
	// one variable per tenant, `ROSTER_ACCOUNT_TENANT_KEY_<ALIAS>`, holding the
	// key. And `--tenant-key alias=rt_…` takes the key, which is in the process
	// list and says so.
	//
	// One of this and [AccountConfig.DeploymentKey]: a tenant running its own
	// copy names its `rt_` here.
	//
	// **Both empty, inside `roster serve`, is a key made at start**: the rows
	// `roster account provision` writes -- one deployment key, and in every
	// tenant with a `Host` row a holder, a role holding what the app calls as
	// itself, a binding and the nomination -- with the token kept in memory,
	// which is one replica: the reading [AccountConfig.Seal] gives an empty
	// value. A restart is a rotation. `roster account serve` still refuses with
	// neither, because somebody typed that one and a process whose only job is
	// the front door has nothing to do without a key.
	TenantKeys map[string]Token `yaml:"tenant_keys"`

	// DeploymentKey is one deployment key for every tenant fronted, narrowed on each call
	// to the holder that tenant nominated for it -- the shape the Login App
	// takes, and the one a roster operator running this for many tenants wants
	// (#76, `docs/apps.md`). A [Token], written where it is kept:
	// `${file:/run/roster-account/account.key}` is what `roster account
	// provision` writes. A tenant this app is put into later is fronted at the
	// first request for one of its names, with no restart.
	DeploymentKey Token `yaml:"deployment_key"`

	// Seal is the key sessions are sealed into the cookie under, 32 bytes of
	// base64, each a [Token]. Repeat to rotate: the first seals and every one
	// opens. Empty is a key made at start, which is one replica.
	Seal []Token `yaml:"seal"`

	// Terminal is whether a machine with no browser may ask for a key here:
	// `roster sign-in` prints a code, somebody types it into this app in a page
	// they are already signed in at, and the command is handed the key. RFC 8628,
	// and `account/device.go` says why the flow is in this app and not in roster.
	//
	// And whether an app may ask the same way to act for somebody at roster:
	// begun with `delegate`, the yes mints a delegation issued to the app
	// (`DelegationService/Issue`) rather than a key. One switch opens both, since
	// both are a door into an account a person approves at this page.
	//
	// **Off unless a deployment says so.** It is a door into an account, and the
	// narrowest default is what this repository does with those -- `Public` names
	// nothing and `login.enrol` is `invited`. Off, the endpoints answer 501 the
	// way the mail flows do when nothing can deliver, so a terminal -- or an app
	// asking to act for somebody -- is told rather than left polling for fifteen
	// minutes on a code nobody can approve.
	//
	// The blunt control beside it is not the same thing: granting no role that
	// names `ApiKey.Issue` turns off **every** self-service key, the page's own
	// app passwords included. This turns off one door and leaves the page alone.
	Terminal bool `yaml:"terminal"`

	// InsecureCookie drops `Secure`, for a page served over plain http in
	// development. Beside an `https` base it is refused ([secureBeside]).
	InsecureCookie bool `yaml:"insecure_cookie"`

	// Mail is how this front door delivers the links roster mints for it: a
	// recovery link, a link that confirms an address, a link that deletes an
	// account. Empty is a door that cannot, which is what it was: the flows
	// answer 501 and `/providers` offers none of them.
	Mail MailConfig `yaml:"mail"`
}

// MailConfig is the SMTP server the front door sends through
// (`package mail`). roster mints the links and this delivers them, which is
// D19's line: no server of roster's sends mail.
//
// One server, in the shape every transactional provider documents -- an
// address, a username, a password, a sender -- because the provider is the
// deployment's choice and SMTP is what all of them speak.
type MailConfig struct {
	// Addr is the server, `host:port`, as every dial in this file is
	// written. Empty sends nothing. A port left out is the one `tls` uses:
	// 587, 465, or 25.
	Addr string `yaml:"addr"`

	// Username and Password sign in. The password is a secret of the loader's
	// and is written where it is kept, `${env:NAME}` or `${file:/path}`: this
	// file is not a place for one, [AccountConfig.DeploymentKey]'s rule for the same
	// reason, and `roster config` prints one written as itself as
	// `<redacted>`. The username is a value that may be kept somewhere else
	// and usually is not: written as itself and printed so, or a reference,
	// because some providers' usernames are credentials too (SES's is derived
	// from an access key). Empty signs in as nobody.
	Username ref.String `yaml:"username"`
	Password cfg.Secret `yaml:"password"`

	// From is who a message is from: `Contoso <no-reply@contoso.example>`.
	// An address of a domain the provider was told is yours, so that what it
	// signs for that domain (SPF, DKIM) is what the message says it is from.
	From string `yaml:"from"`

	// Tls is how the connection is protected: `starttls`, the default, which
	// refuses a server that does not offer it; `implicit`, TLS from the first
	// byte, which is port 465; or `none`, in the clear, which reaches only this
	// machine or a host [MailConfig.PlaintextHosts] names.
	Tls string `yaml:"tls"`

	// PlaintextHosts is the hosts besides this machine that `tls: none` may
	// reach: a sink on the network in development, `mailpit` in a compose
	// file. Named rather than a switch, so that pointing `addr` at a provider
	// does not leave the connection in the clear.
	PlaintextHosts []string `yaml:"plaintext_hosts"`

	// Timeout bounds one message, from the dial to the server's last answer.
	// Empty is 30s.
	Timeout time.Duration `yaml:"timeout"`
}

// On is whether the front door sends mail.
func (c MailConfig) On() bool { return c.Addr != "" }

// Serves is whether this deployment answers a front door.
func (c AccountConfig) Serves() bool { return c.Addr != "" }

// Meant refuses an `account:` block whose values are not of their form, as
// [ScimConfig.Meant] does: each started and was found at the first request
// that read it. Asked by [Build] where the block is served, and by `roster
// account serve`; the flag is named beside the key, since this cannot tell
// which said it.
func (c AccountConfig) Meant() error {
	if err := webOrigin("account.base (--base)", c.Base); err != nil {
		return err
	}
	if err := secureBeside("account", c.Base, c.InsecureCookie); err != nil {
		return err
	}

	return dirThere("account.page.dir (--static)", c.Page.Dir)
}

// secureBeside refuses a front door's cookies without `Secure` where its base
// is `https`.
//
// `insecure_cookie` is for a page served over plain http, where a browser
// would not send a `Secure` cookie back. Beside an `https` base it is every
// browser handed its session without the flag, which is what an overlay got
// that said its own base and inherited the switch from `deploy/config.yaml`:
// told once at start, by a line of payday's that named no key.
func secureBeside(block, base string, insecure bool) error {
	if !insecure {
		return nil
	}
	u, err := url.Parse(base)
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return nil
	}

	return fmt.Errorf("%[1]s.insecure_cookie (--insecure-cookie) is on and %[1]s.base (--base) is https: "+
		"its cookies would go to every browser without Secure. take %[1]s.insecure_cookie out; it is for a page served over plain http", block)
}

// PageConfig is a built page, as a directory.
type PageConfig struct {
	// Dir is `ts/dist/account`, or wherever the build was put.
	Dir string `yaml:"dir"`
}

// LdapConfig is roster as a directory, for clients that speak nothing else.
type LdapConfig struct {
	// Addr speaks LDAP, offering StartTLS when `tls` is given. AddrTls speaks
	// LDAPS and needs it. Both empty is nowhere.
	Addr    string `yaml:"addr"`
	AddrTls string `yaml:"addr_tls"`

	// Roster is where roster's data plane speaks gRPC, defaulting to this
	// deployment's own for the reason [AccountConfig.Roster] gives.
	Roster string `yaml:"roster"`

	// Insecure dials roster without TLS.
	Insecure bool `yaml:"insecure"`

	// TenantKeys is one tenant key per tenant fronted, by alias, each a
	// [Token]. See [AccountConfig.TenantKeys];
	// `ROSTER_LDAP_TENANT_KEY_<ALIAS>` is merged with it.
	TenantKeys map[string]Token `yaml:"tenant_keys"`

	// DeploymentKey is one deployment key instead, answered in each tenant as the holder
	// that tenant nominated for it -- `roster control key add --allow
	// /roster.NominationService/List directory`, then `roster app install
	// directory` per tenant with the directory's role. A [Token]. One of this
	// and TenantKeys (#76).
	DeploymentKey Token `yaml:"deployment_key"`

	// Bases is a tenant's suffix, by alias. `o=<alias>` where none is given.
	Bases map[string]string `yaml:"bases"`

	// Bind is what a bind's password may be: `key`, an app password the person
	// minted, which is the default; `password`, their own; or `either`.
	Bind string `yaml:"bind"`

	// Tls is this server's certificate. Given, it offers StartTLS and enables
	// `addr_tls`.
	Tls TlsConfig `yaml:"tls"`

	// RequireTls refuses a bind in the clear: a client must StartTLS or arrive
	// on LDAPS first.
	RequireTls bool `yaml:"require_tls"`

	// MaxConns is how many connections the directory holds at once, and
	// MaxConnsPerAddr how many of them one address has (an IPv6 address
	// counted with the /64 it is in); one more is closed with nothing said.
	// Nought, in the file or in a `Config` made in Go, is the default:
	// 1024 and 256, `ldap/wire`'s, which `roster`'s command line also puts
	// here before the file is read so that `roster config` shows them. It is
	// never *no bound*. Below nought is refused ([LdapConfig.Meant]). What a connection
	// costs, and so what raising either spends, is said beside the defaults
	// (`ldap/wire/listen.go`).
	MaxConns        int `yaml:"max_conns"`
	MaxConnsPerAddr int `yaml:"max_conns_per_addr"`
}

// Serves is whether this deployment answers LDAP.
func (c LdapConfig) Serves() bool { return c.Addr != "" || c.AddrTls != "" }

// Meant refuses an `ldap:` block whose values are not of their form; see
// [AccountConfig.Meant].
//
// A suffix holds an `=`, and one that does not is what a suffix written with
// its own commas in `ROSTER_LDAP_BASES` becomes: the environment parts a map's
// pairs at commas, so `acme=dc=acme,dc=example` is two tenants, the second
// called `dc` under `example`. The flow form, `{acme: "dc=acme,dc=example"}`,
// is read whole.
//
// And a bound on connections below nought, which cannot be meant: nought is
// the default, as it is left unsaid, so that a program carrying roster in its
// own process starts a directory without knowing either number.
func (c LdapConfig) Meant() error {
	for alias, suffix := range c.Bases {
		if !strings.Contains(suffix, "=") {
			return fmt.Errorf("ldap.bases.%s (--base): %q is no suffix, which is attributes and their values, as "+
				"dc=acme,dc=example. In ROSTER_LDAP_BASES a suffix with commas is written in the flow form, "+
				"'{acme: \"dc=acme,dc=example\"}': written as pairs, the commas part it", alias, suffix)
		}
	}
	if c.MaxConns < 0 {
		return fmt.Errorf("ldap.max_conns: %d connections at once cannot be meant; write how many the directory "+
			"holds, or leave it out (or 0) for 1024", c.MaxConns)
	}
	if c.MaxConnsPerAddr < 0 {
		return fmt.Errorf("ldap.max_conns_per_addr: %d connections from one address cannot be meant; write how many "+
			"one address has, or leave it out (or 0) for 256", c.MaxConnsPerAddr)
	}

	return nil
}

// ScimConfig is roster as a SCIM 2.0 endpoint: where a directory pushes the
// people it provisions (`docs/scim.md`).
//
// A listener of its own and not a path on `server.http`, though a deployment
// exposes it at a path: that listener answers every RPC over Connect, so a
// proxy rule that forwarded a little too much would publish all of it to the
// directory's cloud. Here a rule that forwards too much publishes SCIM.
type ScimConfig struct {
	// Addr speaks HTTP, the path `/scim/v2`. Empty is nowhere.
	Addr string `yaml:"addr"`

	// Roster is where the data plane speaks gRPC, defaulting to this
	// deployment's own for the reason [AccountConfig.Roster] gives.
	Roster string `yaml:"roster"`

	// Insecure dials roster without TLS.
	Insecure bool `yaml:"insecure"`

	// Concurrency is how many requests of one tenant's directory are worked
	// on at once, counted by the tenant the key is of: a directory's cycle is
	// a burst, and the rate a tenant's callers are held to is shared with its
	// people signing in. Asking roster who a key is, for a key not
	// remembered, is held to the same number for the process. Four when
	// nought or unsaid; below nought is refused ([ScimConfig.Meant]).
	Concurrency int `yaml:"concurrency"`
}

// Serves is whether this deployment answers SCIM.
func (c ScimConfig) Serves() bool { return c.Addr != "" }

// Meant refuses a `scim:` block that cannot be meant, as [AuditMeant] does an
// `audit:` one: a `concurrency` below nought was read as four with nothing
// said.
func (c ScimConfig) Meant() error {
	if c.Concurrency < 0 {
		return fmt.Errorf("scim.concurrency: %d requests at once cannot be meant; write how many of one "+
			"tenant's directory are worked on together, or leave it out for four", c.Concurrency)
	}

	return nil
}

// LoginConfig is the Login App: the box Hydra hands a `login_challenge` to.
//
// The third consumer, and the one whose settings are not all roster's -- it has
// an `hydra` block, because the glue is the glue. Everything else reads like
// the two above: named is a listener, empty is nowhere, and the key is written
// where it is kept.
type LoginConfig struct {
	// Addr is where it listens. Empty is nowhere.
	Addr string `yaml:"addr"`

	// Roster is where the data plane speaks gRPC, defaulting to this
	// deployment's own for the reason [AccountConfig.Roster] gives.
	Roster string `yaml:"roster"`

	// Insecure dials roster without TLS.
	Insecure bool `yaml:"insecure"`

	// Hydra is the one thing here that is not roster's.
	Hydra HydraConfig `yaml:"hydra"`

	// DeploymentKey is the **one** credential this instance holds, a [Token],
	// and it is a deployment key (`rk_`).
	//
	// # It was a map, and both maps are gone
	//
	// `keys` was one `rt_` per tenant fronted, by alias, and `clients` said which
	// OAuth clients were whose so that the right key could be picked. Two things
	// per customer for a roster operator to write, one of them a secret to
	// distribute and rotate -- and the client map forced a **Hydra registration
	// per customer**, because a discriminator that is the client cannot tell two
	// tenants of one product apart.
	//
	// Now: one key, and which tenant a flow is about comes from the redirect the
	// authorization request named, resolved through that tenant's own `Host` row.
	// So adding a customer is a row they write themselves (#42) and nothing here
	// changes. `login/at.go` is the resolution and why the client could not stay.
	//
	// # Why an `rk_` is allowed to be this app's credential now
	//
	// `docs/login.md` refused one: an `rk_` resolves to a frame with no tenant,
	// so what separated customers would be this app's own code. A `Nomination`
	// answers it rather than waiving it -- every call goes out with `roster-at`
	// and is answered as the holder that tenant nominated for this app's key,
	// with their bindings and nothing wider (#43, #73). The wall is still what
	// separates them; it is applied per request instead of per process.
	//
	// **Empty, inside `roster serve`, is a key made at start**, with the
	// nominations that go with it -- what `roster login provision` writes,
	// without the file -- kept in memory, which is one replica. It needs the
	// control plane an `rk_` lives in, and `roster login serve` still refuses
	// with none, for [AccountConfig.TenantKeys]'s reason.
	DeploymentKey Token `yaml:"deployment_key"`

	// Base is this app's public origin, which every provider has registered as
	// the redirect: `https://login.example.com`.
	//
	// **One for the whole app**, unlike the account app's, which has a host per
	// tenant. Hydra sends every browser here under one name, so there is one
	// callback and which tenant it belongs to comes from the state. An
	// tenant adding a `Connection` registers this URL with their directory.
	Base string `yaml:"base"`

	// Enrol is who a directory may sign in. Empty is `invited`.
	//
	//	invited    only somebody already linked -- an `Identity` row somebody
	//	           wrote, naming the subject that directory asserts
	//	expected   somebody an tenant entered, matched by the **address** on
	//	           their row, and nobody else
	//	enrolling  that, and a stranger too, named by the local part of their
	//	           address where the directory says it is verified
	//
	// `expected` is what an tenant means by *putting people in*, and
	// `invited` is not: the subject a directory asserts is issued there and is
	// not knowable in advance, so an tenant entering somebody has their
	// address and nothing else. `invited` can therefore admit nobody at all
	// through a directory, which is right for a deployment that writes
	// `Identity` rows itself and is a trap for one that does not.
	//
	// Matching adds one condition over what an `Email` row already is (*a way
	// to sign in as whoever the row is about*, `CLAUDE.md`): the directory has
	// to say the address is **verified**. One that lets somebody type an
	// address into their own profile would otherwise hand out whichever account
	// carries it. A name is read off an address on the same condition: a
	// stranger whose address nobody checked is enrolled under a name nobody
	// chose, and not under whatever they typed before the `@`.
	//
	// `enrolling` needs the key to hold `HolderService.Add`, which the one
	// `roster login provision` mints does not: making people is a wider grant
	// than signing them in, and `--enrol` is what says so out loud.
	Enrol string `yaml:"enrol"`

	// PrivateHosts is the hosts off the internet this app may ask a provider
	// at; see [AccountConfig.PrivateHosts], which says why it asks only
	// outside unless told.
	PrivateHosts []string `yaml:"private_hosts"`

	// Consent is what happens at the consent hop: `skip`, which grants what the
	// client asked for and draws nothing, or `ask`, which draws a screen.
	//
	// Empty is `skip`, and that is a decision rather than an omission. A
	// client is registered with Hydra by whoever runs this deployment, for its
	// own tenants' apps, so the ordinary one is first-party, and a consent
	// screen for an app the tenant wrote is a dialog people learn to click
	// through. `ask` is for a deployment that registers clients somebody else
	// wrote, where the screen is the whole point.
	//
	// A device's flow (RFC 8628) is drawn the screen whichever this says: the
	// screen its code is typed at cannot name the client, so the consent is
	// where somebody first sees whose device they are letting in.
	Consent string `yaml:"consent"`

	// Remember is how long Hydra should skip the form for a browser that has
	// already signed in. Zero asks every time.
	Remember time.Duration `yaml:"remember"`

	// Seal is the key sessions are sealed into the cookie under; see
	// [AccountConfig.Seal]. A second replica has to be able to open one.
	//
	// The session here lasts as long as [LoginConfig.Remember]: it is this
	// app's half of the same fact Hydra keeps, and what it holds is a
	// delegation narrowed to `Me.Get` about one person. It was closed at the
	// redirect for a while, which made every flow after the first hand back a
	// token with no name and no address in it.
	Seal []Token `yaml:"seal"`

	// InsecureCookie drops `Secure`, for a page served over plain http in
	// development. Beside an `https` base it is refused ([secureBeside]).
	InsecureCookie bool `yaml:"insecure_cookie"`

	// Page is the built sign-in page (`ts/dist/login`). Empty serves none: the
	// app embeds no page, and `/login` answers 404, *this deployment serves no
	// sign-in page*. A directory that is not there is refused at start.
	Page PageConfig `yaml:"page"`
}

// HydraConfig is where Hydra's admin API answers.
type HydraConfig struct {
	// Admin is its base URL, e.g. `http://hydra:4445`.
	//
	// **Private by construction.** Anybody who reaches this can sign anybody in
	// as anybody, so what protects it is that it is not routable rather than a
	// credential. A deployment that puts a proxy in front says so with [Header].
	Admin string `yaml:"admin"`

	// Header is sent with every admin call, as `Name: value`, for that proxy.
	// The deployment's arrangement and not this app's. A secret to the loader,
	// since a header for a proxy is a credential: `roster config` prints none
	// of its values.
	Header map[string]string `yaml:"header" cfg:",secret"`
}

// Serves is whether this deployment answers a login flow.
func (c LoginConfig) Serves() bool { return c.Addr != "" }

// Meant refuses a `login:` block whose values are not of their form; see
// [AccountConfig.Meant].
func (c LoginConfig) Meant() error {
	if err := webOrigin("login.hydra.admin (--hydra)", c.Hydra.Admin); err != nil {
		return err
	}
	if err := webOrigin("login.base (--base)", c.Base); err != nil {
		return err
	}
	if err := secureBeside("login", c.Base, c.InsecureCookie); err != nil {
		return err
	}

	return dirThere("login.page.dir (--static)", c.Page.Dir)
}

// TlsConfig is a certificate and the key that goes with it, each a value of
// the file as a listener's `tls:` is: `${file:/path}`, `${env:NAME}` or the
// PEM itself. `roster config` prints `cert` and leaves `key` out, by its name.
//
// They are read when the listener is built, so a certificate that is
// replaced takes a restart.
type TlsConfig struct {
	Cert ref.String `yaml:"cert"`
	Key  ref.String `yaml:"key"`
}

// IsSet is whether a certificate was named.
func (c TlsConfig) IsSet() bool { return !c.Cert.IsZero() || !c.Key.IsZero() }
