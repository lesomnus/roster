// Package core is what roster means that no schema can state.
//
// The schema says an identity is a `(provider, subject)` pair unique across the
// deployment. It cannot say that a subject which looks like an email address is
// almost certainly the wrong value, or that one person having two accounts at
// one provider is a link that went wrong rather than a fact. Those are
// judgements, and this is where they live.
package core

import (
	"context"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// Core is the layer that answers what this app decided.
type Core struct {
	app.Overlay

	// rules is what a caller holds, which `cmd` reads for `gate.Policy` and
	// hands over rather than this package asking a second time. See [Rules].
	rules Rules

	// lock is a write on somebody's own row that nothing asked for. See
	// [Core.only]: it is what makes two callers removing a person's last two
	// ways in contend for something, and there is nothing in the generated API
	// that does it -- a `Patch` carrying only a version test compiles to an
	// existence check and no write at all, which is D34's finding arriving in a
	// second place.
	//
	// Supplied rather than done here, for `Rules`' reason: it is a write
	// against ent, and this package deliberately holds no client. `cmd` has
	// one and already reads it directly, with the same argument.
	lock Lock

	// drv is what this stack was built on, for the one judgement here that a
	// read cannot make on its own.
	//
	// Nearly every rule in this package is *look, then refuse* -- and a look is
	// enough when what it looks at is the request. [coreIdentity.Erase] is the
	// exception: what it looks at is a **count** of other rows, and a count
	// taken outside a transaction is a fact about a moment that has passed. See
	// [Core.only].
	//
	// Nil is a stack assembled without one, which is every test that builds a
	// layer by hand. The rule reads that as *the caller has arranged the
	// transaction* and does its work in whatever it was handed, which is also
	// what it does inside a batch.
	drv dialect.Driver

	// breached answers whether a secret is one somebody has already lost, when
	// the deployment has a corpus. It is the same question `server/vouch` asks,
	// handed to this layer because `Credential.Set` now writes here -- nil is a
	// deployment with no corpus, which refuses no secret.
	breached Breached

	// keyring is what wraps a TOTP seed, handed to this layer because
	// `Credential.Enrol` makes one here now. The zero value holds no key, which
	// is a deployment that cannot store a second factor -- `Enrol` refuses a
	// `totp` there rather than writing a seed nothing can read back. It is the
	// same keyring `server/vouch` verifies a code with, so a factor enrolled
	// here is one that plane can check.
	keyring vouch.Keyring

	// prefix is which plane a minted key belongs to -- `rk_` on the control
	// plane, `rt_` on the data plane -- handed to this layer because
	// `ApiKey.Mint` makes the secret here now. It is a fact about the server
	// that answered and never a field, so a caller cannot ask one port for the
	// other's kind. The zero value is `keys.PrefixTenant`'s empty sibling, which
	// `Mint` refuses rather than mint an unprefixed key.
	prefix string
}

// Breached is whether a secret is in a corpus of leaked ones. Nil refuses none.
type Breached func(ctx context.Context, secret []byte) (bool, error)

// WithBreached gives the layer the corpus check, so a credential write can
// refuse a leaked secret without a service reaching back for the rule.
func WithBreached(v Breached) Option { return func(s *Core) { s.breached = v } }

// WithKeyring gives the layer the key a TOTP seed is wrapped with, so
// `Credential.Enrol` can make a second factor without a service holding the
// crypto. Left out, the layer holds no key and refuses a `totp` enrolment.
func WithKeyring(v vouch.Keyring) Option { return func(s *Core) { s.keyring = v } }

// WithPrefix gives the layer the plane a minted key belongs to (`rk_`/`rt_`),
// so `ApiKey.Mint` can make the secret without a service choosing which port's
// kind it is. One per stack; see `server/keys`.
func WithPrefix(v string) Option { return func(s *Core) { s.prefix = v } }

// Rules is what this layer has to know about a caller and cannot work out.
//
// All four answers come from the same rows `gate.Policy` reads -- bindings, group
// memberships, team memberships -- and `cmd` reads them against ent, because
// working out what somebody may do cannot itself require permission. Asking
// again here would be a second implementation of one question, and two
// implementations of one question drift.
//
// A zero value refuses everything a frame carries, which is the safe direction
// for a stack somebody assembled without it.
type Rules struct {
	// Holds answers whether somebody may call a method, for a team. See [Holds].
	Holds Holds

	// Granted is every method somebody holds **through a binding**, which is
	// what they may pass on. See [Granted].
	Granted Granted

	// Joining is what a group holds, which is what putting somebody into it
	// hands them. See [Joining].
	Joining Joining

	// Holding is everything somebody holds by any path, which is what
	// [Core.mayReach] compares. See [Holding].
	Holding Holding

	// Held is the same union as [Rules.Holding], in the shape a page reads:
	// the method patterns, the sites, and whether a binding reaches the whole
	// tenant. It is what `Holder.Reaches` answers with and what `MeService.Get`
	// answers about the caller -- one function, so the two cannot disagree.
	// Nil is a stack that cannot say, and `Reaches` says so.
	Held Held
}

// Held is what somebody may call and where, as the gate decides it. The
// signature is `me.Held`'s, so one function serves both.
type Held func(ctx context.Context, who pdid.Id) (methods []string, sites []pdid.Id, every bool, err error)

// holding is [Rules.Holding], or [Rules.Granted] where a stack was assembled
// before there were two answers.
//
// A fallback rather than a requirement, because the two are the same for every
// deployment that has no teams and no groups -- and a stack built without the
// wider one should refuse conservatively rather than answer as though nobody
// held anything, which is the direction that lets a credential be written.
func (r Rules) holding() Holding {
	if r.Holding != nil {
		return r.Holding
	}
	if r.Granted == nil {
		return nil
	}

	return Holding(r.Granted)
}

func New(next app.Server, rules Rules, opts ...Option) Core {
	s := Core{Overlay: app.NewOverlay(next), rules: rules}
	for _, opt := range opts {
		opt(&s)
	}

	return s
}

// Option is something this layer is told beside its rules.
type Option func(*Core)

// Lock is a write on somebody's own row that nothing asked for, made on the
// driver it is handed so that it lands inside whatever transaction is open.
//
// It exists because two callers have to contend for something and the schema's
// own version is not reachable as a write: see [Core.only].
type Lock func(ctx context.Context, drv dialect.Driver, who pdid.Id) error

// On is the driver this stack was built on and the write one rule takes to
// serialise on; see [Core.drv] and [Lock].
func On(drv dialect.Driver, lock Lock) Option {
	return func(s *Core) {
		s.drv = drv
		s.lock = lock
	}
}

// Build makes a builder of this layer so that it can be stacked.
func Build(rules Rules, opts ...Option) app.Builder { return builder{rules, opts} }

type builder struct {
	rules Rules
	opts  []Option
}

func (b builder) Build(next app.Server) (app.Server, error) {
	return New(next, b.rules, b.opts...), nil
}

var (
	_ app.Server               = Core{}
	_ enttx.Binder[app.Server] = Core{}
)

// WithDriver answers with this stack running on `drv`.
//
// Every layer writes this and none can inherit it: an overlay holds what is
// behind it and has no way to make itself again, so a layer that did not write
// it would be missing from the rebuilt stack and the requests inside the
// transaction would go around it.
func (s Core) WithDriver(drv dialect.Driver) (app.Server, error) {
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	// The driver is **not** carried over: this stack is being rebound onto a
	// transaction somebody else opened, and a rule that began its own inside
	// that one would be a transaction nested in a transaction. Nil is how
	// [Core.only] is told the caller has already arranged one.
	//
	// The corpus and the keyring **are**, for the reason `Rules` is: a
	// `Credential.Set` or `Enrol` made inside a batch is still a write that must
	// refuse a leaked secret and must wrap a seed with the deployment's key.
	// Dropped, a rebuilt stack would accept a breached password and refuse every
	// second factor the moment two writes shared a transaction.
	return New(next, s.rules, WithBreached(s.breached), WithKeyring(s.keyring), WithPrefix(s.prefix)), nil
}
