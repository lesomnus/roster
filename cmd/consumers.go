package cmd

// The two consumers, as configuration.
//
// `roster account serve` and `roster ldap serve` are separate processes that
// reach roster over the wire, and `scripts/test.sh` holds them to it: neither
// package may import `internal`, `cmd` or `server` (`server/front` excepted).
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

// TlsConfig is a certificate and the key that goes with it, as paths.
type TlsConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// IsSet is whether a certificate was named.
func (c TlsConfig) IsSet() bool { return c.Cert != "" || c.Key != "" }
