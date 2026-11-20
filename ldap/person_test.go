package ldap_test

import (
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/roster/ldap"
	rstr "github.com/lesomnus/roster/rstr"
)

// called is what `MeService/Get` says somebody is called, asked with their own
// app password: the string a front door puts in a token's `name`.
func (d *deployment) called(t *testing.T, key string) string {
	t.Helper()
	got, err := rstr.NewMeServiceClient(dialRoster(t, d.roster)).Get(bearing(t.Context(), key), rstr.MeGetRequest_builder{}.Build())
	require.NoError(t, err)

	return got.GetName()
}

// renamed writes somebody's profile by the verb that renames them
// (`HolderService/Update`), which is the only one a port serves that does.
func (d *deployment) renamed(t *testing.T, who []byte, p *rstr.Profile) {
	t.Helper()
	ctx := t.Context()
	v, err := d.s.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{Ref: rstr.HolderRef_builder{Id: who}.Build()}.Build())
	require.NoError(t, err)
	_, err = d.s.Ungated.Holder().Update(ctx, rstr.HolderUpdateRequest_builder{
		Ref: rstr.HolderRef_builder{Id: who}.Build(), DateUpdated: v.GetDateUpdated(), Profile: p,
	}.Build())
	require.NoError(t, err)
}

// one is the single entry a base search of a name answers.
func one(t *testing.T, c *goldap.Conn, name string, attrs ...string) *goldap.Entry {
	t.Helper()
	got := search(t, c, name, goldap.ScopeBaseObject, "(objectClass=*)", attrs...)
	require.Len(t, got, 1, name)

	return got[0]
}

// TestSomebodyIsCalledWhatATokenCallsThem is `cn`, `sn` and `displayName`,
// which are what an address book shows and what a client's filter is written
// against.
//
// `cn` and `sn` were the name somebody was added with (`Holder.name`), which
// no served verb changes. So somebody renamed went on being `cn: Lee Junho`
// in every client while `MeService/Get`, a token's `name` and SCIM said
// `Lee Junho-Park`, and `(cn=Lee Junho-Park)` found nobody. And somebody added
// by their alias alone had no `cn` and no `sn` at all, before and after a
// display name was written, while claiming `person` and `inetOrgPerson`, which
// require both.
//
// `cn` is what somebody is called now: the display name where there is one,
// else the name they were added with, else the alias, so it is never missing.
// `sn` is the same string, since roster splits no name. `displayName` is the
// first two and absent when there is neither, which is a token's `name`
// exactly (#353). `givenName` is not published: half of a name roster does
// not split would be a guess.
func TestSomebodyIsCalledWhatATokenCallsThem(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	t.Run("a display name is what somebody is called", func(t *testing.T) {
		x := require.New(t)
		// kim was added as Kim Minji and is called Minji.
		e := one(t, c, kimDN)
		x.Equal([]string{"Minji"}, e.GetAttributeValues("cn"), "answered the name she was added with; right is what she is called now")
		x.Equal([]string{"Minji"}, e.GetAttributeValues("sn"))
		x.Equal([]string{"Minji"}, e.GetAttributeValues("displayName"))
		x.Equal(d.called(t, d.kimKey), e.GetAttributeValue("displayName"), "a token's name")
	})

	t.Run("with none, it is the name they were added with", func(t *testing.T) {
		x := require.New(t)
		e := one(t, c, leeDN)
		x.Equal([]string{"Lee Junho"}, e.GetAttributeValues("cn"))
		x.Equal([]string{"Lee Junho"}, e.GetAttributeValues("sn"))
		x.Equal([]string{"Lee Junho"}, e.GetAttributeValues("displayName"), "answered none; right is the name a token says")
		x.Equal(d.called(t, d.leeKey), e.GetAttributeValue("displayName"), "a token's name")
	})

	t.Run("somebody renamed is called the new name, and found by it", func(t *testing.T) {
		x := require.New(t)
		d.renamed(t, d.lee, rstr.Profile_builder{DisplayName: "Lee Junho-Park", Department: "Payroll", EmployeeNo: "1002"}.Build())

		e := one(t, c, leeDN)
		x.Equal([]string{"Lee Junho-Park"}, e.GetAttributeValues("cn"), "answered Lee Junho; right is the name they have now")
		x.Equal([]string{"Lee Junho-Park"}, e.GetAttributeValues("sn"))
		x.Equal([]string{"Lee Junho-Park"}, e.GetAttributeValues("displayName"))
		x.Equal(d.called(t, d.leeKey), e.GetAttributeValue("cn"), "a token's name")

		for _, f := range []string{"(cn=Lee Junho-Park)", "(sn=lee junho-park)", "(displayName=Lee Junho-Park)", "(cn=*junho-p*)", "(sn=Lee*Park)"} {
			x.Equal([]string{leeDN}, dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, f, "1.1")), f)
		}
		x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(cn=Lee Junho)", "1.1"), "the name they were added with is not what they are called")
	})

	t.Run("somebody added by alias alone is called their alias, and says no display name", func(t *testing.T) {
		x := require.New(t)
		choi := d.person(t, d.contoso, "choi", "", nil, nil)
		const choiDN = "uid=choi,ou=people,o=contoso"

		e := one(t, c, choiDN)
		x.Equal([]string{"choi"}, e.GetAttributeValues("cn"), "answered no cn, which person requires")
		x.Equal([]string{"choi"}, e.GetAttributeValues("sn"), "answered no sn, which person requires")
		x.Empty(e.GetAttributeValues("displayName"), "an alias is not what a token calls somebody")
		x.Equal(d.called(t, d.key(t, choi, "nas", []string{"/roster.MeService/Get"})), e.GetAttributeValue("displayName"), "a token's name, which is none")
		x.Equal([]string{choiDN}, dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(cn=choi)", "1.1")))

		d.renamed(t, choi, rstr.Profile_builder{DisplayName: "Choi Yuna"}.Build())
		e = one(t, c, choiDN)
		x.Equal([]string{"Choi Yuna"}, e.GetAttributeValues("cn"))
		x.Equal([]string{"Choi Yuna"}, e.GetAttributeValues("sn"))
		x.Equal([]string{"Choi Yuna"}, e.GetAttributeValues("displayName"))
	})

	t.Run("so everybody has a cn and an sn, and nobody a givenName", func(t *testing.T) {
		x := require.New(t)
		everybody := dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(objectClass=inetOrgPerson)", "1.1"))
		x.ElementsMatch(everybody, dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(&(cn=*)(sn=*))", "1.1")))
		x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(!(cn=*))", "1.1"))
		x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(givenName=*)", "1.1"))
		for _, e := range search(t, c, peopleDN, goldap.ScopeSingleLevel, "(objectClass=inetOrgPerson)") {
			x.Len(e.GetAttributeValues("cn"), 1, e.DN)
			x.Equal(e.GetAttributeValues("cn"), e.GetAttributeValues("sn"), e.DN)
		}
	})
}

// TestTheFirstMailIsTheAddressATokenSays is somebody with two proved
// addresses, of which the one written second was proved first.
//
// `mail`'s values were in the order the addresses were written. A token's
// `email` is the address proved first (`server/me`), and that is the one a
// relying party keys an account on; a client that takes the first `mail`, as
// most do, took another address than every other way in says is theirs.
//
// The values are in the order a token reads them: by when each was proved,
// and two proved at one instant in the order they were written. An address
// nobody proved is still not published.
func TestTheFirstMailIsTheAddressATokenSays(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	ctx := t.Context()
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	// Written in this order, and proved in another: the second an hour before
	// the first, the third and fourth at one instant after both, the fifth by
	// nobody.
	at := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	for _, v := range []struct {
		address string
		proved  *time.Time
	}{
		{"lee.written-first@contoso.example", ptr(at.Add(time.Hour))},
		{"lee.proved-first@contoso.example", ptr(at)},
		{"lee.third@contoso.example", ptr(at.Add(2 * time.Hour))},
		{"lee.fourth@contoso.example", ptr(at.Add(2 * time.Hour))},
		{"lee.claimed@contoso.example", nil},
	} {
		req := rstr.EmailAddRequest_builder{Holder: rstr.HolderRef_builder{Id: d.lee}.Build(), Address: v.address}
		if v.proved != nil {
			req.DateVerified = timestamppb.New(*v.proved)
		}
		_, err := d.s.Ungated.Email().Add(ctx, req.Build())
		x.NoError(err)
	}

	me, err := rstr.NewMeServiceClient(dialRoster(t, d.roster)).Get(bearing(ctx, d.leeKey), rstr.MeGetRequest_builder{}.Build())
	x.NoError(err)
	var token []string
	for _, e := range me.GetEmails() {
		if e.GetDateVerified() != nil {
			token = append(token, e.GetAddress())
		}
	}
	x.Len(token, 4, "what a token reads, which this test holds mail to")
	x.Equal([]string{"lee.proved-first@contoso.example", "lee.written-first@contoso.example"}, token[:2])
	x.ElementsMatch([]string{"lee.third@contoso.example", "lee.fourth@contoso.example"}, token[2:],
		"two proved at one instant, which a token orders by their rows and this orders as a token does")

	x.Equal(token, one(t, c, leeDN, "mail").GetAttributeValues("mail"),
		"answered the order written; right is the order proved, so that the first is a token's email")
	// And the same from a search that reads a page of people.
	for _, e := range search(t, c, peopleDN, goldap.ScopeSingleLevel, "(objectClass=inetOrgPerson)", "mail") {
		if e.DN == leeDN {
			x.Equal(token, e.GetAttributeValues("mail"))
		}
	}
	x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(mail=lee.claimed@contoso.example)", "1.1"), "an address nobody proved was found")
}

func ptr[T any](v T) *T { return &v }

// TestAMachineIsNotAPersonInTheDirectory is who `ou=people` holds.
//
// It held every holder of the tenant: the directory's own, the one each front
// door is answered as, and whatever else a file or a provisioner declared,
// each an `inetOrgPerson` with a `uid`. A client that imports users imported
// them, and a client that lists a group listed them among its members. The
// SCIM endpoint answers the same question -- who are this tenant's people --
// and leaves out a row that is declared and its own (`scim/user.go`).
//
// So does this, by the same test: a holder that carries `roster.declared`,
// and the holder this directory's key is answered as, are in no listing, are
// no such object by name, and are named by no group and as nobody's manager.
// A tenant's first administrator carries no such label and is a person.
func TestAMachineIsNotAPersonInTheDirectory(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	// A front door's holder, as its provisioner writes it (`cli/login.go`),
	// which somebody then put in a group and said kim reports to; and the
	// directory's own, put in the same group.
	door, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: at, Alias: "login-app", Labels: map[string]string{"roster.declared": "config: login"},
	}.Build())
	x.NoError(err)
	own, err := d.s.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{Alias: ptr("directory"), Tenant: at}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	apps := d.group(t, d.contoso, "apps", "Everything that signs in")
	d.inGroup(t, door.GetId(), apps)
	d.inGroup(t, own.GetId(), apps)
	d.inGroup(t, d.kim, apps)
	_, err = d.s.Ungated.Relation().Add(ctx, rstr.RelationAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: d.lee}.Build(), Other: rstr.HolderRef_builder{Id: door.GetId()}.Build(),
		Labels: map[string]string{"manager": ""},
	}.Build())
	x.NoError(err)

	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	const (
		doorDN = "uid=login-app,ou=people,o=contoso"
		appsDN = "cn=apps,ou=groups,o=contoso"
	)

	t.Run("no listing holds one", func(t *testing.T) {
		x := require.New(t)
		for _, filter := range []string{"(objectClass=inetOrgPerson)", "(objectClass=*)", "(uid=*)", "(|(uid=login-app)(uid=directory)(uid=kim))", "(cn=*)"} {
			got := dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, filter, "uid"))
			x.NotContains(got, doorDN, "%s answered a front door's holder", filter)
			x.NotContains(got, directoryDN, "%s answered the directory's own holder", filter)
			x.Contains(got, kimDN, filter)
		}
		x.ElementsMatch([]string{kimDN, leeDN, adminDN}, dns(search(t, c, contosoDN, goldap.ScopeWholeSubtree, "(objectClass=inetOrgPerson)", "uid")),
			"a tenant's first administrator is a person, and stays")
		x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(uid=login-app)", "uid"))
		x.Empty(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(uid=directory)", "uid"))
	})

	t.Run("nor is one read by name", func(t *testing.T) {
		for _, name := range []string{doorDN, directoryDN} {
			_, err := c.Search(goldap.NewSearchRequest(name, goldap.ScopeBaseObject, goldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", nil, nil))
			require.True(t, goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject), "%s: answered an entry; right is no such object: %v", name, err)
		}
	})

	t.Run("nor named by a group, or as somebody's manager", func(t *testing.T) {
		x := require.New(t)
		x.Equal([]string{kimDN}, one(t, c, appsDN, "member").GetAttributeValues("member"), "a group named a machine among its members")
		x.Equal([]string{kimDN}, dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(memberOf="+appsDN+")", "1.1")))
		x.Empty(one(t, c, leeDN, "manager").GetAttributeValues("manager"), "somebody was said to report to a machine")
	})
}

// TestAnEmployeeNumberMindsCase is `employeeNumber`, the one attribute here
// whose value is compared exactly.
//
// roster's `Search` compares an employee number exactly and promises so
// (`docs/baseline.md`), and that is the read `(employeeNumber=…)` becomes. The
// filter, asked of each entry afterwards, compared it without regard to case.
// So one filter meant two things: alone it was the plan, and `ab07` found
// nobody; inside an `or`, where everybody is read, the same words found the
// person whose number is `AB07`.
//
// The entry agrees with the plan: an employee number is the characters it is
// written in, in an equality and in a substring alike.
func TestAnEmployeeNumberMindsCase(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	d.person(t, d.contoso, "seo", "Seo Jiwoo", &rstr.Profile{}, func(p *rstr.Profile) { p.SetEmployeeNo("AB07") })
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	const seoDN = "uid=seo,ou=people,o=contoso"
	for filter, want := range map[string][]string{
		"(employeeNumber=AB07)":                       {seoDN},
		"(employeeNumber=ab07)":                       {},
		"(|(employeeNumber=AB07)(uid=nobody))":        {seoDN},
		"(|(employeeNumber=ab07)(uid=nobody))":        {},
		"(|(employeeNumber=ab07)(objectClass=x))":     {},
		"(&(uid=seo)(employeeNumber=ab07))":           {},
		"(&(uid=seo)(!(employeeNumber=ab07)))":        {seoDN},
		"(&(uid=seo)(employeeNumber=AB*))":            {seoDN},
		"(&(uid=seo)(employeeNumber=ab*))":            {},
		"(&(objectClass=person)(employeeNumber=*07))": {seoDN},
	} {
		t.Run(filter, func(t *testing.T) {
			require.ElementsMatch(t, want, dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, filter, "1.1")))
		})
	}
}

// TestAFilterThatAsksForNothingAnswersNobody is an equality or a substring
// with nothing in it: `(cn=)`, `(departmentNumber=)`, `(cn=* *)`.
//
// Each was planned into a `Holder.Search` for nothing, which roster refuses
// ("what to look for; nothing is a List"), and the refusal came back as
// `protocolError` with roster's sentence -- for a filter that is well formed,
// from a client that had sent whatever its user left in a box. `(uid=)`
// beside them answered nobody.
//
// They all answer nobody, with success and without a read: no attribute here
// has an empty value, so nothing can equal one, and a fragment of spaces alone
// is nothing to look for.
func TestAFilterThatAsksForNothingAnswersNobody(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	for _, filter := range []string{
		"(cn=)", "(sn=)", "(displayName=)", "(departmentNumber=)", "(employeeNumber=)", "(uid=)", "(mail=)", "(memberOf=)",
		"(cn= )", "(cn=* *)", "(uid=* *)", "(displayName=*  *)",
		"(&(objectClass=inetOrgPerson)(cn=))", "(|(cn=)(departmentNumber=))",
	} {
		t.Run(filter, func(t *testing.T) {
			res, err := c.Search(goldap.NewSearchRequest(peopleDN, goldap.ScopeSingleLevel, goldap.NeverDerefAliases, 0, 0, false, filter, []string{"1.1"}, nil))
			require.NoError(t, err, "answered an error for a filter that is well formed; right is nobody")
			require.Empty(t, res.Entries)
		})
	}

	// And beside something that is asked for, it is nothing: the rest of the
	// filter is answered as it would be alone.
	x.Equal([]string{kimDN}, dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(|(cn=)(uid=kim))", "1.1")))
	x.Equal([]string{kimDN}, dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(&(uid=kim)(!(cn=)))", "1.1")))
	x.Equal([]string{leeDN}, dns(search(t, c, peopleDN, goldap.ScopeSingleLevel, "(cn=* *o)", "1.1")), "a fragment beside spaces is still looked for")
}

// TestAClientKeysOnEntryUUID is what stays the same about somebody, which is
// what a client that keeps an account for them has to key it on
// (`docs/ldap.md` § A person).
//
// Their `uid` and their DN are what they are called, and move when they are
// renamed; and a name comes free when somebody is erased, so the next person
// to have it is the same DN. A client keyed on either hands the newcomer the
// account of whoever had the name before. `entryUUID` is roster's own
// identifier for the row: it never changes and is never used again. It is
// operational, so it is answered only when asked for by name, and it is
// found by one read.
func TestAClientKeysOnEntryUUID(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	ctx := t.Context()
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	const junhoDN = "uid=junho,ou=people,o=contoso"
	was := one(t, c, leeDN, "entryUUID", "uid").GetAttributeValue("entryUUID")
	x.NotEmpty(was)
	x.Empty(one(t, c, leeDN).GetAttributeValue("entryUUID"), "an operational attribute is answered only when asked for")
	x.Empty(one(t, c, leeDN, "*").GetAttributeValue("entryUUID"))
	x.Equal(was, one(t, c, leeDN, "+").GetAttributeValue("entryUUID"))

	// lee is written as junho from now on.
	lee, err := d.s.Ungated.Holder().Get(ctx, rstr.HolderGetRequest_builder{Ref: rstr.HolderRef_builder{Id: d.lee}.Build()}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Holder().Realias(ctx, rstr.HolderRealiasRequest_builder{
		Ref: rstr.HolderRef_builder{Id: d.lee}.Build(), DateUpdated: lee.GetDateUpdated(), Alias: ptr("junho"),
	}.Build())
	x.NoError(err)

	_, err = c.Search(goldap.NewSearchRequest(leeDN, goldap.ScopeBaseObject, goldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", nil, nil))
	x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject), "the name somebody had is no longer theirs: %v", err)
	renamed := one(t, c, junhoDN, "entryUUID", "uid")
	x.Equal("junho", renamed.GetAttributeValue("uid"))
	x.Equal(was, renamed.GetAttributeValue("entryUUID"), "a rename moved the identifier a client keys on")
	x.Equal([]string{junhoDN}, dns(search(t, c, contosoDN, goldap.ScopeWholeSubtree, "(entryUUID="+was+")", "1.1")),
		"and they are found by it, whatever they are called")

	// And the name is somebody else's as soon as somebody else has it.
	d.person(t, d.contoso, "lee", "Lee Another", nil, nil)
	another := one(t, c, leeDN, "entryUUID").GetAttributeValue("entryUUID")
	x.NotEmpty(another)
	x.NotEqual(was, another, "two people who have had one name are one entryUUID")

	// As it is after an erase: the name comes free, and the identifier is
	// never anybody's again.
	_, err = d.s.Ungated.Holder().Erase(ctx, rstr.HolderRef_builder{Id: d.lee}.Build())
	x.NoError(err)
	x.Empty(search(t, c, contosoDN, goldap.ScopeWholeSubtree, "(entryUUID="+was+")", "1.1"))
	d.person(t, d.contoso, "junho", "Junho Again", nil, nil)
	again := one(t, c, junhoDN, "entryUUID").GetAttributeValue("entryUUID")
	x.NotEqual(was, again)
	x.NotEqual(another, again)
}
