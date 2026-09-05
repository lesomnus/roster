package ldap_test

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/ldap"
	"github.com/lesomnus/roster/ldap/wire"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/vouch"
)

// The methods the directory's key holds: what a directory reads, and
// `Verify` for the deployments that bind with passwords.
var reads = []string{
	"/roster.TenantService/Get",
	"/roster.HolderService/Get", "/roster.HolderService/List", "/roster.HolderService/Search",
	"/roster.EmailService/Get", "/roster.EmailService/List",
	"/roster.GroupService/Get", "/roster.GroupService/List", "/roster.GroupMembershipService/List",
	"/roster.SiteService/Get", "/roster.SiteService/List",
	"/roster.TeamService/Get", "/roster.TeamService/List", "/roster.TeamMembershipService/List",
	"/roster.VouchService/Verify",
}

const (
	password = "correct horse battery staple"
	kimDN    = "uid=kim,ou=people,o=contoso"
	leeDN    = "uid=lee,ou=people,o=contoso"
	peopleDN = "ou=people,o=contoso"
	// The seeded administrator and the directory\'s own holder are people in
	// the tree too: a directory lists whoever roster has, machines included.
	adminDN     = "uid=admin,ou=people,o=contoso"
	directoryDN = "uid=directory,ou=people,o=contoso"
	contosoDN   = "o=contoso"
)

// deployment is a roster fronting two operators and the directory in front of
// both. contoso has kim (a password and an authenticator app, an app password
// for her NAS, two addresses of which one is verified), lee (a password, an
// app password) and park (disabled); fabrikam has a kim of its own.
type deployment struct {
	s      *cmd.Server
	roster string
	keys   map[string]string
	kimKey string // kim's app password
	leeKey string // lee's

	contoso, fabrikam pdid.Id
	kim, lee, park    []byte
}

func stand(t *testing.T) *deployment {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	seal := make([]byte, 32)
	_, err := rand.Read(seal)
	x.NoError(err)

	s, err := cmd.Build(ctx, cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
		Vouch:   cmd.VouchConfig{Keys: []string{"one:" + base64.StdEncoding.EncodeToString(seal)}},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))
	x.NoError(s.Control.Ent.Schema.Create(ctx))
	_, err = cmd.Seed(ctx, s, cmd.Seeding{Tenant: "contoso", Holder: "admin", Operator: "ops"})
	x.NoError(err)

	d := &deployment{s: s, keys: map[string]string{}}
	for _, alias := range []string{"contoso", "fabrikam"} {
		var tn *rstr.Tenant
		if alias == "contoso" {
			tn, err = s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{Ref: rstr.TenantRef_builder{Alias: proto.String(alias)}.Build()}.Build())
		} else {
			tn, err = s.Ungated.Tenant().Add(ctx, rstr.TenantAddRequest_builder{Alias: alias, Name: "Fabrikam Inc"}.Build())
		}
		x.NoError(err)
		id, err := pdid.From(tn.GetId())
		x.NoError(err)
		if alias == "contoso" {
			d.contoso = id
		} else {
			d.fabrikam = id
		}
		d.keys[alias] = d.service(t, id, "directory", reads)
	}

	d.kim = d.person(t, d.contoso, "kim", "Kim Minji", &rstr.Profile{}, func(p *rstr.Profile) {
		p.SetDisplayName("Minji")
		p.SetDepartment("Platform")
		p.SetEmployeeNo("1001")
		p.SetLocale("ko")
	})
	d.lee = d.person(t, d.contoso, "lee", "Lee Junho", &rstr.Profile{}, func(p *rstr.Profile) {
		p.SetDepartment("Payroll")
		p.SetEmployeeNo("1002")
	})
	d.park = d.person(t, d.contoso, "park", "Park Left", nil, nil)
	d.person(t, d.fabrikam, "kim", "Kim Other", nil, nil)

	// kim: a verified address and an unverified one, a password, and an
	// authenticator app confirmed by one code.
	d.email(t, d.kim, "kim@contoso.example", true)
	d.email(t, d.kim, "kim.old@contoso.example", false)
	d.setPassword(t, d.kim)
	res, err := s.Ungated.Credential().Enrol(ctx, rstr.CredentialEnrolRequest_builder{
		Ref: rstr.HolderRef_builder{Id: d.kim}.Build(), Kind: vouch.KindTotp,
	}.Build())
	x.NoError(err)
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(res.GetSeed())
	x.NoError(err)

	// lee: a password. park: gone.
	d.setPassword(t, d.lee)
	_, err = s.Ungated.Holder().Disable(ctx, rstr.HolderDisableRequest_builder{Ref: rstr.HolderRef_builder{Id: d.park}.Build()}.Build())
	x.NoError(err)

	// App passwords: a key on the person's own row, `Me.Get` and nothing else.
	d.kimKey = d.key(t, d.kim, "nas", []string{"/roster.MeService/Get"})
	d.leeKey = d.key(t, d.lee, "jenkins", []string{"/roster.MeService/Get"})

	// roster on the wire.
	g, err := s.Grpc(ctx, cmd.Config{})
	x.NoError(err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(func() { g.Stop() })
	d.roster = l.Addr().String()

	// Confirm kim's authenticator over the wire, as the account app would:
	// until a code has been verified against it, it is a seed and not a factor.
	conn := dialRoster(t, d.roster)
	got, err := rstr.NewVouchServiceClient(conn).Verify(bearing(ctx, d.keys["contoso"]), rstr.VouchVerifyRequest_builder{
		Who: rstr.VouchWho_builder{Id: d.kim}.Build(), Kind: vouch.KindTotp,
		Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
	}.Build())
	x.NoError(err)
	// Not `ok`: she has a password too, so one factor proved is a sign-in
	// half done. `satisfied` is what says the code was accepted.
	x.Contains(got.GetSatisfied(), vouch.KindTotp, "the authenticator was not confirmed: %v", got)

	return d
}

func (d *deployment) service(t *testing.T, in pdid.Id, alias string, methods []string) string {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: in.Bytes()}.Build()

	h, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: alias}.Build())
	x.NoError(err)
	role, err := d.s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{Tenant: at, Alias: alias, Methods: methods}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role: rstr.RoleRef_builder{Id: role.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	return d.key(t, h.GetId(), alias, methods)
}

func (d *deployment) key(t *testing.T, holder []byte, alias string, methods []string) string {
	t.Helper()
	x := require.New(t)

	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	_, err = d.s.Ungated.ApiKey().Add(t.Context(), rstr.ApiKeyAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: holder}.Build(), Alias: alias, Secret: sum, Methods: methods,
	}.Build())
	x.NoError(err)

	return token
}

func (d *deployment) person(t *testing.T, in pdid.Id, alias, name string, p *rstr.Profile, fill func(*rstr.Profile)) []byte {
	t.Helper()
	if fill != nil {
		fill(p)
	}
	v, err := d.s.Ungated.Holder().Add(t.Context(), rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: in.Bytes()}.Build(), Alias: alias, Name: name, Profile: p,
	}.Build())
	require.NoError(t, err)

	return v.GetId()
}

func (d *deployment) email(t *testing.T, holder []byte, address string, verified bool) {
	t.Helper()
	req := rstr.EmailAddRequest_builder{Holder: rstr.HolderRef_builder{Id: holder}.Build(), Address: address}
	if verified {
		req.DateVerified = timestamppb.Now()
	}
	_, err := d.s.Ungated.Email().Add(t.Context(), req.Build())
	require.NoError(t, err)
}

func (d *deployment) setPassword(t *testing.T, holder []byte) {
	t.Helper()
	_, err := d.s.Ungated.Credential().Set(t.Context(), rstr.CredentialSetRequest_builder{
		Ref: rstr.HolderRef_builder{Id: holder}.Build(), Secret: []byte(password),
	}.Build())
	require.NoError(t, err)
}

// serve stands the directory up in the mode given and dials it.
func (d *deployment) serve(t *testing.T, mode ldap.Mode, with func(*ldap.Config)) *goldap.Conn {
	t.Helper()
	x := require.New(t)

	cfg := ldap.Config{Roster: d.roster, Insecure: true, Keys: d.keys, Bind: mode}
	if with != nil {
		with(&cfg)
	}
	dir, err := ldap.New(t.Context(), cfg)
	x.NoError(err)
	t.Cleanup(func() { dir.Close() })

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	s := &wire.Server{Handler: dir.Handler()}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { l.Close(); s.Close() })

	c, err := goldap.DialURL("ldap://" + l.Addr().String())
	x.NoError(err)
	t.Cleanup(func() { c.Close() })

	return c
}

func search(t *testing.T, c *goldap.Conn, base string, scope int, filter string, attrs ...string) []*goldap.Entry {
	t.Helper()
	res, err := c.Search(goldap.NewSearchRequest(base, scope, goldap.NeverDerefAliases, 0, 0, false, filter, attrs, nil))
	require.NoError(t, err, filter)

	return res.Entries
}

func dns(entries []*goldap.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.DN
	}

	return out
}

func TestAnAppPasswordBindsItsOwnerAndNobodyElse(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := d.serve(t, ldap.BindKey, nil)

	x.NoError(c.Bind(kimDN, d.kimKey))
	who, err := c.WhoAmI(nil)
	x.NoError(err)
	x.Equal("dn:"+kimDN, who.AuthzID)

	for name, try := range map[string]struct{ dn, pw string }{
		"somebody else's key":           {kimDN, d.leeKey},
		"a key that is not one":         {kimDN, "rt_" + strings.Repeat("x", 40)},
		"the wrong suffix":              {"uid=kim,ou=people,o=fabrikam", d.kimKey},
		"a name that is not a person's": {"cn=kim,ou=people,o=contoso", d.kimKey},
		"the directory's own key":       {kimDN, d.keys["contoso"]},
	} {
		t.Run(name, func(t *testing.T) {
			err := c.Bind(try.dn, try.pw)
			require.True(t, goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials), "%v", err)
		})
	}

	t.Run("an unauthenticated bind is refused rather than taken as anonymous", func(t *testing.T) {
		err := c.UnauthenticatedBind(kimDN)
		require.True(t, goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials), "%v", err)
	})

	t.Run("and a password is refused in this mode, with a hint", func(t *testing.T) {
		x := require.New(t)
		err := c.Bind(leeDN, password)
		x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials), "%v", err)
		x.Contains(err.Error(), "app password")
	})
}

func TestAPasswordBindStopsAtASecondFactor(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := d.serve(t, ldap.BindPassword, nil)

	x.NoError(c.Bind(leeDN, password), "lee has a password and nothing else")

	err := c.Bind(leeDN, "not it")
	x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials), "%v", err)

	// kim's password is right, and she has an authenticator app: roster
	// answers a continuation rather than `ok`, and there is no second form
	// here to spend it on. Refused, with the same code a wrong password gets.
	err = c.Bind(kimDN, password)
	x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials), "a second factor was walked around: %v", err)
	x.Contains(err.Error(), "second factor")

	t.Run("in password mode an app password is refused", func(t *testing.T) {
		err := c.Bind(kimDN, d.kimKey)
		require.True(t, goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials), "%v", err)
	})

	t.Run("and either takes both", func(t *testing.T) {
		x := require.New(t)
		c := d.serve(t, ldap.BindEither, nil)
		x.NoError(c.Bind(leeDN, password))
		x.NoError(c.Bind(kimDN, d.kimKey))
		err := c.Bind(kimDN, password)
		x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials), "%v", err)
	})
}

func TestASuffixIsATenant(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := d.serve(t, ldap.BindKey, nil)

	root := search(t, c, "", goldap.ScopeBaseObject, "(objectClass=*)", "+")
	x.Len(root, 1)
	x.ElementsMatch([]string{"o=contoso", "o=fabrikam"}, root[0].GetAttributeValues("namingContexts"))

	x.NoError(c.Bind(kimDN, d.kimKey))

	contoso := search(t, c, contosoDN, goldap.ScopeWholeSubtree, "(uid=kim)", "entryUUID")
	x.Equal([]string{kimDN}, dns(contoso))
	fabrikam := search(t, c, "o=fabrikam", goldap.ScopeWholeSubtree, "(uid=kim)", "entryUUID")
	x.Equal([]string{"uid=kim,ou=people,o=fabrikam"}, dns(fabrikam))
	x.NotEqual(contoso[0].GetAttributeValue("entryUUID"), fabrikam[0].GetAttributeValue("entryUUID"), "two kims, one row")

	// And the whole server is both, each under its own name.
	all := search(t, c, "", goldap.ScopeWholeSubtree, "(objectClass=inetOrgPerson)", "uid")
	x.ElementsMatch([]string{kimDN, leeDN, adminDN, directoryDN, "uid=kim,ou=people,o=fabrikam", "uid=directory,ou=people,o=fabrikam"}, dns(all))

	t.Run("a suffix renamed is the same tenant", func(t *testing.T) {
		x := require.New(t)
		c := d.serve(t, ldap.BindKey, func(c *ldap.Config) { c.Bases = map[string]string{"contoso": "dc=contoso,dc=example"} })
		x.NoError(c.Bind("uid=kim,ou=people,dc=contoso,dc=example", d.kimKey))
		got := search(t, c, "dc=contoso,dc=example", goldap.ScopeWholeSubtree, "(uid=lee)", "cn")
		x.Equal([]string{"uid=lee,ou=people,dc=contoso,dc=example"}, dns(got))
		err := c.Bind(kimDN, d.kimKey)
		x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials), "the old suffix still binds: %v", err)
	})

	t.Run("a base outside every suffix is no such object", func(t *testing.T) {
		_, err := c.Search(goldap.NewSearchRequest("o=nobody", goldap.ScopeWholeSubtree, goldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", nil, nil))
		require.True(t, goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject), "%v", err)
	})

	t.Run("and nothing below the root is read unbound", func(t *testing.T) {
		c := d.serve(t, ldap.BindKey, nil)
		_, err := c.Search(goldap.NewSearchRequest(contosoDN, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", nil, nil))
		require.True(t, goldap.IsErrorWithCode(err, goldap.LDAPResultInsufficientAccessRights), "%v", err)
	})
}

func TestAFilterIsARosterRead(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	for filter, want := range map[string][]string{
		"(uid=kim)":                                    {kimDN},
		"(uid=KIM)":                                    {kimDN},
		"(cn=*min*)":                                   {kimDN},
		"(cn=Kim*)":                                    {kimDN},
		"(displayName=minji)":                          {kimDN},
		"(departmentNumber=Payroll)":                   {leeDN},
		"(employeeNumber=1001)":                        {kimDN},
		"(mail=kim@contoso.example)":                   {kimDN},
		"(mail=KIM@contoso.example)":                   {kimDN},
		"(mail=kim.old@contoso.example)":               {},
		"(mail=*)":                                     {kimDN},
		"(&(objectClass=inetOrgPerson)(!(mail=*)))":    {leeDN, adminDN, directoryDN},
		"(|(uid=lee)(cn~=nobody))":                     {leeDN},
		"(cn~=kim)":                                    {},
		"(&(uid=kim)(departmentNumber=Payroll))":       {},
		"(objectClass=inetOrgPerson)":                  {kimDN, leeDN, adminDN, directoryDN},
		"(uid=park)":                                   {},
		"(&(objectClass=person)(|(uid=kim)(uid=lee)))": {kimDN, leeDN},
		"(sn=Kim Minji)":                               {kimDN},
		"(preferredLanguage=ko)":                       {kimDN},
	} {
		t.Run(filter, func(t *testing.T) {
			got := search(t, c, peopleDN, goldap.ScopeSingleLevel, filter, "1.1")
			require.ElementsMatch(t, want, dns(got))
		})
	}

	t.Run("the attributes of a person", func(t *testing.T) {
		x := require.New(t)
		got := search(t, c, kimDN, goldap.ScopeBaseObject, "(objectClass=*)")
		x.Len(got, 1)
		e := got[0]
		x.ElementsMatch([]string{"top", "person", "organizationalPerson", "inetOrgPerson"}, e.GetAttributeValues("objectClass"))
		x.Equal("kim", e.GetAttributeValue("uid"))
		x.Equal("Kim Minji", e.GetAttributeValue("cn"))
		x.Equal("Kim Minji", e.GetAttributeValue("sn"))
		x.Equal("Minji", e.GetAttributeValue("displayName"))
		x.Equal([]string{"kim@contoso.example"}, e.GetAttributeValues("mail"), "an unverified address was published")
		x.Equal("1001", e.GetAttributeValue("employeeNumber"))
		x.Equal("Platform", e.GetAttributeValue("departmentNumber"))
		x.Empty(e.GetAttributeValues("entryUUID"), "an operational attribute answered unasked")

		ops := search(t, c, kimDN, goldap.ScopeBaseObject, "(objectClass=*)", "entryUUID", "uid")
		x.Len(ops, 1)
		x.NotEmpty(ops[0].GetAttributeValue("entryUUID"))
		x.Empty(ops[0].GetAttributeValues("cn"), "an attribute not asked for")
	})

	t.Run("the tree above the people", func(t *testing.T) {
		x := require.New(t)
		org := search(t, c, contosoDN, goldap.ScopeBaseObject, "(objectClass=*)")
		x.Equal([]string{contosoDN}, dns(org))
		x.Equal("contoso", org[0].GetAttributeValue("o"))

		units := search(t, c, contosoDN, goldap.ScopeSingleLevel, "(objectClass=organizationalUnit)", "ou")
		x.ElementsMatch([]string{"ou=people,o=contoso", "ou=groups,o=contoso", "ou=sites,o=contoso"}, dns(units))

		people := search(t, c, peopleDN, goldap.ScopeBaseObject, "(objectClass=*)", "ou")
		x.Equal([]string{peopleDN}, dns(people))

		_, err := c.Search(goldap.NewSearchRequest("uid=park,ou=people,o=contoso", goldap.ScopeBaseObject, goldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", nil, nil))
		x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject), "somebody disabled has an entry: %v", err)

		_, err = c.Search(goldap.NewSearchRequest("uid=nobody,ou=people,o=contoso", goldap.ScopeBaseObject, goldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", nil, nil))
		x.True(goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject), "%v", err)
	})
}

func TestPagingReadsEverybodyOnce(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	for i := range 23 {
		d.person(t, d.contoso, fmt.Sprintf("p%02d", i), fmt.Sprintf("Person %02d", i), &rstr.Profile{}, func(p *rstr.Profile) {
			if i%2 == 0 {
				p.SetDepartment("Even")
			}
		})
	}
	c := d.serve(t, ldap.BindKey, func(c *ldap.Config) { c.PageSize = 5 })
	x.NoError(c.Bind(kimDN, d.kimKey))

	once := func(t *testing.T, filter string, want int) {
		t.Helper()
		res, err := c.SearchWithPaging(goldap.NewSearchRequest(peopleDN, goldap.ScopeSingleLevel, goldap.NeverDerefAliases, 0, 0, false, filter, []string{"uid"}, nil), 5)
		require.NoError(t, err)
		seen := map[string]bool{}
		for _, e := range res.Entries {
			require.False(t, seen[e.DN], "%s twice", e.DN)
			seen[e.DN] = true
		}
		require.Len(t, res.Entries, want)
	}

	// 23 numbered, kim, lee, the seeded admin and the directory's own holder;
	// park is disabled and not counted.
	once(t, "(objectClass=inetOrgPerson)", 27)
	// A filter that drops every other row: pages are short, none is missed.
	once(t, "(departmentNumber=Even)", 12)
	// And one the filter alone can drop, since roster has no index for it.
	once(t, "(cn=*1*)", 12)

	t.Run("the client's size limit is honoured", func(t *testing.T) {
		_, err := c.Search(goldap.NewSearchRequest(peopleDN, goldap.ScopeSingleLevel, goldap.NeverDerefAliases, 7, 0, false, "(objectClass=*)", []string{"1.1"}, nil))
		require.True(t, goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded), "%v", err)
	})
}

func TestNothingSecretIsInTheTree(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := d.serve(t, ldap.BindEither, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	var all strings.Builder
	for _, e := range search(t, c, "", goldap.ScopeWholeSubtree, "(objectClass=*)", "*", "+") {
		all.WriteString(e.DN)
		for _, a := range e.Attributes {
			x.False(strings.EqualFold("userPassword", a.Name), "userPassword in the tree")
			all.WriteString(a.Name)
			all.WriteString(strings.Join(a.Values, ""))
		}
	}
	s := all.String()
	x.NotContains(s, password)
	x.NotContains(s, d.kimKey)
	x.NotContains(s, d.leeKey)
	x.NotContains(s, d.keys["contoso"])
	x.NotContains(s, "rt_")
}

func TestTheDisabledAreNotListed(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := d.serve(t, ldap.BindKey, nil)
	x.NoError(c.Bind(kimDN, d.kimKey))

	got := search(t, c, peopleDN, goldap.ScopeSingleLevel, "(objectClass=*)", "uid")
	x.ElementsMatch([]string{kimDN, leeDN, adminDN, directoryDN}, dns(got))

	// And disabling somebody is seen on the very next search: there is no
	// cache to wait out.
	_, err := d.s.Ungated.Holder().Disable(t.Context(), rstr.HolderDisableRequest_builder{Ref: rstr.HolderRef_builder{Id: d.lee}.Build()}.Build())
	x.NoError(err)
	got = search(t, c, peopleDN, goldap.ScopeSingleLevel, "(objectClass=*)", "uid")
	x.ElementsMatch([]string{kimDN, adminDN, directoryDN}, dns(got))
}

func TestLdapIsToldEverything(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	ctx := t.Context()

	_, err := ldap.New(ctx, ldap.Config{Roster: d.roster, Insecure: true})
	x.ErrorContains(err, "Keys")

	_, err = ldap.New(ctx, ldap.Config{Roster: d.roster, Insecure: true, Keys: d.keys, Bases: map[string]string{"nobody": "o=nobody"}})
	x.ErrorContains(err, "no key for it")

	_, err = ldap.New(ctx, ldap.Config{Roster: d.roster, Insecure: true, Keys: map[string]string{"fabrikam": d.keys["contoso"]}})
	x.ErrorContains(err, "cannot see", "a key for one tenant was taken as another's")
}

func dialRoster(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	c, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })

	return c
}

func bearing(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}
