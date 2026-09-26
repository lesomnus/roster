// Package prove is how a tenant shows a hostname is theirs.
//
// One question, asked of DNS: *is the value roster handed you published under
// the name you are claiming?* Whoever can answer yes controls the name, because
// putting a record under a name is the thing owning a name means.
//
// `#42` is the issue, and what it changed is a paragraph rather than a
// mechanism: `Host` said roster does not resolve DNS and should not, on the
// grounds that it runs in an air gap. That answered the wrong question. An
// air-gapped deployment has no tenant registering its own hostnames either --
// what it has is a roster operator with a shell -- so there are two roads to a
// `Host` row, and this package is only the first of them.
//
// # It asks the servers that hold the zone, not a cache
//
// Which is the one thing here that is not obvious, and it is not purity. A
// recursive resolver caches **negative** answers, and the negative TTL comes
// from the zone's own SOA minimum -- five minutes to an hour, commonly. So:
// somebody publishes the record, presses the button, and is refused by an
// `NXDOMAIN` their resolver cached from the attempt thirty seconds ago. They
// press it again and are refused again. What they conclude is that roster is
// broken, and they are not being unreasonable.
//
// ACME does it this way for the same reason, and it costs no dependency:
// `net.Resolver` with `PreferGo` and a `Dial` of one's own sends the query
// wherever it is told, and a server that is authoritative for the zone answers
// from the zone rather than from anything it remembered.
//
// # What it deliberately does not do
//
// Validate DNSSEC, or walk from the root. What the delegation walk here is for
// is finding the closest enclosing zone, which is a lookup of `NS` records
// through the ordinary resolver -- so a deployment whose resolver owns the name
// internally can still be misled about where the zone is, and
// [Config.Resolver] is how that deployment says otherwise.
//
// That is the honest bound of this check: it proves somebody could write a
// record in the zone this deployment's network agrees is the zone. A deployment
// that does not trust its own resolver about that has a bigger problem than
// hostnames.
package prove

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

// Record is the label a claim is published under, so that a token never sits on
// the name itself.
//
// Underscored, which is the convention for a name that is data rather than a
// host (`_acme-challenge`, `_dmarc`), and which keeps it from colliding with
// anything a tenant would serve.
const Record = "_roster-challenge"

// Prefix is what every token starts with.
//
// A zone file is read by people, and a bare thirty-two bytes of base64 in one
// says nothing about who asked for it or whether it can be deleted. It is part
// of the token rather than added on the way out, so that *publish this string*
// is literal and the comparison is an equality.
const Prefix = "roster-verify="

// For is how long a claim can be spent, unless a deployment says otherwise.
//
// A day, because the two ends of this are a person in a console and a person in
// a DNS provider's web form, and those are not the same afternoon in an
// organisation of any size. It is not a security window -- what the window is
// for is a token lying in DNS forever being a name provable by whoever finds
// it.
const For = 24 * time.Hour

// Token is a fresh value to publish, with [Prefix] on it.
func Token() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return Prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// Resolver is what reads the TXT records at a name.
//
// An interface with one method, so that a test writes a zone rather than a
// server (see [Static]) and so that a deployment with no resolver configured is
// a nil one rather than a flag somebody has to check in two places.
type Resolver interface {
	TXT(ctx context.Context, name string) ([]string, error)
}

// None is the resolver a deployment that cannot ask names, and it is the word
// `watch.broker` already uses for the same shape: one setting with three
// meanings, each written down.
const None = "none"

// Config is what this deployment needs to ask.
type Config struct {
	// Resolver is where the `NS` lookup goes. Three answers:
	//
	//	(empty)          the system's
	//	host:port        that one
	//	none             this deployment cannot ask
	//
	// The middle one exists for split horizon. The TXT query goes to the zone's
	// own servers whatever this says, but **finding** those servers is an
	// ordinary lookup -- so a resolver that answers for `example.com` out of an
	// internal zone answers this one too, and a deployment in that position
	// names a resolver that does not.
	//
	// `none` is an air gap, and it is a setting rather than an inference for the
	// reason `watch.broker` is: a deployment that cannot reach DNS and one whose
	// DNS is merely slow look identical from in here, and the difference decides
	// whether a tenant is told *claim your name* or *a roster operator writes
	// the row*. Without it the air-gapped deployment answers the first, thirty
	// seconds at a time.
	//
	// Empty is the ordinary case and is right for almost everybody.
	Resolver string `yaml:"resolver"`

	// Asks is a resolver handed over rather than described, and it wins.
	//
	// **Not in the file**, and it is the only field here that is not -- the
	// shape `ClientConfig.Local` already has. There is no way to write a zone in
	// YAML, and a test that wants to say *this record says that* has to say it
	// in Go. [Static] is what it says it with.
	//
	// A deployment cannot set this and so cannot be surprised by it: the loader
	// skips the key, and what is in the file is the line above.
	Asks Resolver `yaml:"-"`
}

// Asking is the resolver this configuration describes, and **nil** where it
// describes none.
func (c Config) Asking() Resolver {
	if c.Asks != nil {
		return c.Asks
	}
	if c.Resolver == None {
		return nil
	}

	return authoritative{boot: bootstrap(c.Resolver)}
}

// bootstrap is the resolver the `NS` lookup goes through.
func bootstrap(at string) *net.Resolver {
	if at == "" {
		return net.DefaultResolver
	}

	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, at)
		},
	}
}

// Held reports whether `want` is published at the challenge record for `name`.
//
// The answer is yes, no, or *could not ask*, and the caller needs all three
// apart: a lookup that failed is not a claim that was refused, and telling a
// tenant their record is wrong when the query never left is how somebody
// spends an afternoon on a correct zone file.
func Held(ctx context.Context, at Resolver, name, want string) (bool, error) {
	vs, err := at.TXT(ctx, Record+"."+name)
	if err != nil {
		return false, err
	}

	return slices.Contains(vs, want), nil
}

// authoritative asks the servers that hold the zone.
type authoritative struct{ boot *net.Resolver }

func (r authoritative) TXT(ctx context.Context, name string) ([]string, error) {
	servers, at, err := r.zone(ctx, name)
	if err != nil {
		return nil, err
	}

	// The first that answers. A zone is served by several and one of them being
	// unreachable is ordinary; a name that is genuinely not there is a
	// `NotFound` from the first one, which is an answer rather than a failure
	// and is returned as such.
	var last error
	for _, s := range servers {
		vs, err := r.ask(ctx, s).LookupTXT(ctx, name)
		if err == nil {
			return vs, nil
		}

		var dns *net.DNSError
		if errors.As(err, &dns) && dns.IsNotFound {
			return nil, nil
		}

		last = err
	}

	return nil, fmt.Errorf("%s: no server for %s answered: %w", name, at, last)
}

// ask is a resolver pointed at one server.
func (r authoritative) ask(ctx context.Context, server string) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(server, "53"))
		},
	}
}

// zone is the closest enclosing zone's servers, and the zone they hold.
//
// Walked up rather than looked up, because where the cut is cannot be known from
// the name: `foo.example.com` may be its own zone or a record in
// `example.com`'s, and both are ordinary. So the first `NS` that answers is the
// answer.
//
// It starts one label in, because the name asked about is [Record] under
// something and nobody delegates that label; and it stops before the last,
// because a TLD's servers hold no TXT record anybody here is asking for and
// asking them is a referral this does not follow.
func (r authoritative) zone(ctx context.Context, name string) ([]string, string, error) {
	for _, at := range candidates(name) {
		ns, err := r.boot.LookupNS(ctx, at)
		if err != nil || len(ns) == 0 {
			continue
		}

		servers := make([]string, 0, len(ns))
		for _, v := range ns {
			servers = append(servers, strings.TrimSuffix(v.Host, "."))
		}

		return servers, at, nil
	}

	return nil, "", fmt.Errorf("%s: nothing says which nameservers hold this name", name)
}

// candidates is the names a zone cut could be at, closest first.
//
// Separated from the lookup because it is the part with an off-by-one in it and
// the part that can be tested without a network. It starts one label in, because
// the name asked about is [Record] under something and nobody delegates that
// label; and it stops before the last, because a TLD's servers hold no TXT
// record anybody here is asking for.
func candidates(name string) []string {
	labels := strings.Split(strings.TrimSuffix(name, "."), ".")

	var vs []string
	for i := 1; i < len(labels)-1; i++ {
		vs = append(vs, strings.Join(labels[i:], "."))
	}

	return vs
}

// Static is a zone written down, for a test and for nothing else.
//
// Keyed by the whole record name -- `_roster-challenge.contoso.example` -- so
// that a test says what is published rather than how it would be found.
type Static map[string][]string

func (s Static) TXT(_ context.Context, name string) ([]string, error) {
	return s[strings.TrimSuffix(name, ".")], nil
}

// Refusing is a resolver that cannot ask, which is a deployment that named one
// it cannot reach. It exists so that *could not ask* has something to be in a
// test.
type Refusing struct{ Err error }

func (r Refusing) TXT(_ context.Context, name string) ([]string, error) {
	return nil, fmt.Errorf("%s: %w", name, r.Err)
}
