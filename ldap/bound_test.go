package ldap_test

import (
	"sync"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// bound is somebody with both things a bind may carry: a password, and an app
// password with the row it is, so that a test can take either away.
type bound struct {
	dn    string
	id    []byte
	key   string
	keyID []byte
}

func (d *deployment) binder(t *testing.T, alias string) bound {
	t.Helper()
	x := require.New(t)

	v := bound{dn: "uid=" + alias + ",ou=people,o=contoso", id: d.person(t, d.contoso, alias, "Somebody "+alias, nil, nil)}
	d.setPassword(t, v.id)

	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	k, err := d.s.Ungated.ApiKey().Add(t.Context(), rstr.ApiKeyAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: v.id}.Build(), Alias: "nas", Secret: sum,
		Methods: []string{rstr.MeService_Get_FullMethodName},
	}.Build())
	x.NoError(err)
	v.key, v.keyID = token, k.GetId()

	return v
}

// everybody is a search of the whole of `ou=people`, with `mail`: what a
// connection that may read reads.
func everybody(c *goldap.Conn) error {
	_, err := c.Search(goldap.NewSearchRequest(peopleDN, goldap.ScopeSingleLevel, goldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", []string{"uid", "mail"}, nil))

	return err
}

// unit is a search that costs roster nothing: `ou=people` itself, which is a
// node of the tree and no row.
func unit(c *goldap.Conn) error {
	_, err := c.Search(goldap.NewSearchRequest(peopleDN, goldap.ScopeBaseObject, goldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", []string{"ou"}, nil))

	return err
}

// TestAConnectionIsWhoeverBoundItForAsLongAsTheyStillStand: a bind is asked
// about again, and a connection whose binder was stopped is nobody's.
//
// # What it was
//
// A bind was checked once. The connection kept the name it bound as, and every
// later search asked only which tenant that name is in. So somebody whose app
// password was erased, who was suspended, or who was erased, was refused at a
// new bind -- and the connection they had bound before went on answering a
// search of everybody, with `mail`, for as long as it stayed up: answered
// success; right is 50, *bind first*. WhoAmI went on saying their name. A
// stream left open at roster is asked again who opened it (#274), and a
// connection of the directory's is the same thing: a credential shown once.
//
// # What ends one
//
// What would refuse the same bind made again, and for a password one thing
// more. An app password is asked about by bearing it, so its erasure, its
// holder suspended or erased, and their tenant closed all answer no. A
// password is not kept, so it is the person who is asked about: suspended,
// erased -- or signed out everywhere since they bound, which is what a person
// does about a connection they no longer trust. Signing out everywhere does
// not end an app password (`Holder.date_invalidated`), so it does not end a
// connection bound with one.
func TestAConnectionIsWhoeverBoundItForAsLongAsTheyStillStand(t *testing.T) {
	d := stand(t)
	ctx := t.Context()

	// A directory and a clock for each case, so that each binds at the time
	// it is: when somebody bound is compared with a moment roster stamped.
	directory := func(t *testing.T) (*handClock, *goldap.Conn) {
		clock := &handClock{}

		return clock, clientOf(t, d.directoryOn(t, ldap.BindEither, func(c *ldap.Config) { c.Now = clock.Now }, nil))
	}

	suspended := func(t *testing.T, v bound) {
		_, err := d.s.Ungated.Holder().Disable(ctx, rstr.HolderDisableRequest_builder{Ref: rstr.HolderRef_builder{Id: v.id}.Build()}.Build())
		require.NoError(t, err)
	}
	erased := func(t *testing.T, v bound) {
		_, err := d.s.Ungated.Holder().Erase(ctx, rstr.HolderRef_builder{Id: v.id}.Build())
		require.NoError(t, err)
	}
	signedOut := func(t *testing.T, v bound) {
		_, err := d.s.Ungated.Holder().Invalidate(ctx, rstr.HolderInvalidateRequest_builder{Ref: rstr.HolderRef_builder{Id: v.id}.Build()}.Build())
		require.NoError(t, err)
	}

	for _, tc := range []struct {
		name, alias string
		byKey       bool
		stop        func(*testing.T, bound)
	}{
		{"whose app password is erased", "han", true, func(t *testing.T, v bound) {
			_, err := d.s.Ungated.ApiKey().Erase(ctx, rstr.ApiKeyRef_builder{Id: v.keyID}.Build())
			require.NoError(t, err)
		}},
		{"suspended, who bound with an app password", "seo", true, suspended},
		{"erased, who bound with an app password", "choi", true, erased},
		{"suspended, who bound with their password", "yoon", false, suspended},
		{"erased, who bound with their password", "jang", false, erased},
		{"signed out everywhere, who bound with their password", "lim", false, signedOut},
	} {
		t.Run("somebody "+tc.name, func(t *testing.T) {
			x := require.New(t)
			v := d.binder(t, tc.alias)
			clock, c := directory(t)
			if tc.byKey {
				x.NoError(c.Bind(v.dn, v.key))
			} else {
				x.NoError(c.Bind(v.dn, password))
			}
			x.NoError(everybody(c), "the control: bound, they read")

			tc.stop(t, v)
			clock.pass(ldap.Recheck + time.Second)

			err := everybody(c)
			x.Equal(uint16(goldap.LDAPResultInsufficientAccessRights), codeOf(t, err),
				"the connection they bound before went on reading everybody: %v", err)
			x.ErrorContains(err, "bind first", "answered as a connection that never bound is")

			who, err := c.WhoAmI(nil)
			x.NoError(err)
			x.Empty(who.AuthzID, "the connection still says it is theirs")

			root := search(t, c, "", goldap.ScopeBaseObject, "(objectClass=*)", "+")
			x.Len(root, 1)
			x.Empty(root[0].GetAttributeValues("namingContexts"), "a suffix is a tenant's name, and this connection is in none now")

			x.Equal(uint16(goldap.LDAPResultInsufficientAccessRights), codeOf(t, everybody(c)), "and it stays nobody's")
		})
	}

	// What does not stop somebody does not end their connection. Signing out
	// everywhere ends what was issued before it, and a bind made after it is
	// not that.
	t.Run("and somebody who signed out everywhere and then bound is served", func(t *testing.T) {
		x := require.New(t)
		v := d.binder(t, "baek")
		signedOut(t, v)

		clock, c := directory(t)
		x.NoError(c.Bind(v.dn, password))
		clock.pass(ldap.Recheck + time.Second)

		x.NoError(everybody(c))
	})

	// And a key is named, listed and revoked one at a time: *sign out
	// everywhere* leaves each as it is.
	t.Run("and signing out everywhere leaves a connection bound with an app password", func(t *testing.T) {
		x := require.New(t)
		v := d.binder(t, "moon")
		clock, c := directory(t)
		x.NoError(c.Bind(v.dn, v.key))

		signedOut(t, v)
		clock.pass(ldap.Recheck + time.Second)

		x.NoError(everybody(c))
		who, err := c.WhoAmI(nil)
		x.NoError(err)
		x.Equal("dn:"+v.dn, who.AuthzID)
	})

	// A tenant that closed lets nobody in, the directory's own holder there
	// included. An app password is asked about by bearing it, and is refused:
	// the connection is nobody's. A password's person is asked about with the
	// directory's key, which is refused itself, so nothing can be said of
	// them -- and nothing is read either, by that connection or any other.
	// Last, since it closes fabrikam for every case after it.
	t.Run("and of a tenant that closed, nothing is read on a connection bound before", func(t *testing.T) {
		x := require.New(t)
		d.setPassword(t, d.otherKim)
		people := goldap.NewSearchRequest("ou=people,o=fabrikam", goldap.ScopeSingleLevel, goldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", []string{"uid"}, nil)

		clock := &handClock{}
		addr := d.directoryOn(t, ldap.BindEither, func(c *ldap.Config) { c.Now = clock.Now }, nil)
		byKey, byPassword := clientOf(t, addr), clientOf(t, addr)
		x.NoError(byKey.Bind(otherKimDN, d.otherKimKey))
		x.NoError(byPassword.Bind(otherKimDN, password))

		_, err := d.s.Ungated.Tenant().Close(ctx, rstr.TenantCloseRequest_builder{Ref: rstr.TenantRef_builder{Id: d.fabrikam.Bytes()}.Build()}.Build())
		x.NoError(err)
		clock.pass(ldap.Recheck + time.Second)

		_, err = byKey.Search(people)
		x.Equal(uint16(goldap.LDAPResultInsufficientAccessRights), codeOf(t, err), "%v", err)
		x.ErrorContains(err, "bind first")
		who, err := byKey.WhoAmI(nil)
		x.NoError(err)
		x.Empty(who.AuthzID, "the connection still says it is theirs")

		_, err = byPassword.Search(people)
		x.Equal(uint16(goldap.LDAPResultInsufficientAccessRights), codeOf(t, err), "%v", err)
	})
}

// TestAConnectionWhoseBinderStillStandsCostsOneCallAClock: asking again is one
// call of roster each [ldap.Recheck] for a connection that searches, and not
// one a search.
//
// The other half of the rule above, and what the clock is for. Asked at every
// search, a client that keeps one connection and searches at each sign-in --
// which is what a VPN does -- would pay a call of roster for each; asked on a
// clock, it pays one each time the clock runs out, whichever kind of bind it
// made and however many searches arrive together when it does.
func TestAConnectionWhoseBinderStillStandsCostsOneCallAClock(t *testing.T) {
	d := stand(t)
	relay := d.relayed(t)

	clock := &handClock{}
	addr := d.directoryOn(t, ldap.BindEither, func(c *ldap.Config) { c.Roster, c.Now = relay.addr, clock.Now }, nil)

	for _, tc := range []struct {
		name, secret, asks string
	}{
		{"bound with an app password", d.leeKey, rstr.MeService_Get_FullMethodName},
		{"bound with a password", password, rstr.HolderService_Get_FullMethodName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)
			c := clientOf(t, addr)
			x.NoError(c.Bind(leeDN, tc.secret))

			bound, asked := relay.asked(""), relay.asked(tc.asks)
			for range 3 {
				x.NoError(unit(c))
			}
			x.Equal(bound, relay.asked(""), "roster was asked within the clock, about a connection it had just answered for")

			// Across the clock, several times, and each time by searches
			// that arrive together.
			for turn := 1; turn <= 2; turn++ {
				clock.pass(ldap.Recheck + time.Second)

				var wg sync.WaitGroup
				errs := make([]error, 6)
				for i := range errs {
					wg.Add(1)
					go func() {
						defer wg.Done()
						errs[i] = unit(c)
					}()
				}
				wg.Wait()
				for _, err := range errs {
					x.NoError(err, "a connection whose binder still stands was not served")
				}
				for range 3 {
					x.NoError(unit(c))
				}

				x.Equal(bound+turn, relay.asked(""), "one call of roster for each time the clock ran out")
			}
			x.Equal(asked+2, relay.asked(tc.asks), "and it is the call that asks about whoever bound")

			who, err := c.WhoAmI(nil)
			x.NoError(err)
			x.Equal("dn:"+leeDN, who.AuthzID)
		})
	}
}
