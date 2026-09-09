package cmd

import "time"

// The three consumers, as configuration.
//
// `roster account serve`, `roster ldap serve` and `roster login serve` are
// separate processes that reach roster over the wire, and `scripts/test.sh`
// holds them to it: none of those packages may import `internal`, `cmd` or
// `server` (`server/front` excepted, and `login/` takes not even that).
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
// The account app faces the internet and holds one tenant key per operator.
// The control plane holds every key and the database. In one process a bug in
// the first reaches the second; in three, that is a kernel boundary rather than
// a code one. Worth the pods for a deployment that has them, and a lot of
// ceremony for one that does not -- which is why it is the deployment's answer
// and not this file's.
//
// Nothing is weakened by the shorter answer: in one process the account app
// still dials roster's own listener, so it is still a caller with a key, still
// walled, and still unable to import what it must not.

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
	// `control.console.dir` is already the name for "the built UI directory
	// for this listener" and a reader who has seen one should be able to guess
	// the other. The flag is still `--static`, which is what it was.
	Page PageConfig `yaml:"page"`

	// Enrol is what happens to a stranger a provider vouches for: `invited`,
	// which is nobody, or `enrolling`.
	Enrol string `yaml:"enrol"`

	// Keys is one tenant key per operator fronted, by alias.
	//
	// The values are **references** and not tokens: `env:NAME`, the one scheme
	// this binary knows. A key is a secret and this file is not a place for
	// one. `ROSTER_ACCOUNT_KEY_<ALIAS>` still works and is merged with these,
	// and `--key alias=rt_…` still takes a literal, which is in the process
	// list and says so.
	Keys map[string]string `yaml:"keys"`

	// Seal is the key sessions are sealed into the cookie under, as `env:NAME`
	// holding 32 bytes of base64. Repeat to rotate: the first seals and every
	// one opens. Empty is a key made at start, which is one replica.
	Seal []string `yaml:"seal"`

	// InsecureCookie drops `Secure`, for a page served over plain http in
	// development.
	InsecureCookie bool `yaml:"insecure_cookie"`
}

// Serves is whether this deployment answers a front door.
func (c AccountConfig) Serves() bool { return c.Addr != "" }

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

	// Keys is one tenant key per operator fronted, by alias, as `env:NAME`.
	// See [AccountConfig.Keys]; `ROSTER_LDAP_KEY_<ALIAS>` is merged with it.
	Keys map[string]string `yaml:"keys"`

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
}

// Serves is whether this deployment answers LDAP.
func (c LdapConfig) Serves() bool { return c.Addr != "" || c.AddrTls != "" }

// LoginConfig is the Login App: the box Hydra hands a `login_challenge` to.
//
// The third consumer, and the one whose settings are not all roster's -- it has
// an `hydra` block, because the glue is the glue. Everything else reads like
// the two above: named is a listener, empty is nowhere, and the keys are
// references rather than tokens.
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

	// Keys is one tenant key per operator fronted, by alias, as `env:NAME`.
	// See [AccountConfig.Keys]; `ROSTER_LOGIN_KEY_<ALIAS>` is merged with it.
	Keys map[string]string `yaml:"keys"`

	// Clients is which OAuth clients are whose, by the same alias: `contoso:
	// [contoso-web, contoso-mobile]`. It is what says which tenant a challenge
	// belongs to, and it is the one setting here that is about Hydra's rows
	// rather than roster's.
	//
	// A **list**, because an operator with two products has two clients and one
	// sign-in; `ROSTER_LOGIN_CLIENT_<ALIAS>` takes them comma separated, and so
	// does `--client alias=a,b`. A client named for two operators is refused at
	// start: which one it is decides whose password is checked.
	//
	// # Two flat maps and not a block per operator
	//
	// A block would carry both facts about one customer together, and the first
	// draft of this was one -- the argument being that two maps keyed the same
	// way is a place to add an entry to one and not the other. What decided
	// against it is the **environment**: a key is a secret, so it arrives as
	// `ROSTER_LOGIN_KEY_<ALIAS>`, which is flat and which nothing nested
	// answers to. A nested block would have needed a flat `keys` beside it
	// anyway, and then there are two places to write a key, which is worse than
	// two places to write an operator.
	//
	// The risk the block was for is closed where it actually bites: a key with
	// no client, or a client with no key, is refused at start by name and with
	// the flag that fixes it (`cli/login.go`). Half an operator never runs.
	Clients map[string][]string `yaml:"clients"`

	// Consent is what happens at the consent hop: `skip`, which grants what the
	// client asked for and draws nothing, or `ask`, which draws a screen.
	//
	// Empty is `skip`, and that is a decision rather than an omission. Every
	// client in [Clients] was registered by this deployment for one of its own
	// operators -- a third party cannot be in that map -- so every one of them
	// is first-party, and a consent screen for an app the operator wrote is a
	// dialog people learn to click through. `ask` is for a deployment that
	// registers clients somebody else wrote, where the screen is the whole
	// point.
	Consent string `yaml:"consent"`

	// Remember is how long Hydra should skip the form for a browser that has
	// already signed in. Zero asks every time.
	Remember time.Duration `yaml:"remember"`

	// Seal is the key sessions are sealed into the cookie under; see
	// [AccountConfig.Seal]. The session here is short -- it holds one browser
	// between the form and the consent screen and is closed at the redirect --
	// but a second replica still has to be able to open one.
	Seal []string `yaml:"seal"`

	// InsecureCookie drops `Secure`, for a page served over plain http in
	// development.
	InsecureCookie bool `yaml:"insecure_cookie"`

	// Page is the sign-in page, when a deployment serves its own instead of the
	// one this app embeds. Empty takes the embedded one.
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
	// The deployment's arrangement and not this app's.
	Header map[string]string `yaml:"header"`
}

// Serves is whether this deployment answers a login flow.
func (c LoginConfig) Serves() bool { return c.Addr != "" }

// TlsConfig is a certificate and the key that goes with it, as paths.
type TlsConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// IsSet is whether a certificate was named.
func (c TlsConfig) IsSet() bool { return c.Cert != "" || c.Key != "" }
