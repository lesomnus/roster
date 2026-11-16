package prove

import (
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

// answering is a DNS server a test writes, on a port of this machine.
//
// What it is asked goes to `say`, and what `say` hands back is the answer
// section and the code beside it. `holds` is whether it answers as a server
// that holds the zone or as a resolver that went and asked one. The two differ
// by a bit in the header, and it is not decoration: `net.Resolver` reads an
// empty answer carrying neither bit as a referral, which it does not follow.
func answering(t *testing.T, holds bool, say func(q dnsmessage.Question) ([]dnsmessage.Resource, dnsmessage.RCode)) string {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { pc.Close() })

	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}

			var m dnsmessage.Message
			if err := m.Unpack(buf[:n]); err != nil || len(m.Questions) == 0 {
				continue
			}

			answers, code := say(m.Questions[0])
			b, err := (&dnsmessage.Message{
				Header: dnsmessage.Header{
					ID: m.ID, Response: true, RCode: code,
					Authoritative:      holds,
					RecursionDesired:   m.RecursionDesired,
					RecursionAvailable: !holds,
				},
				Questions: m.Questions,
				Answers:   answers,
			}).Pack()
			if err != nil {
				continue
			}

			_, _ = pc.WriteTo(b, from)
		}
	}()

	return pc.LocalAddr().String()
}

// asked is a question as a test writes one down: the type, and the name
// lowered, because a resolver may ask in whatever capitals it likes.
func asked(q dnsmessage.Question) (dnsmessage.Type, string) {
	return q.Type, strings.ToLower(q.Name.String())
}

func record(owner string, kind dnsmessage.Type, body dnsmessage.ResourceBody) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{
			Name: dnsmessage.MustNewName(owner), Type: kind, Class: dnsmessage.ClassINET, TTL: 60,
		},
		Body: body,
	}
}

func ns(owner, server string) dnsmessage.Resource {
	return record(owner, dnsmessage.TypeNS, &dnsmessage.NSResource{NS: dnsmessage.MustNewName(server)})
}

func cname(owner, to string) dnsmessage.Resource {
	return record(owner, dnsmessage.TypeCNAME, &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(to)})
}

func a(owner string, at [4]byte) dnsmessage.Resource {
	return record(owner, dnsmessage.TypeA, &dnsmessage.AResource{A: at})
}

func txt(owner string, says ...string) dnsmessage.Resource {
	return record(owner, dnsmessage.TypeTXT, &dnsmessage.TXTResource{TXT: says})
}

// The three things a server says when it has no record to give: the name is
// there and holds nothing of that type, the name is not there, and it could not
// find out.
const (
	nothing = dnsmessage.RCodeSuccess
	nowhere = dnsmessage.RCodeNameError
	failed  = dnsmessage.RCodeServerFailure
)

// TestANameIsAskedOfItsOwnZone: the servers asked about a name are the ones
// that hold the zone it is in, and a name that is an alias is not a zone.
//
// # What it was
//
// The walk up a name took the first candidate `LookupNS` answered for. Asked
// for the `NS` of a name that is a `CNAME`, a recursive resolver follows the
// alias and answers with the name servers of where it points, and
// `net.Resolver` hands those back without the name that owns them. A name
// pointed at another zone's apex is an ordinary thing to have -- it is how a
// product somebody else hosts is reached -- and it was read as a zone of its
// own, held by the servers of the zone it points into.
//
// So the TXT question for `_roster-challenge.app.victim.example` went to
// `platform.example`'s servers, and what they said about a name they do not
// hold was taken as the zone's answer. Whoever can have those servers answer
// for it -- another customer of the same DNS host, adding a zone of that name
// -- proved a name that was never theirs (#277).
func TestANameIsAskedOfItsOwnZone(t *testing.T) {
	// One zone, `victim.example`, and what a recursive resolver says of the
	// names in it.
	at := answering(t, false, func(q dnsmessage.Question) ([]dnsmessage.Resource, dnsmessage.RCode) {
		kind, name := asked(q)

		switch name {
		case "victim.example.":
			if kind == dnsmessage.TypeNS {
				return []dnsmessage.Resource{ns(name, "ns1.victim.example.")}, nothing
			}

			return nil, nothing

		// An alias to the apex of somebody else's zone. Whatever is asked of it,
		// the answer begins with the alias; asked for `NS`, it goes on with the
		// name servers of where the alias points.
		case "app.victim.example.":
			alias := []dnsmessage.Resource{cname(name, "platform.example.")}
			if kind == dnsmessage.TypeNS {
				return append(alias,
					ns("platform.example.", "ns1.platform.example."),
					ns("platform.example.", "ns2.platform.example."),
				), nothing
			}

			return alias, nothing

		// A zone of its own below the first, whose apex serves nothing.
		case "own.victim.example.":
			if kind == dnsmessage.TypeNS {
				return []dnsmessage.Resource{ns(name, "ns1.own.victim.example.")}, nothing
			}

			return nil, nothing

		// And one whose apex is a site, spelled by the resolver in the capitals
		// its zone file has.
		case "site.victim.example.":
			switch kind {
			case dnsmessage.TypeNS:
				return []dnsmessage.Resource{ns(name, "ns1.site.victim.example.")}, nothing
			case dnsmessage.TypeA:
				return []dnsmessage.Resource{a("Site.Victim.Example.", [4]byte{192, 0, 2, 7})}, nothing
			}

			return nil, nothing

		// A zone the resolver can list the servers of and say nothing else
		// about.
		case "flaky.victim.example.":
			if kind == dnsmessage.TypeNS {
				return []dnsmessage.Resource{ns(name, "ns1.flaky.victim.example.")}, nothing
			}

			return nil, failed
		}

		return nil, nowhere
	})

	r := authoritative{boot: bootstrap(at)}
	ctx := t.Context()

	t.Run("an alias is asked of the zone the alias is written in", func(t *testing.T) {
		x := require.New(t)

		servers, zone, err := r.zone(ctx, Record+".app.victim.example")
		x.NoError(err)
		x.Equal("victim.example", zone,
			"a name that is an alias was taken for a zone")
		x.Equal([]string{"ns1.victim.example"}, servers,
			"the servers of where an alias points were asked about the alias")
	})

	t.Run("a zone of its own is still asked of its own servers", func(t *testing.T) {
		x := require.New(t)

		// The control. Where the cut is cannot be known from the name, and a
		// name with `NS` records and no alias is one: an apex that serves no
		// address is an ordinary zone, not a name to walk past.
		for name, server := range map[string]string{
			"own.victim.example":  "ns1.own.victim.example",
			"site.victim.example": "ns1.site.victim.example",
		} {
			servers, zone, err := r.zone(ctx, Record+"."+name)
			x.NoError(err, name)
			x.Equal(name, zone)
			x.Equal([]string{server}, servers)
		}
	})

	t.Run("and a resolver that could not say which it is has not answered", func(t *testing.T) {
		x := require.New(t)

		// Neither way out is right. Walking on to the parent asks servers that
		// may not hold the name; taking the candidate is the defect above with
		// a failure in front of it.
		servers, zone, err := r.zone(ctx, Record+".flaky.victim.example")
		x.Error(err, "asked of %v, for %q", servers, zone)
	})
}

// TestANameServerIsFoundByTheResolverTheDeploymentNamed: where a zone's server
// is, is asked of the resolver that said which server it is.
//
// # What it was
//
// The `NS` lookup went to the resolver the deployment named and what it
// answered with was a hostname. Asking that server was a dial of the hostname,
// and a dial finds a name with the system's resolver. So a deployment that set
// `host.resolver` because the system's resolver answers out of an internal
// zone -- which is the one thing the setting is for -- was back on the system's
// for the half that decides which machine is asked (#277).
func TestANameServerIsFoundByTheResolverTheDeploymentNamed(t *testing.T) {
	x := require.New(t)

	const (
		name  = "app.tenant.test"
		token = Prefix + "what-roster-asked-for"
	)

	// The zone's own server. `.test` is nobody's, so nothing but the resolver
	// below can say where `ns1.dns.test` is.
	holder := answering(t, true, func(q dnsmessage.Question) ([]dnsmessage.Resource, dnsmessage.RCode) {
		if kind, name := asked(q); kind == dnsmessage.TypeTXT && name == Record+".app.tenant.test." {
			return []dnsmessage.Resource{txt(name, token)}, nothing
		}

		return nil, nowhere
	})
	_, port, err := net.SplitHostPort(holder)
	x.NoError(err)

	// And the resolver the deployment named, which is the only one that knows
	// the zone or its server.
	at := answering(t, false, func(q dnsmessage.Question) ([]dnsmessage.Resource, dnsmessage.RCode) {
		switch kind, name := asked(q); {
		case name == "tenant.test." && kind == dnsmessage.TypeNS:
			return []dnsmessage.Resource{ns(name, "ns1.dns.test.")}, nothing
		case name == "tenant.test.":
			return nil, nothing
		case name == "ns1.dns.test." && kind == dnsmessage.TypeA:
			return []dnsmessage.Resource{a(name, [4]byte{127, 0, 0, 1})}, nothing
		case name == "ns1.dns.test.":
			return nil, nothing
		}

		return nil, nowhere
	})

	held, err := Held(t.Context(), authoritative{boot: bootstrap(at), port: port}, name, token)
	x.NoError(err, "the zone's server was looked for somewhere other than the resolver that named it")
	x.True(held)
}
