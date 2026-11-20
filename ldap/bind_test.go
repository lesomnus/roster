package ldap_test

import (
	"strings"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/ldap"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// answered is a bind's answer, whole: the code and the diagnostic.
func answered(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return "0"
	}
	var le *goldap.Error
	require.ErrorAs(t, err, &le, "not an answer of the directory's")
	if le.Err == nil {
		return goldap.LDAPResultCodeMap[le.ResultCode]
	}

	return goldap.LDAPResultCodeMap[le.ResultCode] + ": " + le.Err.Error()
}

// TestABindIsAnsweredTheSameWhicheverTenantItGuessed: a bind that does not
// work is answered by what was sent as the password and by nothing of the
// name.
//
// # What it was
//
// The name was read first, and a name under no suffix this process serves was
// refused there, with nothing. The sentence about the mode -- *this directory
// binds with app passwords* -- was said after, and so only for a suffix that
// is served: an unbound stranger binding as `uid=nobody,ou=people,o=<guess>`
// was answered 49 with the sentence for `o=contoso` and `o=fabrikam` and 49
// with nothing for `o=initech`; right is one answer. A suffix is a tenant's
// name, which is why the root DSE names none before a bind.
//
// # Where the sentence is still said
//
// Where it is decided by the deployment's mode and the shape of what was sent
// alone: every name is then answered with it, and it says which kind this
// directory binds and nothing else. And an app password is asked about
// whatever the name, since it is the key that says whose it is: a guess under
// a suffix that is served costs what one under any other costs.
func TestABindIsAnsweredTheSameWhicheverTenantItGuessed(t *testing.T) {
	d := stand(t)
	relay := d.relayed(t)

	// A key in shape, and nobody's.
	guess, _, err := keys.Mint(keys.PrefixTenant)
	require.NoError(t, err)

	names := []string{
		"uid=nobody,ou=people,o=contoso",  // a tenant this directory serves, and nobody of that name
		"uid=nobody,ou=people,o=fabrikam", // the other
		"uid=nobody,ou=people,o=initech",  // no tenant
		"uid=nobody,ou=people,o=CONTOSO",
		"cn=nobody,ou=people,o=contoso", // served, and not the shape of a person's name
		"uid=nobody,ou=groups,o=contoso",
		"o=contoso",
		"nobody",
	}

	for _, m := range []struct {
		name string
		mode ldap.Mode
		// said is what the refusal of each kind of secret says in this mode.
		ofPassword, ofKey string
	}{
		{"key", ldap.BindKey, "app password", ""},
		{"password", ldap.BindPassword, "", "binds with passwords"},
		{"either", ldap.BindEither, "", ""},
	} {
		t.Run("under "+m.name, func(t *testing.T) {
			c := clientOf(t, d.directoryOn(t, m.mode, func(c *ldap.Config) { c.Roster = relay.addr }, nil))

			for _, s := range []struct{ name, secret, said string }{
				{"a password", "not anybody's password", m.ofPassword},
				{"an app password", guess, m.ofKey},
			} {
				t.Run(s.name, func(t *testing.T) {
					x := require.New(t)

					first := answered(t, c.Bind(names[0], s.secret))
					x.True(strings.HasPrefix(first, "Invalid Credentials"), first)
					if s.said == "" {
						x.Equal("Invalid Credentials: ", first, "a refusal that says something of the name or the secret")
					} else {
						x.Contains(first, s.said, "the person reading their client's log is not told which kind this directory binds")
					}

					for _, name := range names[1:] {
						x.Equal(first, answered(t, c.Bind(name, s.secret)), "%s is answered apart from %s", name, names[0])
					}
				})
			}
		})
	}

	// And roster is asked about a key whichever name it came with, where
	// keys bind: what a guess costs says nothing of the suffix either.
	t.Run("an app password costs one call of roster whatever the name", func(t *testing.T) {
		x := require.New(t)
		c := clientOf(t, d.directoryOn(t, ldap.BindKey, func(c *ldap.Config) { c.Roster = relay.addr }, nil))

		for _, name := range names {
			before := relay.asked("")
			x.Error(c.Bind(name, guess))
			x.Equal(1, relay.asked("")-before, "a bind as %s", name)
		}
	})
}

// TestAPasswordIsTriedAsAPassword: where passwords bind, a password binds,
// whatever it begins with.
//
// The prefix `rt_` alone decided that what was sent was an app password. So
// somebody whose password begins with it never bound: under `--bind password`
// they were told an app password was given, and under `either` their password
// was sent to roster as a bearer token and never tried as a password; each
// answered 49, and right is success. An app password is known by its whole
// shape -- the prefix, and what roster mints after it -- and everything else
// is a password.
//
// What is a key by its shape is tried as a key and as nothing else, on
// purpose. A client still sending an app password its owner has revoked would
// otherwise count toward the lockout of that person's password at each try,
// and a person does not type forty-three characters of base64.
func TestAPasswordIsTriedAsAPassword(t *testing.T) {
	d := stand(t)
	ctx := t.Context()
	relay := d.relayed(t)

	const begins = "rt_is how my passphrase happens to begin"
	_, err := d.s.Ungated.Credential().Set(ctx, rstr.CredentialSetRequest_builder{
		Ref: rstr.HolderRef_builder{Id: d.lee}.Build(), Secret: []byte(begins),
	}.Build())
	require.NoError(t, err)

	failures := func(t *testing.T) int32 {
		t.Helper()
		cs, err := d.s.Ungated.Credential().List(ctx, rstr.CredentialListRequest_builder{
			Filters: []*rstr.CredentialFilter{rstr.CredentialFilter_builder{Holder: rstr.HolderRef_builder{Id: d.lee}.Build()}.Build()},
		}.Build())
		require.NoError(t, err)
		var n int32
		for _, v := range cs.GetItems() {
			n += v.GetFailures()
		}

		return n
	}

	for _, m := range []struct {
		name string
		mode ldap.Mode
	}{{"password", ldap.BindPassword}, {"either", ldap.BindEither}} {
		t.Run("under "+m.name, func(t *testing.T) {
			x := require.New(t)
			c := clientOf(t, d.directoryOn(t, m.mode, func(c *ldap.Config) { c.Roster = relay.addr }, nil))

			borne := relay.asked(rstr.MeService_Get_FullMethodName)
			x.NoError(c.Bind(leeDN, begins), "a password that begins as a key does was not tried as a password")
			x.Equal(borne, relay.asked(rstr.MeService_Get_FullMethodName), "a password was sent to roster as a key")

			// A key in shape that is nobody's -- one its owner revoked, say --
			// is not somebody's wrong password.
			revoked, _, err := keys.Mint(keys.PrefixTenant)
			x.NoError(err)
			tried := relay.asked(rstr.VouchService_Verify_FullMethodName)
			x.Error(c.Bind(leeDN, revoked))
			x.Equal(tried, relay.asked(rstr.VouchService_Verify_FullMethodName), "an app password was tried as a password")
			x.Zero(failures(t), "and counted toward a lockout")
		})
	}

	t.Run("and under key it is refused, without roster being asked", func(t *testing.T) {
		x := require.New(t)
		c := clientOf(t, d.directoryOn(t, ldap.BindKey, func(c *ldap.Config) { c.Roster = relay.addr }, nil))

		before := relay.asked("")
		err := c.Bind(leeDN, begins)
		x.Equal(uint16(goldap.LDAPResultInvalidCredentials), codeOf(t, err))
		x.ErrorContains(err, "app password")
		x.Equal(before, relay.asked(""))
	})
}

// TestANameIsReadAsRosterReadsIt: a name somebody typed is folded for an app
// password as it is for a password, and the connection is bound as the name
// the tree uses.
//
// `uid=LEE` bound by password, since roster folds a name somebody typed
// (`front.Alias`), and was refused by app password, where the directory
// compared the name with the key's holder exactly: answered 49; right is
// success. And WhoAmI answered the client's own spelling of what it had bound
// as -- `dn:UID=lee, OU=People, O=Contoso` -- which names no entry a search
// returns; right is the name the entry has.
func TestANameIsReadAsRosterReadsIt(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	c := d.serve(t, ldap.BindEither, func(c *ldap.Config) { c.Bases = map[string]string{"fabrikam": "dc=fabrikam,dc=example"} })

	whoami := func() string {
		who, err := c.WhoAmI(nil)
		x.NoError(err)

		return who.AuthzID
	}

	x.NoError(c.Bind("uid=LEE,ou=people,o=contoso", password), "the control: roster folds a name for a password")
	x.Equal("dn:"+leeDN, whoami())

	x.NoError(c.Bind("uid=LEE,ou=people,o=contoso", d.leeKey), "an app password was refused for the spelling of its holder's name")
	x.Equal("dn:"+leeDN, whoami())

	x.NoError(c.Bind("UID=lee, OU=People, O=Contoso", d.leeKey))
	x.Equal("dn:"+leeDN, whoami(), "the connection is bound as the client spelled it")
	got := search(t, c, whoami()[len("dn:"):], goldap.ScopeBaseObject, "(objectClass=*)", "uid")
	x.Equal([]string{leeDN}, dns(got), "what WhoAmI answers is not the name of an entry")

	x.NoError(c.Bind("UID=Lee , OU=PEOPLE , O=CONTOSO", password))
	x.Equal("dn:"+leeDN, whoami())

	// And under a suffix a deployment renamed, as the deployment wrote it.
	x.NoError(c.Bind("UID=Kim,OU=People,DC=Fabrikam,DC=Example", d.otherKimKey))
	x.Equal("dn:uid=kim,ou=people,dc=fabrikam,dc=example", whoami())
}

// TestATenantRenamedUnderTheDirectoryStillBindsByPassword: a tenant renamed
// while the directory runs goes on binding its people, by password as by app
// password, under the suffix the directory is serving.
//
// The alias was read at start and sent as `VouchWho.tenant` with every
// `Verify`. After a rename it named no tenant, so every password bind was
// refused -- 49 under the old suffix and under the new, uncounted and
// unlogged -- while an app password and a search, which go by the tenant's
// identifier, carried on. A bind goes by the identifier too: the directory
// asks as its holder in the tenant, and names the person alone.
//
// Which suffix is served is what the manual says it is, read at start: the
// old one until a restart, and the new one from then.
func TestATenantRenamedUnderTheDirectoryStillBindsByPassword(t *testing.T) {
	x := require.New(t)
	d := stand(t)
	ctx := t.Context()
	c := d.serve(t, ldap.BindEither, nil)
	x.NoError(c.Bind(leeDN, password), "the control: before the rename")

	tn, err := d.s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{Ref: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Tenant().Patch(ctx, rstr.TenantPatchRequest_builder{
		Ref: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Alias: proto.String("contoso-group"),
		DateUpdated: tn.GetDateUpdated(),
	}.Build())
	x.NoError(err)

	x.NoError(c.Bind(leeDN, d.leeKey), "the control: an app password goes by the tenant's identifier")
	x.NoError(c.Bind(leeDN, password), "a password bind was refused under the suffix the directory serves")
	got := search(t, c, contosoDN, goldap.ScopeWholeSubtree, "(uid=lee)", "uid")
	x.Equal([]string{leeDN}, dns(got))

	err = c.Bind("uid=lee,ou=people,o=contoso-group", password)
	x.Equal(uint16(goldap.LDAPResultInvalidCredentials), codeOf(t, err), "a suffix is read at start: %v", err)

	// A restart, and the new name is the suffix.
	again := d.serve(t, ldap.BindEither, nil)
	x.NoError(again.Bind("uid=lee,ou=people,o=contoso-group", password))
	err = again.Bind(leeDN, password)
	x.Equal(uint16(goldap.LDAPResultInvalidCredentials), codeOf(t, err), "%v", err)
}

// TestTwoTenantsAreNotGivenOneSuffix: a suffix is one tenant's, and a
// directory told otherwise does not start.
//
// `--base fabrikam=o=contoso` was accepted beside contoso's own `o=contoso`:
// the directory started, served `[o=contoso o=contoso]`, and nobody of
// fabrikam could bind, since a name under the suffix was always read as
// contoso's. Nothing said so. It is refused at start, naming both, as
// everything the directory is told and cannot do is.
func TestTwoTenantsAreNotGivenOneSuffix(t *testing.T) {
	d := stand(t)
	ctx := t.Context()

	for name, bases := range map[string]map[string]string{
		"another's default":           {"fabrikam": "o=contoso"},
		"spelled another way":         {"fabrikam": "O = Contoso"},
		"both given the same":         {"contoso": "dc=corp,dc=example", "fabrikam": "DC=Corp,DC=Example"},
		"each given the other's name": {"contoso": "o=fabrikam"},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			dir, err := ldap.New(ctx, ldap.Config{Roster: d.roster, Insecure: true, Key: d.deployed, Bases: bases})
			if err == nil {
				defer dir.Close()
			}
			x.Error(err, "started, serving %v", func() []string {
				if dir == nil {
					return nil
				}

				return dir.NamingContexts()
			}())
			x.ErrorContains(err, `"contoso"`)
			x.ErrorContains(err, `"fabrikam"`)
			x.ErrorContains(err, "suffix")
		})
	}

	// One suffix under another is two suffixes, and both are served.
	t.Run("and one under another is served", func(t *testing.T) {
		x := require.New(t)
		c := d.serve(t, ldap.BindKey, func(c *ldap.Config) { c.Bases = map[string]string{"fabrikam": "o=fabrikam,o=contoso"} })
		x.NoError(c.Bind(kimDN, d.kimKey))
		x.NoError(c.Bind("uid=kim,ou=people,o=fabrikam,o=contoso", d.otherKimKey))
	})
}
