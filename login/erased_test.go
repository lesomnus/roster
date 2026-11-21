package login_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/arrives"
	"github.com/lesomnus/roster/internal/idptest"
	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
)

// erased is somebody who signed in through `entra`, proved an address, and
// was then erased: by an operator, or by themselves where their tenant lets
// them leave.
//
// An erase stamps their row alone. Their alias comes free at once; their
// address and their account at the provider stay theirs, because `roster
// restore` brings back who they were for as long as they are not forgotten.
func (d *deployment) erased(t *testing.T, alias, subject, address string) pdid.Id {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	who, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(), Alias: alias, Name: "Zed",
	}.Build())
	x.NoError(err)
	ref := rstr.HolderRef_builder{Id: who.GetId()}.Build()
	_, err = d.s.Ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{Holder: ref, Provider: "entra", Subject: subject}.Build())
	x.NoError(err)
	v, err := d.s.Ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{Holder: ref, Address: address}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Email().Patch(ctx, rstr.EmailPatchRequest_builder{
		Ref: rstr.EmailRef_builder{Id: v.GetId()}.Build(), DateVerified: timestamppb.Now(), DateUpdated: v.GetDateUpdated(),
	}.Build())
	x.NoError(err)

	gone, err := d.s.Ungated.Holder().Erase(ctx, ref)
	x.NoError(err)
	x.True(gone.GetErased())

	id, err := pdid.From(who.GetId())
	x.NoError(err)

	return id
}

// people is how many people contoso has, which is what a refusal leaves as it
// was.
func (d *deployment) people(t *testing.T) int {
	t.Helper()

	vs, err := d.s.Ungated.Holder().List(t.Context(), rstr.HolderListRequest_builder{
		Filters: []*rstr.HolderFilter{rstr.HolderFilter_builder{
			Tenant: rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
		}.Build()},
		Size: 100,
	}.Build())
	require.NoError(t, err)

	return len(vs.GetItems())
}

// TestSomebodyErasedIsToldTheirAccountWasDeleted: an erase stamps the person
// and leaves their account at a provider and their addresses theirs, so that
// a restore gives back who they were. What met them at a front door did not
// know that. Arriving by the account they had, they were found by it and
// handed to `VouchService/Accept`, which answered `Unknown`; arriving verified
// at an address they proved, they were matched to it and the link to a person
// who is not there was `NotFound`. Each came back as the app's own failure:
// `502`, *this login is not working*, at every try, for as long as nobody
// forgot them -- which is for good where `holder.forget_after` is not set.
//
// It is a refusal about that arrival, as the one
// `TestSomebodyTheDoorMayNotLinkIsToldWhoCan` holds is: the account was
// deleted, and an administrator of their organisation brings it back or lets
// it go. Nobody is made in their place, since the address and the account are
// still theirs to come back to.
//
// And it says no more to a stranger than a refused arrival already does. It
// is said to whoever the provider says holds the account, or holds the mailbox
// somebody **proved** -- the two an arrival is matched by, and the two the
// other refusals already answer -- and not to an address the provider did not
// check, nor about an address the erased person never proved, which names
// nobody and keeps nobody out.
func TestSomebodyErasedIsToldTheirAccountWasDeleted(t *testing.T) {
	refused := func(t *testing.T, d *deployment, challenge string) {
		t.Helper()
		x := require.New(t)

		before := d.people(t)
		d.hydra.raise(challenge, "contoso-web")
		res := d.through(t, d.browser(t), challenge, "entra")
		x.Equal(http.StatusForbidden, res.StatusCode, "a refusal was answered as the app's own failure")
		said, err := io.ReadAll(res.Body)
		x.NoError(err)
		x.Contains(string(said), "was deleted", "the person is not told what happened to their account")
		x.Contains(string(said), "an administrator", "nor who can bring it back")

		_, told := d.hydra.said()
		x.False(told, "hydra was told about a sign-in that was refused")
		x.Equal(before, d.people(t), "somebody was made in the place of a person who can still be brought back")
	}

	for name, enrol := range map[string]func() arrives.Enrol{
		"invited": arrives.Invited, "expected": arrives.Expected, "enrolling": arrives.Enrolling,
	} {
		t.Run("by the account they had, under "+name, func(t *testing.T) {
			p := idptest.New(t, "login-app")
			d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = enrol() })
			d.connect(t, p, "entra")
			d.erased(t, "zed", "zed-at-entra", "zed@contoso.example")

			p.Subject = "zed-at-entra"
			p.Claims = map[string]any{"email": "zed@contoso.example", "email_verified": true}
			refused(t, d, "c1")
		})
	}

	for name, enrol := range map[string]func() arrives.Enrol{
		"expected": arrives.Expected, "enrolling": arrives.Enrolling,
	} {
		t.Run("at an address they proved, under "+name, func(t *testing.T) {
			p := idptest.New(t, "login-app")
			d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = enrol() })
			d.connect(t, p, "entra")
			d.erased(t, "zed", "zed-at-entra", "zed@contoso.example")

			// Their account at the directory made again, or whoever has the
			// mailbox now.
			p.Subject = "zed-made-again-at-entra"
			p.Claims = map[string]any{"email": "zed@contoso.example", "email_verified": true}
			refused(t, d, "c1")
		})
	}

	t.Run("and brought back, they are who they were", func(t *testing.T) {
		x := require.New(t)
		p := idptest.New(t, "login-app")
		d := serve(t)
		d.connect(t, p, "entra")
		zed := d.erased(t, "zed", "zed-at-entra", "zed@contoso.example")

		p.Subject = "zed-at-entra"
		refused(t, d, "c1")

		x.NoError(forget.Restore(t.Context(), d.s.Ent, zed, audit.Contents{}, nil))

		d.hydra.raise("c2", "contoso-web")
		res := d.through(t, d.browser(t), "c2", "entra")
		x.Equal(http.StatusSeeOther, res.StatusCode)
		subject, _ := d.hydra.told()
		x.Equal(zed.String(), subject)
	})

	t.Run("an address the provider did not check is told nothing of them", func(t *testing.T) {
		x := require.New(t)
		p := idptest.New(t, "login-app")
		d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Expected() })
		d.connect(t, p, "entra")
		d.erased(t, "zed", "zed-at-entra", "zed@contoso.example")

		// Anybody may type an address into a directory that asks no proof.
		p.Subject = "somebody-who-typed-it"
		p.Claims = map[string]any{"email": "zed@contoso.example"}

		d.hydra.raise("c1", "contoso-web")
		res := d.through(t, d.browser(t), "c1", "entra")
		x.Equal(http.StatusForbidden, res.StatusCode)
		said, err := io.ReadAll(res.Body)
		x.NoError(err)
		x.Contains(string(said), "has not been invited", "a stranger is answered as a stranger")
		x.NotContains(string(said), "deleted", "an address nobody checked was told whose it had been")
	})
}

// TestAnAddressSomebodyErasedNeverProvedKeepsNobodyFromArriving: an address
// nobody proved names nobody and keeps nobody out, and the one on an erased
// person's row is no different -- an operator's invitation they never took up,
// or an address they typed. It was read as an invitation all the same, and
// asking how somebody who is not there signs in was `NotFound`: `502`, *this
// login is not working*, for whoever the provider says holds that mailbox.
//
// So it matches nobody. Under `expected` they are a stranger, and under
// `enrolling` they are made, and the address is theirs, proved by the
// directory's word, as any address nobody proved goes to whoever proves it.
func TestAnAddressSomebodyErasedNeverProvedKeepsNobodyFromArriving(t *testing.T) {
	// Entered by an operator, never arrived, and erased.
	invited := func(t *testing.T, d *deployment) pdid.Id {
		t.Helper()

		who := d.entered(t, "yun", "yun@contoso.example")
		gone, err := d.s.Ungated.Holder().Erase(t.Context(), rstr.HolderRef_builder{Id: who.Bytes()}.Build())
		require.NoError(t, err)
		require.True(t, gone.GetErased())

		return who
	}

	t.Run("under expected they are a stranger", func(t *testing.T) {
		x := require.New(t)
		p := idptest.New(t, "login-app")
		d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Expected() })
		d.connect(t, p, "entra")
		invited(t, d)

		p.Subject = "yun-at-entra"
		p.Claims = map[string]any{"email": "yun@contoso.example", "email_verified": true}

		d.hydra.raise("c1", "contoso-web")
		res := d.through(t, d.browser(t), "c1", "entra")
		x.Equal(http.StatusForbidden, res.StatusCode, "a stranger was answered as the app's own failure")
		said, err := io.ReadAll(res.Body)
		x.NoError(err)
		x.Contains(string(said), "has not been invited")
	})

	t.Run("under enrolling they are made, and the address is theirs", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()
		p := idptest.New(t, "login-app")
		d := serveAs(t, login.Skip, func(c *login.Config) { c.Enrol = arrives.Enrolling() })
		d.connect(t, p, "entra")
		was := invited(t, d)

		p.Subject = "yun-at-entra"
		p.Claims = map[string]any{"email": "yun@contoso.example", "email_verified": true, "name": "Yun"}

		d.hydra.raise("c1", "contoso-web")
		res := d.through(t, d.browser(t), "c1", "entra")
		x.Equal(http.StatusSeeOther, res.StatusCode, "an address nobody proved kept somebody out")
		subject, _ := d.hydra.told()
		x.NotEqual(was.String(), subject, "signed in as somebody who was erased")

		tn := d.tenant(t)
		row, err := d.s.Ungated.Email().Get(ctx, rstr.EmailGetRequest_builder{
			Ref: rstr.EmailRef_builder{At: rstr.EmailRefByAt_builder{
				TenantId: tn.Bytes(), Address: proto.String("yun@contoso.example"),
			}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		holder, err := pdid.From(row.GetHolder().GetId())
		x.NoError(err)
		x.Equal(subject, holder.String(), "the address stayed with somebody who never proved it")
		x.NotNil(row.GetDateVerified(), "and what the directory checked is not written down")
	})
}
