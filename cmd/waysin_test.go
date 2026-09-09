package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// A tenant whose people all arrive through a directory, and the switch that
// says so.
//
// # Why the test is about `Verify` and not about a screen
//
// The first draft of `TenantConfig.password` was a boolean the sign-in pages
// read to decide whether to draw a form, with `Vouch.Verify` going on answering
// `ok` underneath it -- a lock somebody sets and does not get. So what is
// written down here is the **refusal**, and the pages drawing what is true is
// the consequence rather than the feature.
//
// It is D43's shape a second time: there a TOTP seed was a whole sign-in
// because `Verify` counted it, and the fix was one sentence both sides ask.
// `vouch.Offers` is this one's, over `Tenant.OffersPassword`.

// off turns the password off for a tenant, through the verb the console calls
// rather than through `Patch`, which is closed on the wire anyway.
func off(t *testing.T, b *built, in pdid.Id) {
	t.Helper()
	x := require.New(t)
	ref := app.TenantRef_builder{Id: in.Bytes()}.Build()

	// The version this caller read, which `Update` refuses to go without --
	// the rule that keeps two writers from each thinking they wrote last.
	got, err := b.Ungated.Tenant().Get(t.Context(), app.TenantGetRequest_builder{
		Ref:    ref,
		Select: app.TenantSelect_builder{DateUpdated: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Tenant().Update(t.Context(), app.TenantUpdateRequest_builder{
		Ref:         ref,
		DateUpdated: got.GetDateUpdated(),
		Config:      app.TenantConfig_builder{Password: proto.Bool(false)}.Build(),
	}.Build())
	x.NoError(err)
}

func TestATenantSaysWhetherAPasswordIsAWayIn(t *testing.T) {
	const secret = "correct horse battery staple"

	t.Run("unset is yes", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		who := b.holder(t, ctx, b.Contoso, "erin")

		_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
			Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(), Secret: []byte(secret),
		}.Build())
		x.NoError(err)

		// Every tenant written before the field existed reads this way, which
		// is the whole reason `password` has presence.
		res, err := b.vouched().Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
			Secret: []byte(secret),
		}.Build())
		x.NoError(err)
		x.True(res.GetOk())
	})

	t.Run("off refuses the right password", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		who := b.holder(t, ctx, b.Contoso, "erin")

		_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
			Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(), Secret: []byte(secret),
		}.Build())
		x.NoError(err)
		off(t, b, b.Contoso)

		res, err := b.vouched().Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
			Secret: []byte(secret),
		}.Build())

		// The same answer as a wrong password, and not an error: a sign-in form
		// must not be a way to read how an operator is configured.
		x.NoError(err)
		x.False(res.GetOk())
		x.Empty(res.GetContinuation())
	})

	t.Run("and only that tenant", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)

		erin := b.holder(t, ctx, b.Contoso, "erin")
		frank := b.holder(t, ctx, b.Fabrikam, "frank")
		for _, who := range []pdid.Id{erin, frank} {
			_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
				Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(), Secret: []byte(secret),
			}.Build())
			x.NoError(err)
		}
		off(t, b, b.Contoso)

		res, err := b.vouched().Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: frank.Bytes()}.Build(),
			Secret: []byte(secret),
		}.Build())
		x.NoError(err)
		x.True(res.GetOk(), "fabrikam was refused for contoso's setting")
	})

	// The write is untouched on purpose: a tenant that turns this back on
	// should find its people's passwords where they left them, and a refusal
	// here would be a password screen that breaks with nothing saying why.
	t.Run("a password can still be written", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		who := b.holder(t, ctx, b.Contoso, "erin")
		off(t, b, b.Contoso)

		_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
			Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(), Secret: []byte(secret),
		}.Build())
		x.NoError(err)
	})

	// The consequence that is easiest to forget, which is why it is written
	// down: `Redeem` ends by writing a password, so a link minted for a tenant
	// with no passwords is a mailbox full of dead ends. Refused where it is
	// minted rather than where it is spent, so that nothing is sent at all.
	t.Run("and no recovery link is mintable", func(t *testing.T) {
		x := require.New(t)
		b := keyFor(t, "/roster.VouchService/Link", "/roster.VouchService/Redeem", listHolders)
		ctx := t.Context()
		c := app.NewVouchServiceClient(b.Conn)
		as := bearing(ctx, b.Token)
		mayList(t, ctx, b, b.Who, listHolders)

		mint := func() *app.VouchDelegateResponse {
			t.Helper()
			made, err := c.Link(as, app.VouchLinkRequest_builder{
				Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			}.Build())
			x.NoError(err)
			x.NotEmpty(made.GetToken())

			res, err := c.Redeem(as, app.VouchRedeemRequest_builder{
				Token:   made.GetToken(),
				Methods: []string{listHolders},
			}.Build())
			x.NoError(err)

			return res
		}

		// The control, first: without it a refusal below proves nothing, since
		// every one of this method's answers is *a token that resolves to
		// nothing* and a test can reach that by getting the harness wrong.
		x.True(mint().GetVerified().GetOk(), "a link did not work before the switch")

		got, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
			Ref:    app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Select: app.TenantSelect_builder{DateUpdated: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		_, err = b.Ungated.Tenant().Update(ctx, app.TenantUpdateRequest_builder{
			Ref:         app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			DateUpdated: got.GetDateUpdated(),
			Config:      app.TenantConfig_builder{Password: proto.Bool(false)}.Build(),
		}.Build())
		x.NoError(err)

		// A token still comes back from `Link` -- this method answers a
		// stranger with one too, because asking for a link must not be a way to
		// ask *is this address here*, and it must not be a way to read another
		// operator's configuration either. What changes is that it resolves to
		// nobody.
		res := mint()
		x.False(res.GetVerified().GetOk(), "a link was minted for a tenant with no passwords")
		x.Empty(res.GetToken())
	})

	// And the one thing it must not reach: a second factor is not a way in, so
	// a tenant's answer about ways in does not touch it. Somebody who arrived
	// through a directory still proves a TOTP step.
	t.Run("a second factor is not a way in and is unaffected", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		who := b.holder(t, ctx, b.Contoso, "erin")
		off(t, b, b.Contoso)

		x.True(vouch.Offers(nil, vouch.KindTotp))

		got, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
			Ref:    app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Select: app.TenantSelect_builder{Config: proto.Bool(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.False(got.OffersPassword())
		x.True(vouch.Offers(got, vouch.KindTotp))
		x.False(vouch.Offers(got, vouch.KindPassword))

		_ = who
	})
}
