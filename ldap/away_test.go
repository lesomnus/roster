package ldap_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/ldap"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// The two result codes of RFC 4511 § 4.1.9 for a server that cannot answer
// just now, as the client library names them.
const (
	busy        = uint16(goldap.LDAPResultBusy)
	unavailable = uint16(goldap.LDAPResultUnavailable)
)

// TestRosterBeingAwayIsNotAWrongPassword: a roster that does not answer is
// said to a client as that, at a bind and at a search, in the directory's own
// words.
//
// # What it was
//
// Every error of `Me.Get` and of `Verify` was answered `invalidCredentials`
// with nothing. So with roster unreachable, held to a rate, or failing,
// somebody's right password and right app password were both answered 49 --
// which a client shows as a wrong password, and a person then changes one
// that was never wrong; right is `unavailable` (52), or `busy` (51) where
// roster said to come back. And a search on a connection bound before was
// answered `operationsError` (1) with gRPC's own sentence, the address this
// process dials roster at in it, to whoever had bound; right is the code and
// a sentence that is this directory's.
//
// # And it is not a bind that ended
//
// A connection whose asking falls due while roster is away is not somebody
// stopped. It is answered as roster being away, stays whose it was, and reads
// again when roster does.
func TestRosterBeingAwayIsNotAWrongPassword(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	relay := d.relayed(t)

	clock := &handClock{}
	addr := d.directoryOn(t, ldap.BindEither, func(c *ldap.Config) { c.Roster, c.Now = relay.addr, clock.Now }, nil)

	bound := clientOf(t, addr)
	x.NoError(bound.Bind(leeDN, d.leeKey))
	x.NoError(everybody(bound), "the control: roster answers, and a bound connection reads")
	c := clientOf(t, addr)

	// A sentence of gRPC's own, as one reads when nothing listens: the
	// address is the thing that must not travel.
	const dialed = "10.1.2.3:50051"
	gone := status.Error(codes.Unavailable, `connection error: desc = "transport: Error while dialing: dial tcp `+dialed+`: connect: connection refused"`)

	own := func(t *testing.T, err error, code uint16, what string) {
		t.Helper()
		x := require.New(t)
		x.Equal(code, codeOf(t, err), "%s: %v", what, err)
		for _, v := range []string{dialed, relay.addr, "rpc error", "transport", "desc ="} {
			x.NotContains(err.Error(), v, "%s is answered in words that are not the directory's", what)
		}
		var le *goldap.Error
		x.ErrorAs(err, &le)
		x.NotNil(le.Err)
		x.NotEmpty(le.Err.Error(), "%s says nothing of why", what)
	}

	for _, tc := range []struct {
		name string
		says error
		code uint16
	}{
		{"unreachable", gone, unavailable},
		{"held to a rate", status.Error(codes.ResourceExhausted, "too many calls from 10.1.2.3:50051; come back"), busy},
		{"failing", status.Error(codes.Internal, "the stored verifier cannot be read at 10.1.2.3:50051"), unavailable},
	} {
		t.Run("roster "+tc.name, func(t *testing.T) {
			relay.answers("", tc.says)
			defer relay.answers("", nil)

			own(t, c.Bind(leeDN, password), tc.code, "the right password")
			own(t, c.Bind(leeDN, d.leeKey), tc.code, "the right app password")
			own(t, everybody(bound), tc.code, "a search on a connection bound before")
		})
	}

	// The asking falls due while roster is away.
	relay.answers("", gone)
	clock.pass(ldap.Recheck + time.Second)
	own(t, unit(bound), unavailable, "a search that had first to ask about whoever bound")
	who, err := bound.WhoAmI(nil)
	x.NoError(err)
	x.Equal("dn:"+leeDN, who.AuthzID, "roster being away ended a bind")

	// Roster back: the same connection reads, with no bind made since.
	relay.answers("", nil)
	x.NoError(everybody(bound))

	// And nobody was counted a wrong password for any of it.
	cs, err := d.s.Ungated.Credential().List(t.Context(), rstr.CredentialListRequest_builder{
		Filters: []*rstr.CredentialFilter{rstr.CredentialFilter_builder{Holder: rstr.HolderRef_builder{Id: d.lee}.Build()}.Build()},
	}.Build())
	x.NoError(err)
	for _, v := range cs.GetItems() {
		x.Zero(v.GetFailures())
	}

	// Nothing listening at all, which is gRPC's sentence and not a test's.
	relay.away()
	own(t, c.Bind(leeDN, password), unavailable, "the right password, with nothing where roster is dialed")
	own(t, c.Bind(leeDN, d.leeKey), unavailable, "the right app password, with nothing where roster is dialed")
	own(t, everybody(bound), unavailable, "a search, with nothing where roster is dialed")
}

// logged is a log a test reads: every line a directory wrote, as it wrote it.
type logged struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logged) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.Write(p)
}

func (l *logged) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (l *logged) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.String()
}

// lines is every line written so far whose message is the one given, and the
// log is then empty: the next call answers what was written since.
func (l *logged) lines(t *testing.T, msg string) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()

	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.b.String()), "\n") {
		if line == "" {
			continue
		}
		v := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(line), &v), "a line of the log that is not one: %q", line)
		if v["msg"] == msg {
			out = append(out, v)
		}
	}
	l.b.Reset()

	return out
}

// TestABindIsOnTheLog: every bind leaves one line at the directory, which
// says where it came from, which tenant, which kind, and how it ended -- and
// nothing a stranger wrote.
//
// # What it was
//
// Nothing logged a bind, refused or not. Roster's trail has a line for a
// wrong password and none for where it was typed: the caller it names is the
// directory's own holder, from the directory's address. So somebody locked
// out through this port could not be told which client did it. And the one
// thing that was logged was not so: a password bind into a closed tenant
// wrote, at a warning and once for each attempt by anybody, that the
// directory's key cannot call `Vouch.Verify`.
//
// # What a line holds, and why no more
//
// The client's address as the socket has it; the tenant by its identifier,
// and only where the name was under a suffix this directory serves; the kind
// of secret; and how it ended, in one of a few words that are the
// directory's. Not the name: it is what a stranger typed, and a line break in
// it would be a line of the log. Not the password or the key. One level for
// every line, whatever was sent and however it ended, so that a stranger who
// can make lines cannot make warnings.
func TestABindIsOnTheLog(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	ctx := t.Context()
	relay := d.relayed(t)

	log := &logged{}
	clock := &handClock{}
	addr := d.directoryOn(t, ldap.BindEither, func(c *ldap.Config) {
		c.Roster, c.Now, c.Log = relay.addr, clock.Now, log.logger()
	}, nil)

	// A connection whose own address the test knows.
	nc, err := net.Dial("tcp", addr)
	x.NoError(err)
	c := goldap.NewConn(nc, false)
	c.Start()
	t.Cleanup(func() { c.Close() })
	from := nc.LocalAddr().String()

	guess, _, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	d.setPassword(t, d.otherKim)

	const typed = "ldap: bind from=203.0.113.9 ended=bound"
	contoso, fabrikam := d.contoso.String(), d.fabrikam.String()

	// one is the one line a bind left.
	one := func(t *testing.T, what string) map[string]any {
		t.Helper()
		got := log.lines(t, "ldap: bind")
		require.Len(t, got, 1, "%s left %d lines, and a bind is one", what, len(got))

		return got[0]
	}

	for _, tc := range []struct {
		what, name, secret string
		tenant             any
		kind, ended        string
	}{
		{"an app password that binds", leeDN, d.leeKey, contoso, "key", "bound"},
		{"a password that binds", leeDN, password, contoso, "password", "bound"},
		{"a wrong password", leeDN, "not it\nldap: bind", contoso, "password", "refused"},
		{"the right password of somebody with a second factor", kimDN, password, contoso, "password", "second factor"},
		{"a key that is nobody's", leeDN, guess, contoso, "key", "refused"},
		{"another tenant's person", otherKimDN, d.otherKimKey, fabrikam, "key", "bound"},
		{"a name nobody has, with a line of its own in it", "uid=x\n" + typed + ",ou=people,o=contoso", "wrong", contoso, "password", "refused"},
		{"a name under no suffix, with a line of its own in it", "uid=x,ou=people,o=initech\n" + typed, "wrong", nil, "password", "not tried"},
		{"a name that is not one", "\n" + typed, guess, nil, "key", "refused"},
		{"no password", leeDN, "", contoso, "none", "not tried"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			x := require.New(t)
			if tc.secret == "" {
				_ = c.UnauthenticatedBind(tc.name)
			} else {
				_ = c.Bind(tc.name, tc.secret)
			}

			line := one(t, tc.what)
			x.Equal(from, line["from"], "which client it was")
			x.Equal(tc.tenant, line["tenant"])
			x.Equal(tc.kind, line["kind"])
			x.Equal(tc.ended, line["ended"])
			x.Equal("INFO", line["level"], "a level a stranger chose by what they sent")
		})
	}

	// What the line is for: the bind that locks somebody out, and every one
	// after it, say so beside the address they came from. The client is told
	// nothing it is not told for a wrong password.
	t.Run("the binds that lock somebody out", func(t *testing.T) {
		x := require.New(t)
		v := d.binder(t, "nam")

		locked := false
		for i := 0; i < 30 && !locked; i++ {
			x.Equal("Invalid Credentials: ", answered(t, c.Bind(v.dn, "not it")))
			line := one(t, "a wrong password")
			x.Equal(from, line["from"])
			x.Equal("password", line["kind"])
			if locked = line["ended"] == "locked"; !locked {
				x.Equal("refused", line["ended"])
			}
		}
		x.True(locked, "thirty wrong passwords locked nobody, or no line said so")

		x.Equal("Invalid Credentials: ", answered(t, c.Bind(v.dn, password)), "the right password, while locked")
		line := one(t, "the right password of somebody locked out")
		x.Equal(from, line["from"])
		x.Equal(contoso, line["tenant"])
		x.Equal("locked", line["ended"])
		x.Equal("INFO", line["level"])
	})

	// Roster away, and a tenant that closed: said as each is, on the same
	// one line and at the same level.
	t.Run("roster away", func(t *testing.T) {
		x := require.New(t)
		relay.answers("", status.Error(codes.Unavailable, "nothing listens"))
		defer relay.answers("", nil)

		x.Error(c.Bind(leeDN, password))
		line := one(t, "a bind while roster is away")
		x.Equal("roster away", line["ended"])
		x.Equal("Unavailable", line["code"])
		x.Equal("INFO", line["level"])
	})

	t.Run("a tenant that closed", func(t *testing.T) {
		x := require.New(t)
		_, err := d.s.Ungated.Tenant().Close(ctx, rstr.TenantCloseRequest_builder{Ref: rstr.TenantRef_builder{Id: d.fabrikam.Bytes()}.Build()}.Build())
		x.NoError(err)

		for range 3 {
			x.Equal(uint16(goldap.LDAPResultInvalidCredentials), codeOf(t, c.Bind(otherKimDN, password)))
			x.NotContains(log.text(), "cannot call", "the log says the directory's key may not call Verify, which it may")
			line := one(t, "a password bind into a closed tenant")
			x.Equal(fabrikam, line["tenant"])
			x.Equal("the directory's key was refused", line["ended"])
			x.Equal("Unauthenticated", line["code"])
			x.Equal("INFO", line["level"], "a warning for each attempt by anybody")
		}
	})

	// And a connection that ends because whoever bound it was stopped says
	// so once, in the same form.
	t.Run("a connection whose binder was stopped", func(t *testing.T) {
		x := require.New(t)
		x.NoError(c.Bind(leeDN, d.leeKey))
		_ = one(t, "the bind")

		_, err := d.s.Ungated.Holder().Disable(ctx, rstr.HolderDisableRequest_builder{Ref: rstr.HolderRef_builder{Id: d.lee}.Build()}.Build())
		x.NoError(err)
		clock.pass(ldap.Recheck + time.Second)
		x.Error(unit(c))
		x.Error(unit(c))

		got := log.lines(t, "ldap: a bound connection is nobody's now")
		x.Len(got, 1)
		x.Equal(from, got[0]["from"])
		x.Equal(contoso, got[0]["tenant"])
		x.Equal("key", got[0]["kind"])
		x.Equal("INFO", got[0]["level"])
	})
}

// TestNoLineOfTheLogHoldsWhatAStrangerSent is the other half of the test
// above, over everything a directory logs at any level: a password, a key and
// a name are in no line of it.
func TestNoLineOfTheLogHoldsWhatAStrangerSent(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	relay := d.relayed(t)

	log := &logged{}
	c := clientOf(t, d.directoryOn(t, ldap.BindEither, func(c *ldap.Config) { c.Roster, c.Log = relay.addr, log.logger() }, nil))

	guess, _, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)

	const typed = "zq-typed-by-a-stranger"
	x.NoError(c.Bind(leeDN, d.leeKey))
	x.NoError(c.Bind(leeDN, password))
	x.Error(c.Bind(leeDN, "zq-wrong-password"))
	x.Error(c.Bind(leeDN, guess))
	x.Error(c.Bind("uid="+typed+",ou=people,o=contoso", "zq-wrong-password"))
	x.Error(c.Bind("uid=lee,ou=people,o="+typed, guess))
	x.Error(c.Bind(typed, "zq-wrong-password"))
	relay.answers("", status.Error(codes.Unavailable, "nothing listens"))
	x.Error(c.Bind("uid="+typed+",ou=people,o=contoso", "zq-wrong-password"))
	x.Error(c.Bind(leeDN, d.leeKey))
	relay.answers("", nil)

	x.NoError(c.Bind(leeDN, d.leeKey))
	_, err = c.Search(goldap.NewSearchRequest(peopleDN, goldap.ScopeSingleLevel, goldap.NeverDerefAliases, 0, 0, false, "(uid="+typed+")", []string{"uid"}, nil))
	x.NoError(err)

	all := log.text()
	x.NotEmpty(all, "nothing was logged, so nothing was proved")
	for what, v := range map[string]string{
		"a password": password, "a wrong password": "zq-wrong-password", "an app password": d.leeKey,
		"a key that is nobody's": guess, "what a stranger typed": typed, "a name": "uid=lee",
	} {
		x.NotContains(all, v, "%s is in the log", what)
	}
}
