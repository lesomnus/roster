package ldap_test

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/ldap"
	rstr "github.com/lesomnus/roster/rstr"
)

// tally is the calls roster was asked, by method: a stats handler on a
// listener of its own, which the directory under test dials.
type tally struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *tally) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	c.mu.Lock()
	c.n[strings.TrimPrefix(info.FullMethodName, "/roster.")]++
	c.mu.Unlock()

	return ctx
}
func (c *tally) HandleRPC(context.Context, stats.RPCStats)                         {}
func (c *tally) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context { return ctx }
func (c *tally) HandleConn(context.Context, stats.ConnStats)                       {}

// since is what was asked since the last time this was read, and forgets it.
func (c *tally) since() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.n
	c.n = map[string]int{}

	return out
}

func total(calls map[string]int) int {
	n := 0
	for _, v := range calls {
		n += v
	}

	return n
}

// said is a tally as a sentence, for a log.
func said(calls map[string]int) string {
	keys := make([]string, 0, len(calls))
	for k := range calls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, calls[k]))
	}

	return fmt.Sprintf("%d calls [%s]", total(calls), strings.Join(parts, " "))
}

// counted is a second listener of the same roster that counts what it is
// asked, and a directory that dials it.
func (d *deployment) counted(t *testing.T, with func(*ldap.Config)) (*goldap.Conn, *tally) {
	t.Helper()
	c := &tally{n: map[string]int{}}
	g, err := d.s.Grpc(t.Context(), cmd.Config{}, grpc.StatsHandler(c))
	require.NoError(t, err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(func() { g.Stop() })

	conn := d.serve(t, ldap.BindKey, func(cfg *ldap.Config) {
		cfg.Roster = l.Addr().String()
		if with != nil {
			with(cfg)
		}
	})
	require.NoError(t, conn.Bind(kimDN, d.kimKey))
	c.since()

	return conn, c
}

// recorded is the rows the access record has gained since the last time this
// was read, by method, and how many people they name between them.
func (d *deployment) recorded(t *testing.T, seen *int) (map[string]int, int) {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, d.s.Access.Flush(ctx))
	rs, err := d.s.Ent.Access.Query().All(ctx)
	require.NoError(t, err)
	sort.Slice(rs, func(i, j int) bool { return rs[i].DateCreated.Before(rs[j].DateCreated) })

	rows, named := map[string]int{}, 0
	for _, r := range rs[min(*seen, len(rs)):] {
		rows[strings.TrimPrefix(r.Action, "/roster.")]++
		named += int(r.SubjectCount)
	}
	*seen = len(rs)

	return rows, named
}

// crowd is n more people of contoso, so that a read of one is told from a
// read of everybody: each has a proved address, every other one a mobile
// number, and every tenth is in the group `crowd`.
func (d *deployment) crowd(t *testing.T, n int) {
	t.Helper()
	ctx := t.Context()
	g := d.group(t, d.contoso, "crowd", "One in ten")
	for i := range n {
		id := d.person(t, d.contoso, fmt.Sprintf("c%05d", i), fmt.Sprintf("Crowd %05d", i), &rstr.Profile{}, func(p *rstr.Profile) {
			p.SetDepartment(fmt.Sprintf("D%d", i%7))
		})
		d.email(t, id, fmt.Sprintf("c%05d@contoso.example", i), true)
		if i%2 == 0 {
			_, err := d.s.Ungated.Phone().Add(ctx, rstr.PhoneAddRequest_builder{
				Holder: rstr.HolderRef_builder{Id: id}.Build(), Number: fmt.Sprintf("+82 10-%04d-%04d", i/10000, i%10000), Kind: "mobile",
			}.Build())
			require.NoError(t, err)
		}
		if i%10 == 0 {
			d.inGroup(t, id, g)
		}
	}
}

// page asks one page of a one-level search and answers its entries and the
// cookie that came back with them, which is empty after the last.
func page(t *testing.T, c *goldap.Conn, base, filter string, size uint32, cookie []byte, attrs ...string) ([]*goldap.Entry, []byte) {
	t.Helper()
	p := goldap.NewControlPaging(size)
	p.SetCookie(cookie)
	res, err := c.Search(goldap.NewSearchRequest(base, goldap.ScopeSingleLevel, goldap.NeverDerefAliases, 0, 0, false, filter, attrs, []goldap.Control{p}))
	require.NoError(t, err, filter)
	back, ok := goldap.FindControl(res.Controls, goldap.ControlTypePaging).(*goldap.ControlPaging)
	require.True(t, ok, "a paged search was answered with no paging control")

	return res.Entries, back.Cookie
}

// crowded is how many people the cost of a lookup is counted among. A
// thousand: the numbers below are the same for a hundred thousand, which is
// what they are held to, and a thousand is where the reader of #281 counted
// what they were.
const crowded = 1000

// TestALookupCostsWhatItReads holds a search to a number of roster's calls,
// among a thousand people. A directory front is asked the same few questions
// by every client at every sign-in, and each of these read as one call and
// was a read of the whole tenant -- with a row of the access record for each
// person it passed (#281).
//
// What is counted is what roster is asked over the wire, by a listener in
// front of it. Each number is exact, and none of them grows with the tenant.
func TestALookupCostsWhatItReads(t *testing.T) {
	d := stand(t)
	d.crowd(t, crowded)
	c, calls := d.counted(t, nil)
	seen := 0
	d.recorded(t, &seen)

	// ask is one search, the people it answered, the calls it cost and what
	// it left on the access record; the last two are logged, since the
	// manual quotes them (`docs/operating.md`, *Who read whose data*).
	ask := func(t *testing.T, base, filter string, page uint32, attrs ...string) ([]string, map[string]int) {
		t.Helper()
		req := goldap.NewSearchRequest(base, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases, 0, 0, false, filter, attrs, nil)
		var res *goldap.SearchResult
		var err error
		if page > 0 {
			res, err = c.SearchWithPaging(req, page)
		} else {
			res, err = c.Search(req)
		}
		require.NoError(t, err, filter)
		cost := calls.since()
		rows, named := d.recorded(t, &seen)
		t.Logf("%s attrs=%v page=%d: %d entries; roster %s; access record %s naming %d", filter, attrs, page, len(res.Entries), said(cost), said(rows), named)

		return dns(res.Entries), cost
	}

	const c7 = "uid=c00007,ou=people,o=contoso"

	t.Run("the sign-in filter many clients ship with is two reads", func(t *testing.T) {
		// `(&(objectClass=inetOrgPerson)(|(uid=x)(mail=x)))`. Only the direct
		// children of a top `&` were planned, so an `|` was everybody, with
		// an `EmailService/List` each: 1,015 calls. It is the union of what
		// each arm reads.
		x := require.New(t)
		got, cost := ask(t, peopleDN, "(&(objectClass=inetOrgPerson)(|(uid=c00007)(mail=c00007@contoso.example)))", 0, "uid")
		x.Equal([]string{c7}, got)
		x.Equal(map[string]int{"HolderService/Get": 1, "EmailService/Get": 1}, cost, said(cost))

		// And what a person types into the box is an address as often as a
		// name: the name arm finds nobody, and the address arm reads whose
		// the address is.
		got, cost = ask(t, peopleDN, "(&(objectClass=inetOrgPerson)(|(uid=c00007@contoso.example)(mail=c00007@contoso.example)))", 0, "uid")
		x.Equal([]string{c7}, got)
		x.Equal(map[string]int{"HolderService/Get": 2, "EmailService/Get": 1}, cost, said(cost))

		// Nested either way round, and with a class beside it.
		got, cost = ask(t, peopleDN, "(|(&(objectClass=person)(uid=c00007))(&(objectClass=person)(mail=c00008@contoso.example)))", 0, "uid")
		x.ElementsMatch([]string{c7, "uid=c00008,ou=people,o=contoso"}, got)
		x.Equal(map[string]int{"HolderService/Get": 2, "EmailService/Get": 1}, cost, said(cost))
	})

	t.Run("an or with an arm nothing but a read of everybody answers is a read of everybody", func(t *testing.T) {
		x := require.New(t)
		got, cost := ask(t, peopleDN, "(|(uid=c00007)(preferredLanguage=tlh))", 0, "uid")
		x.Equal([]string{c7}, got)
		x.Equal(map[string]int{"HolderService/List": 11}, cost, said(cost))
	})

	t.Run("a name nobody has is one read, and of nobody", func(t *testing.T) {
		// After a `NotFound` the plan paged `Holder.Search` for the name in
		// another case: 11 calls, and a thousand people on the access record
		// for a sign-in that named none of them. An alias is small letters by
		// rule, so what was asked is folded as roster folds one and asked
		// once.
		x := require.New(t)
		got, cost := ask(t, peopleDN, "(uid=c0)", 0, "uid")
		x.Empty(got)
		x.Equal(map[string]int{"HolderService/Get": 1}, cost, said(cost))

		got, cost = ask(t, peopleDN, "(uid=C00007)", 0, "uid")
		x.Equal([]string{c7}, got, "a name typed with a capital is the same name")
		x.Equal(map[string]int{"HolderService/Get": 1}, cost, said(cost))

		got, cost = ask(t, "uid=C00007,ou=people,o=contoso", "(objectClass=*)", 0, "uid")
		x.Equal([]string{c7}, got, "and so is a name read as a base")
		x.Equal(map[string]int{"HolderService/Get": 1}, cost, said(cost))
	})

	t.Run("an entryUUID is one read by identifier", func(t *testing.T) {
		// It is what a client is told to key on, and it was not planned: 11
		// calls, everybody read to find one.
		x := require.New(t)
		ids := search(t, c, c7, goldap.ScopeBaseObject, "(objectClass=*)", "entryUUID")
		x.Len(ids, 1)
		calls.since()
		d.recorded(t, &seen)

		got, cost := ask(t, peopleDN, "(entryUUID="+ids[0].GetAttributeValue("entryUUID")+")", 0, "uid")
		x.Equal([]string{c7}, got)
		x.Equal(map[string]int{"HolderService/Get": 1}, cost, said(cost))

		got, cost = ask(t, peopleDN, "(entryUUID="+strings.ToUpper(ids[0].GetAttributeValue("entryUUID"))+")", 0, "uid")
		x.Equal([]string{c7}, got, "an identifier in capitals is the same identifier")
		x.Equal(map[string]int{"HolderService/Get": 1}, cost, said(cost))

		park, err := pdid.From(d.park)
		x.NoError(err)
		for _, nobodys := range []string{"00000000-0000-0000-0000-000000000000", "not an identifier", park.String(), d.fabrikam.String()} {
			got, cost = ask(t, peopleDN, "(entryUUID="+nobodys+")", 0, "uid")
			x.Empty(got, nobodys)
			x.LessOrEqual(total(cost), 1, "%s: %s", nobodys, said(cost))
		}
	})

	t.Run("a filter roster has no index for reads the lists of whoever it keeps", func(t *testing.T) {
		// Every person's five lists were read before the filter was asked:
		// 5,037 calls to answer nobody. The filter is asked of what is
		// already read first.
		x := require.New(t)
		got, cost := ask(t, peopleDN, "(preferredLanguage=tlh)", 0)
		x.Empty(got)
		x.Equal(map[string]int{"HolderService/List": 11}, cost, said(cost))

		// A name is asked of roster's own search, which is one call for a
		// name nobody has.
		got, cost = ask(t, peopleDN, "(sn=Smith)", 0)
		x.Empty(got)
		x.Equal(map[string]int{"HolderService/Search": 1}, cost, said(cost))

		// And one that keeps one person reads that person's lists, each once.
		got, cost = ask(t, peopleDN, "(preferredLanguage=ko)", 0)
		x.Equal([]string{kimDN}, got)
		x.Equal(11, cost["HolderService/List"], said(cost))
		for _, list := range []string{"EmailService/List", "PhoneService/List", "GroupMembershipService/List", "TeamMembershipService/List", "RelationService/List"} {
			x.Equal(1, cost[list], "%s: %s", list, said(cost))
		}

		// A list the filter needs is read for everybody the rest of it keeps,
		// and the others only for whom it then keeps.
		got, cost = ask(t, peopleDN, "(&(departmentNumber=D3)(mobile=*0010))", 0)
		x.Equal([]string{"uid=c00010,ou=people,o=contoso"}, got)
		x.Equal(1, cost["EmailService/List"], said(cost))
		x.Equal(1, cost["GroupMembershipService/List"], said(cost))
	})

	t.Run("a page of a hundred people is four reads of their addresses and four of their numbers", func(t *testing.T) {
		// It was a hundred of each, asked for every person whether or not
		// they had any. A list takes thirty-two filters joined by `or`.
		x := require.New(t)
		// The second page, which is a hundred of the crowd and nobody else.
		_, cookie := page(t, c, peopleDN, "(objectClass=inetOrgPerson)", 100, nil, "uid", "mail", "mobile")
		x.NotEmpty(cookie)
		calls.since()
		d.recorded(t, &seen)

		got, _ := page(t, c, peopleDN, "(objectClass=inetOrgPerson)", 100, cookie, "uid", "mail", "mobile")
		cost := calls.since()
		rows, named := d.recorded(t, &seen)
		t.Logf("one page of a hundred, uid mail mobile: %d entries; roster %s; access record %s naming %d", len(got), said(cost), said(rows), named)
		x.Len(got, 100)
		for _, e := range got {
			x.Len(e.GetAttributeValues("mail"), 1, e.DN)
		}
		x.Equal(map[string]int{"HolderService/List": 1, "EmailService/List": 4, "PhoneService/List": 4}, cost, said(cost))

		// And everybody, in pages: what a client syncing an address book asks.
		all, cost := ask(t, peopleDN, "(objectClass=inetOrgPerson)", 100, "uid", "mail", "mobile")
		x.Len(all, crowded+3)
		x.Equal(11, cost["HolderService/List"], said(cost))
		x.LessOrEqual(cost["EmailService/List"], 44, said(cost))
		x.LessOrEqual(cost["PhoneService/List"], 44, said(cost))
		x.Equal(cost["HolderService/List"]+cost["EmailService/List"]+cost["PhoneService/List"], total(cost), said(cost))

		// With no attribute named it is every list of every person, which was
		// five calls a person: 5,046. Each list is four calls a page.
		all, cost = ask(t, peopleDN, "(objectClass=inetOrgPerson)", 100)
		x.Len(all, crowded+3)
		x.Equal(11, cost["HolderService/List"], said(cost))
		for _, list := range []string{"EmailService/List", "PhoneService/List", "GroupMembershipService/List", "TeamMembershipService/List", "RelationService/List"} {
			x.LessOrEqual(cost[list], 44, "%s: %s", list, said(cost))
		}
		x.LessOrEqual(total(cost), 250, said(cost))
	})

	t.Run("the members of a group are read once, and not asked again whether they are in it", func(t *testing.T) {
		// 303 calls for a group of a hundred: a `Holder.Get` a member, and
		// each member's two lists of places, to ask again a filter the plan
		// had just answered.
		x := require.New(t)
		got, cost := ask(t, peopleDN, "(memberOf=cn=crowd,ou=groups,o=contoso)", 0, "uid")
		x.Len(got, crowded/10)
		x.Equal(map[string]int{"GroupService/List": 1, "GroupMembershipService/List": 1, "HolderService/List": 4}, cost, said(cost))

		// With a filter beside it the same, and with `memberOf` asked for
		// each member's places are read -- in four calls of each list.
		got, cost = ask(t, peopleDN, "(&(objectClass=inetOrgPerson)(memberOf=cn=crowd,ou=groups,o=contoso)(departmentNumber=D3))", 0, "uid", "memberOf")
		x.NotEmpty(got)
		x.Equal(map[string]int{
			"GroupService/List": 1, "GroupMembershipService/List": 2, "HolderService/List": 4, "TeamMembershipService/List": 1,
		}, cost, said(cost))

		// And the group itself, with its hundred members.
		got, cost = ask(t, "ou=groups,o=contoso", "(cn=crowd)", 0, "member")
		x.Equal([]string{"cn=crowd,ou=groups,o=contoso"}, got)
		x.LessOrEqual(total(cost), 8, said(cost))
	})
}

// TestAnOrAnswersEverybodyOnce is a filter that is an `or`, which is read as
// each of its arms: somebody two arms read is in the answer once, in one
// search and in pages, and whoever an arm happened to read is answered by the
// arm that asked for them.
//
// An `or` used to be a read of everybody, so there was nobody to answer
// twice. Now that each arm is a read of its own (#281), an arm's read can
// hold somebody another arm asks for: `Search` is a contains, so the arm for
// `(displayName=Person 1)` reads Person 12, whom `(uid=p12)` beside it
// matches. They are sent by the first arm whose own part of the filter they
// match and by no other, which needs no memory of who was sent and so holds
// from one page to the next.
func TestAnOrAnswersEverybodyOnce(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	for i := range 23 {
		d.person(t, d.contoso, fmt.Sprintf("p%02d", i), fmt.Sprintf("Person %02d", i), &rstr.Profile{}, func(p *rstr.Profile) {
			if i%2 == 0 {
				p.SetDepartment("Even")
			}
		})
	}
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	person := func(i int) string { return fmt.Sprintf("uid=p%02d,ou=people,o=contoso", i) }
	once := func(t *testing.T, filter string, size uint32, want []string) {
		t.Helper()
		req := goldap.NewSearchRequest(peopleDN, goldap.ScopeSingleLevel, goldap.NeverDerefAliases, 0, 0, false, filter, []string{"1.1"}, nil)
		var res *goldap.SearchResult
		var err error
		if size > 0 {
			res, err = c.SearchWithPaging(req, size)
		} else {
			res, err = c.Search(req)
		}
		require.NoError(t, err, filter)
		got := dns(res.Entries)
		sort.Strings(got)
		sort.Strings(want)
		require.Equal(t, want, got, "%s, in pages of %d", filter, size)
	}

	// Twelve are even, twelve have a 1 in what they are called, and five are
	// both; p01 is asked for by name as well.
	var both []string
	for i := range 23 {
		if i%2 == 0 || strings.Contains(fmt.Sprintf("%02d", i), "1") {
			both = append(both, person(i))
		}
	}
	x.Len(both, 19)

	for _, size := range []uint32{0, 5, 100} {
		once(t, "(|(departmentNumber=Even)(cn=*1*)(uid=p01))", size, both)
		once(t, "(|(uid=p01)(cn=*1*)(departmentNumber=Even))", size, both)
		once(t, "(&(objectClass=inetOrgPerson)(|(departmentNumber=Even)(cn=*1*)))", size, both)

		// Whom an arm's read happens to hold is not that arm's to answer.
		once(t, "(|(displayName=Person 1)(uid=p12))", size, []string{person(12)})
		once(t, "(|(cn=Person 0*)(uid=p03)(uid=p14))", size, []string{
			person(0), person(1), person(2), person(3), person(4), person(5), person(6), person(7), person(8), person(9), person(14),
		})
	}

	// And an `or` beside a `not`, or of nothing, is what it was.
	once(t, "(|(uid=kim)(!(uid=*)))", 0, []string{kimDN})
	once(t, "(|)", 0, []string{})
	once(t, "(|(uid=kim)(uid=KIM)(mail=kim@contoso.example)(mail=kim.old@contoso.example))", 0, []string{kimDN})
}

// TestARoleThatNamesNoNumbersPublishesNone is a directory whose role was
// written before numbers and managers were published: it names neither
// `PhoneService/List` nor `RelationService/List`.
//
// Such a role publishes none of either and everything else as before, where a
// missing read otherwise fails the search with roster's refusal
// (`docs/ldap.md` § The key this process holds). The two lists are asked for a
// page of people at once now, and the refusal of one is no reason to fail the
// page: it is learned once a search and not asked again.
func TestARoleThatNamesNoNumbersPublishesNone(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	var older []string
	for _, m := range reads {
		if m != "/roster.PhoneService/List" && m != "/roster.RelationService/List" {
			older = append(older, m)
		}
	}
	key := d.service(t, d.contoso, "directory-as-it-was", older)

	c, calls := d.counted(t, func(c *ldap.Config) { perTenant(c); c.Keys = map[string]string{"contoso": key} })

	kim := one(t, c, kimDN)
	x.Equal("Minji", kim.GetAttributeValue("cn"))
	x.Equal([]string{"kim@contoso.example"}, kim.GetAttributeValues("mail"))
	x.NotEmpty(kim.GetAttributeValues("memberOf"))
	x.Empty(kim.GetAttributeValues("mobile"), "a role that names no read of numbers published one")
	x.Empty(kim.GetAttributeValues("telephoneNumber"))
	x.Empty(kim.GetAttributeValues("manager"), "a role that names no read of relations published a manager")

	calls.since()
	got := search(t, c, peopleDN, goldap.ScopeSingleLevel, "(objectClass=inetOrgPerson)")
	x.Contains(dns(got), kimDN)
	x.Contains(dns(got), leeDN)
	cost := calls.since()
	x.Equal(1, cost["PhoneService/List"], "asked once a search, and not again once refused: %s", said(cost))
	x.Equal(1, cost["RelationService/List"], said(cost))

	// And a filter on what is not published matches nobody.
	x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(mobile=*)", "1.1"))
	x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(manager="+leeDN+")", "1.1"))
}
