package ldap_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/ldap"
)

// paged asks one page of a search at a scope, with a size limit, and answers
// its entries, the cookie that came back and the error.
func paged(c *goldap.Conn, base string, scope int, filter string, size uint32, cookie []byte, limit int, attrs ...string) ([]*goldap.Entry, []byte, error) {
	p := goldap.NewControlPaging(size)
	p.SetCookie(cookie)
	res, err := c.Search(goldap.NewSearchRequest(base, scope, goldap.NeverDerefAliases, limit, 0, false, filter, attrs, []goldap.Control{p}))
	if res == nil {
		return nil, nil, err
	}
	var next []byte
	if back, ok := goldap.FindControl(res.Controls, goldap.ControlTypePaging).(*goldap.ControlPaging); ok && back != nil {
		next = back.Cookie
	}

	return res.Entries, next, err
}

// pages reads a search to its end a page at a time, and answers how many
// entries each page held and every name, in the order they came.
func pages(t *testing.T, c *goldap.Conn, base string, scope int, filter string, size uint32) ([]int, []string) {
	t.Helper()
	var sizes []int
	var names []string
	var cookie []byte
	for {
		got, next, err := paged(c, base, scope, filter, size, cookie, 0, "1.1")
		require.NoError(t, err, filter)
		sizes = append(sizes, len(got))
		names = append(names, dns(got)...)
		if len(next) == 0 {
			return sizes, names
		}
		cookie = next
		require.Less(t, len(sizes), 1000, "a paged search that does not end")
	}
}

// crowdedTree is contoso with enough of everything for a page to end inside
// each part of its tree: twenty-three more people, a group of everybody,
// twelve more groups, and three more teams.
func crowdedTree(t *testing.T) *deployment {
	t.Helper()
	d := stand(t)
	everybody := d.group(t, d.contoso, "everybody", "")
	for _, who := range [][]byte{d.kim, d.lee} {
		d.inGroup(t, who, everybody)
	}
	for i := range 23 {
		d.inGroup(t, d.person(t, d.contoso, fmt.Sprintf("p%02d", i), fmt.Sprintf("Person %02d", i), nil, nil), everybody)
	}
	for i := range 12 {
		d.group(t, d.contoso, fmt.Sprintf("g%02d", i), "")
	}
	for i := range 3 {
		d.team(t, d.contoso, nil, fmt.Sprintf("t%02d", i), "")
	}

	return d
}

// TestAPageIsNoBiggerThanWasAsked is a paged search (RFC 2696) of each part
// of the tree, at a page size smaller than the part.
//
// A page could be bigger than the client asked, three ways. The first page
// from a suffix carried the fixed nodes of the tree beside a page of people:
// a page of 3 held 7. The groups were one page whatever their number, and so
// were the sites with their teams: a page of 5 held 14. And the people of a
// `(memberOf=…)` filter were one page: a page of 5 held 27, and `docs/ldap.md`
// said no page is bigger than a hundred, which a group of more than a hundred
// made untrue. A client that asks for pages has a buffer of that size.
//
// Every part is read through roster's own cursor, at no more than the page
// has room for, and a page ends where one of roster's does. What a search
// answers in pages is what it answers in one, each entry once.
func TestAPageIsNoBiggerThanWasAsked(t *testing.T) {
	x := require.New(t)
	d := crowdedTree(t)
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	const everybody = "cn=everybody,ou=groups,o=contoso"
	for _, v := range []struct {
		what, base string
		scope      int
		filter     string
	}{
		{"the whole suffix", contosoDN, goldap.ScopeWholeSubtree, "(objectClass=*)"},
		{"the root, which is the suffix", "", goldap.ScopeWholeSubtree, "(objectClass=*)"},
		{"the suffix's own children", contosoDN, goldap.ScopeSingleLevel, "(objectClass=*)"},
		{"the groups", "ou=groups,o=contoso", goldap.ScopeSingleLevel, "(objectClass=*)"},
		{"the groups of the whole suffix", contosoDN, goldap.ScopeWholeSubtree, "(objectClass=groupOfNames)"},
		{"the members of a group", peopleDN, goldap.ScopeSingleLevel, "(memberOf=" + everybody + ")"},
		{"the members of a group, from the suffix", contosoDN, goldap.ScopeWholeSubtree, "(&(objectClass=inetOrgPerson)(memberOf=" + everybody + "))"},
		{"the teams with no site", "ou=teams,o=contoso", goldap.ScopeSingleLevel, "(objectClass=*)"},
		{"the sites and what is under them", "ou=sites,o=contoso", goldap.ScopeWholeSubtree, "(objectClass=*)"},
		{"a site and what is under it", "ou=seoul,ou=sites,o=contoso", goldap.ScopeWholeSubtree, "(objectClass=*)"},
		{"an or of two reads", peopleDN, goldap.ScopeSingleLevel, "(|(memberOf=" + everybody + ")(cn=Person*)(uid=admin))"},
	} {
		t.Run(v.what, func(t *testing.T) {
			x := require.New(t)
			whole := dns(search(t, c, v.base, v.scope, v.filter, "1.1"))
			x.NotEmpty(whole)

			for _, size := range []uint32{1, 3, 5, 100} {
				sizes, names := pages(t, c, v.base, v.scope, v.filter, size)
				for i, n := range sizes {
					x.LessOrEqual(n, int(size), "page %d of %v, in pages of %d, was bigger than asked", i+1, sizes, size)
				}
				x.ElementsMatch(whole, names, "in pages of %d: %v", size, sizes)
				seen := map[string]bool{}
				for _, name := range names {
					x.False(seen[name], "%s twice, in pages of %d", name, size)
					seen[name] = true
				}
			}
		})
	}
}

// TestNoPageIsEmptyButTheLast is a paged search whose filter keeps little of
// what it reads.
//
// `(uid=kim)` from a suffix, in pages of a hundred, answered pages of 1, 0 and
// 0: the people were a page, the groups were a page whatever they held, and
// so were the sites. A client was sent back twice for nothing, and the manual
// said a page that would be empty reads on.
//
// A page ends when it is full, or where a read of roster ended that put
// something in it. So only a last page can be empty, and it is only where a
// page ended full and nothing after it matched.
func TestNoPageIsEmptyButTheLast(t *testing.T) {
	x := require.New(t)
	d := crowdedTree(t)
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	sizes, names := pages(t, c, contosoDN, goldap.ScopeWholeSubtree, "(uid=kim)", 100)
	x.Equal([]string{kimDN}, names)
	x.Equal([]int{1}, sizes, "answered pages of 1, 0 and 0; right is one page")

	for _, v := range []struct {
		base, filter string
		size         uint32
	}{
		{contosoDN, "(uid=kim)", 1},
		{contosoDN, "(uid=kim)", 3},
		{contosoDN, "(cn=g07)", 5},
		{contosoDN, "(|(cn=g07)(uid=p11)(cn=t01))", 2},
		{contosoDN, "(objectClass=organizationalUnit)", 2},
		{peopleDN, "(cn=Person 1*)", 3},
		{"", "(|(uid=lee)(cn=nomads))", 100},
		{contosoDN, "(cn=nobody at all)", 5},
	} {
		sizes, _ := pages(t, c, v.base, goldap.ScopeWholeSubtree, v.filter, v.size)
		for i, n := range sizes[:len(sizes)-1] {
			x.NotZero(n, "%s from %q in pages of %d: page %d of %v was empty and not the last", v.filter, v.base, v.size, i+1, sizes)
		}
	}
}

// TestASizeLimitIsOfTheWholeSearch is a size limit beside the paging control.
//
// The limit was counted for each page's request: a limit of 7 in pages of 5
// answered all twenty-six people with no error, where the same search in one
// answered 7 and `sizeLimitExceeded`. RFC 2696 § 3 has the page size "less
// than the sizeLimit value", pages "from the whole result set", and a server
// that ignores the control where the page is no smaller than the limit, "as
// the request can be satisfied in a single page". So the limit is of the
// search, however many pages it is read in.
//
// Nothing is kept between pages, so how many entries went before is in the
// cookie beside where roster was read to.
func TestASizeLimitIsOfTheWholeSearch(t *testing.T) {
	x := require.New(t)
	d := crowdedTree(t)
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))
	one := goldap.ScopeSingleLevel

	t.Run("a limit of 7 in pages of 5 is 7 and no more", func(t *testing.T) {
		x := require.New(t)
		seen := map[string]bool{}
		var cookie []byte
		for n := 1; ; n++ {
			got, next, err := paged(c, peopleDN, one, "(objectClass=*)", 5, cookie, 7, "1.1")
			x.LessOrEqual(len(got), 5)
			for _, e := range got {
				x.False(seen[e.DN], "%s twice", e.DN)
				seen[e.DN] = true
			}
			if err != nil {
				x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded), "%v", err)
				x.Empty(next, "a search that ended at its limit has no next page")
				x.Greater(n, 1, "the limit is more than a page holds")

				break
			}
			x.NotEmpty(next, "answered every page with no error, %d entries; right is sizeLimitExceeded once 7 are sent", len(seen))
			cookie = next
		}
		x.Len(seen, 7)
	})

	t.Run("and through the client's own paging, which is what a client runs", func(t *testing.T) {
		x := require.New(t)
		res, err := c.SearchWithPaging(goldap.NewSearchRequest(peopleDN, one, goldap.NeverDerefAliases, 7, 0, false, "(objectClass=*)", []string{"1.1"}, nil), 5)
		x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded), "answered all %d with no error: %v", entries(res), err)
	})

	t.Run("a limit the search does not reach is no error", func(t *testing.T) {
		x := require.New(t)
		res, err := c.SearchWithPaging(goldap.NewSearchRequest(peopleDN, one, goldap.NeverDerefAliases, 26, 0, false, "(objectClass=*)", []string{"1.1"}, nil), 5)
		x.NoError(err, "exactly as many as the limit is not more than the limit")
		x.Len(res.Entries, 26)

		res, err = c.SearchWithPaging(goldap.NewSearchRequest(peopleDN, one, goldap.NeverDerefAliases, 25, 0, false, "(objectClass=*)", []string{"1.1"}, nil), 5)
		x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded), "%v", err)
	})

	t.Run("a page no smaller than the limit is the search in one", func(t *testing.T) {
		x := require.New(t)
		for _, size := range []uint32{7, 50} {
			got, cookie, err := paged(c, peopleDN, one, "(objectClass=*)", size, nil, 7, "1.1")
			x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded), "a page of %d: %v", size, err)
			x.Len(got, 7)
			x.Empty(cookie)
		}
		got, cookie, err := paged(c, peopleDN, one, "(uid=kim)", 50, nil, 7, "1.1")
		x.NoError(err)
		x.Equal([]string{kimDN}, dns(got))
		x.Empty(cookie)
	})

	t.Run("and it is of every part of the tree together", func(t *testing.T) {
		x := require.New(t)
		// Four fixed nodes, twenty-six people, fourteen groups: a limit of 40
		// ends among the groups, having counted what came before them.
		var all []string
		var cookie []byte
		var err error
		for {
			var got []*goldap.Entry
			got, cookie, err = paged(c, contosoDN, goldap.ScopeWholeSubtree, "(objectClass=*)", 9, cookie, 40, "1.1")
			all = append(all, dns(got)...)
			if err != nil || len(cookie) == 0 {
				break
			}
		}
		x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded), "%v", err)
		x.Len(all, 40)
	})
}

func entries(res *goldap.SearchResult) int {
	if res == nil {
		return 0
	}

	return len(res.Entries)
}

// TestAPageOfNothingEndsThePaging is a search request with the cookie of the
// page before and a page size of zero, which is how a client says it wants no
// more (RFC 2696 § 3: "abandoned by the client sending a search request
// containing a pagedResultsControl with the size set to zero (0) and the
// cookie set to the last cookie returned by the server").
//
// It answered the rest: a size of zero was read as no size, and a client that
// had stopped was sent everybody it had not read. It answers nothing, and no
// cookie.
func TestAPageOfNothingEndsThePaging(t *testing.T) {
	x := require.New(t)
	d := crowdedTree(t)
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))
	one := goldap.ScopeSingleLevel

	got, cookie, err := paged(c, peopleDN, one, "(objectClass=*)", 5, nil, 0, "1.1")
	x.NoError(err)
	x.NotEmpty(got)
	x.NotEmpty(cookie)

	got, next, err := paged(c, peopleDN, one, "(objectClass=*)", 0, cookie, 0, "1.1")
	x.NoError(err)
	x.Empty(got, "answered the rest; right is nothing")
	x.Empty(next)

	// A page of zero that carries no cookie begins a search, at the page this
	// directory reads roster in.
	got, _, err = paged(c, peopleDN, one, "(objectClass=*)", 0, nil, 0, "1.1")
	x.NoError(err)
	x.Len(got, 26)
}

// TestACookieIsWhereRosterWasReadTo is what a paging cookie holds, which the
// manual says since a client is handed it (`docs/ldap.md` § Search).
//
// It is not opaque to anybody who looks, and it is not meant to keep a
// secret: the part of the tree, how many entries went before, and roster's
// own cursor, which is the moment the last row read was made and that row's
// identifier -- whether or not the row was shown. Nothing is kept for it on
// the server, so another connection and another replica read on from it, and
// each reads only the tenant it is bound in, since the wall is in the key
// and not in the cookie.
func TestACookieIsWhereRosterWasReadTo(t *testing.T) {
	x := require.New(t)
	d := crowdedTree(t)
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))
	one := goldap.ScopeSingleLevel

	// The first five rows roster lists are the administrator, the directory's
	// own holder, kim, lee and park: three of them people in the tree, and the
	// last of them somebody suspended.
	first, cookie, err := paged(c, peopleDN, one, "(objectClass=inetOrgPerson)", 5, nil, 0, "1.1")
	x.NoError(err)
	x.ElementsMatch([]string{adminDN, kimDN, leeDN}, dns(first))

	var held struct {
		Stage string `json:"s"`
		Sent  int    `json:"n"`
		After string `json:"a"`
	}
	x.NoError(json.Unmarshal(cookie, &held), "%s", cookie)
	x.Equal(3, held.Sent, "how many entries went before")
	x.Equal("p", held.Stage, "the part of the tree: the people")

	// Roster's cursor is the last row it read, shown or not: here park, who
	// is suspended and has no entry. The manual says so, since it is in a
	// client's hands; if this stops being what a cursor is, so does that.
	park, err := pdid.From(d.park)
	x.NoError(err)
	place, err := base64.RawURLEncoding.DecodeString(held.After)
	x.NoError(err, "roster's cursor is no longer what `docs/ldap.md` § Search says it is: %s", held.After)
	x.Contains(string(place), park.String(), "the identifier of the last row read")
	for _, word := range []string{"kim", "lee", "park", "admin", "contoso"} {
		x.NotContains(string(place), word, "a cursor names nobody")
	}

	// Another connection, through another directory process, reads on.
	other := d.serve(t, ldap.BindKey, nil)
	x.NoError(other.Bind(leeDN, d.leeKey))
	second, _, err := paged(other, peopleDN, one, "(objectClass=inetOrgPerson)", 3, cookie, 0, "1.1")
	x.NoError(err)
	x.Equal([]string{"uid=p00,ou=people,o=contoso", "uid=p01,ou=people,o=contoso", "uid=p02,ou=people,o=contoso"}, dns(second),
		"the page after, at a size of its own")

	// Somebody bound in another tenant reads their own tenant with it, and
	// nothing of this one.
	x.NoError(other.Bind(otherKimDN, d.otherKimKey))
	theirs, _, err := paged(other, "ou=people,o=fabrikam", one, "(objectClass=inetOrgPerson)", 3, cookie, 0, "1.1")
	x.NoError(err)
	for _, e := range theirs {
		x.Contains(e.DN, ",o=fabrikam")
	}

	// And a cookie nobody was given is refused where it is not a cursor, and
	// otherwise reads what a cursor there reads.
	_, _, err = paged(c, peopleDN, one, "(objectClass=*)", 3, []byte(`{"s":"p","a":"not a cursor"}`), 0, "1.1")
	x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultProtocolError), "%v", err)
	_, _, err = paged(c, contosoDN, goldap.ScopeWholeSubtree, "(objectClass=*)", 3, []byte(`{"s":"n","a":"not a number"}`), 0, "1.1")
	x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultProtocolError), "%v", err)
	got, next, err := paged(c, peopleDN, one, "(objectClass=*)", 3, []byte(`{"s":"no such part"}`), 0, "1.1")
	x.NoError(err)
	x.Empty(got)
	x.Empty(next)
}
