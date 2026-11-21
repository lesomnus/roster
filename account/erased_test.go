package account_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/audit"

	"github.com/lesomnus/roster/account"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
)

// erased is somebody who signed in through `example`, proved an address, and
// was then erased, as the Login App's tests make one.
func (d *deployment) erased(t *testing.T, alias, subject, address string) []byte {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	who, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Alias: alias, Name: "Zed",
	}.Build())
	x.NoError(err)
	ref := rstr.HolderRef_builder{Id: who.GetId()}.Build()
	_, err = d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{Holder: ref, Provider: "example", Subject: subject}.Build())
	x.NoError(err)
	proved(t, d, who.GetId(), address)

	gone, err := d.ungated.Holder().Erase(ctx, ref)
	x.NoError(err)
	x.True(gone.GetErased())

	return who.GetId()
}

// people is how many people contoso has, which is what a refusal leaves as it
// was.
func (d *deployment) people(t *testing.T) int {
	t.Helper()

	vs, err := d.ungated.Holder().List(t.Context(), rstr.HolderListRequest_builder{
		Filters: []*rstr.HolderFilter{rstr.HolderFilter_builder{
			Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		}.Build()},
		Size: 100,
	}.Build())
	require.NoError(t, err)

	return len(vs.GetItems())
}

// TestSomebodyErasedIsToldTheirAccountWasDeletedHereToo is the Login App's
// `TestSomebodyErasedIsToldTheirAccountWasDeleted` at the other front door,
// which asks the same policy the same way (`arrives.Known`). Somebody erased
// keeps their account at the provider and the address they proved, and
// arriving by either they were answered `500`, *cannot sign in*, at every try.
// It is a refusal: the account was deleted, and an administrator of their
// organisation brings it back or lets it go. Nobody is made in their place,
// and an address the provider did not check is told nothing of them.
func TestSomebodyErasedIsToldTheirAccountWasDeletedHereToo(t *testing.T) {
	refused := func(t *testing.T, d *deployment) {
		t.Helper()
		x := require.New(t)

		before := d.people(t)
		code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/login?connection=example", "", nil)
		x.Equal(http.StatusForbidden, code, "a refusal was answered as the app's own failure: %s", body)
		x.Contains(body, "was deleted", "the person is not told what happened to their account")
		x.Contains(body, "an administrator", "nor who can bring it back")
		x.Equal(before, d.people(t), "somebody was made in the place of a person who can still be brought back")
	}

	for name, enrol := range map[string]func() account.Enrol{
		"invited": account.Invited, "expected": account.Expected, "enrolling": account.Enrolling,
	} {
		t.Run("by the account they had, under "+name, func(t *testing.T) {
			d := serve(t, enrol())
			d.erased(t, "zed", "zed-at-the-directory", "zed@contoso.com")

			d.idp.Subject = "zed-at-the-directory"
			d.idp.Claims = map[string]any{"email": "zed@contoso.com", "email_verified": true}
			refused(t, d)
		})
	}

	for name, enrol := range map[string]func() account.Enrol{
		"expected": account.Expected, "enrolling": account.Enrolling,
	} {
		t.Run("at an address they proved, under "+name, func(t *testing.T) {
			d := serve(t, enrol())
			d.erased(t, "zed", "zed-at-the-directory", "zed@contoso.com")

			d.idp.Subject = "zed-made-again-at-the-directory"
			d.idp.Claims = map[string]any{"email": "zed@contoso.com", "email_verified": true}
			refused(t, d)
		})
	}

	t.Run("and brought back, they are who they were", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())
		zed := d.erased(t, "zed", "zed-at-the-directory", "zed@contoso.com")

		d.idp.Subject = "zed-at-the-directory"
		refused(t, d)

		x.NoError(forget.Restore(t.Context(), d.s.Ent, mustUuid(t, zed), audit.Contents{}, nil))

		b := d.browser(t, "contoso.test")
		code, body := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
		x.Equal(http.StatusOK, code, body)
		code, body = b.rpc(t, "/roster.MeService/Get", `{}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"zed"`)
	})

	t.Run("an address the provider did not check is told nothing of them", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Expected())
		d.erased(t, "zed", "zed-at-the-directory", "zed@contoso.com")

		d.idp.Subject = "somebody-who-typed-it"
		d.idp.Claims = map[string]any{"email": "zed@contoso.com"}

		code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/login?connection=example", "", nil)
		x.Equal(http.StatusForbidden, code, body)
		x.Contains(body, "has not been invited", "a stranger is answered as a stranger")
		x.NotContains(body, "deleted", "an address nobody checked was told whose it had been")
	})
}

// TestAnAddressSomebodyErasedNeverProvedKeepsNobodyFromArrivingHereEither is
// the Login App's `TestAnAddressSomebodyErasedNeverProvedKeepsNobodyFromArriving`
// at the other front door: an address on an erased person's row that nobody
// proved matches nobody. It was read as an invitation, and asking how somebody
// who is not there signs in was `NotFound`: `500`, *cannot sign in*.
func TestAnAddressSomebodyErasedNeverProvedKeepsNobodyFromArrivingHereEither(t *testing.T) {
	// Entered by an operator, never arrived, and erased.
	invited := func(t *testing.T, d *deployment) []byte {
		t.Helper()
		x := require.New(t)
		ctx := t.Context()

		who, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Alias: "yun", Name: "Yun",
		}.Build())
		x.NoError(err)
		ref := rstr.HolderRef_builder{Id: who.GetId()}.Build()
		_, err = d.ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{Holder: ref, Address: "yun@contoso.com"}.Build())
		x.NoError(err)
		gone, err := d.ungated.Holder().Erase(ctx, ref)
		x.NoError(err)
		x.True(gone.GetErased())

		return who.GetId()
	}

	t.Run("under expected they are a stranger", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Expected())
		invited(t, d)

		d.idp.Subject = "yun-at-the-directory"
		d.idp.Claims = map[string]any{"email": "yun@contoso.com", "email_verified": true}

		code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/login?connection=example", "", nil)
		x.Equal(http.StatusForbidden, code, "a stranger was answered as the app's own failure: %s", body)
		x.Contains(body, "has not been invited")
	})

	t.Run("under enrolling they are made, and the address is theirs", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()
		d := serve(t, account.Enrolling())
		was := invited(t, d)

		d.idp.Subject = "yun-at-the-directory"
		d.idp.Claims = map[string]any{"email": "yun@contoso.com", "email_verified": true, "name": "Yun"}

		b := d.browser(t, "contoso.test")
		code, body := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
		x.Equal(http.StatusOK, code, "an address nobody proved kept somebody out: %s", body)

		row, err := d.ungated.Email().Get(ctx, rstr.EmailGetRequest_builder{
			Ref: rstr.EmailRef_builder{At: rstr.EmailRefByAt_builder{
				TenantId: d.contoso.Bytes(), Address: proto.String("yun@contoso.com"),
			}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		x.NotEqual(was, row.GetHolder().GetId(), "the address stayed with somebody who never proved it")
		x.NotNil(row.GetDateVerified(), "and what the directory checked is not written down")
	})
}
