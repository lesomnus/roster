package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
)

func TestProbeLinkCrossesTheWall(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	hooli := add(t, ctx, b.Server, "hooli")
	erlich := addHolder(t, ctx, b.Server, hooli, "erlich")

	// Alice (b.Who, in acme) is her tenant's administrator: everything.
	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:   "admin",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	hers := mintFor(t, ctx, b, b.Who, "hers", []string{"/roster.*/*"}, time.Time{})

	c := app.NewVouchServiceClient(b.Conn)
	as := bearing(ctx, hers)

	made, err := c.Link(as, app.VouchLinkRequest_builder{
		Who: app.VouchWho_builder{Tenant: "hooli", Alias: "erlich"}.Build(),
	}.Build())
	x.NoError(err)
	t.Logf("link token: %q", made.GetToken())

	n, err := b.Ent.Link.Query().Count(ctx)
	x.NoError(err)
	t.Logf("link rows: %d", n)

	res, err := c.Redeem(as, app.VouchRedeemRequest_builder{
		Token:   made.GetToken(),
		Methods: []string{listHolders},
	}.Build())
	x.NoError(err)
	t.Logf("redeem ok=%v token=%q holder=%x erlich=%x", res.GetVerified().GetOk(), res.GetToken(), res.GetVerified().GetHolder(), erlich.Bytes())
}

func TestProbeSpendIt(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	hooli := add(t, ctx, b.Server, "hooli")
	erlich := addHolder(t, ctx, b.Server, hooli, "erlich")

	// erlich may list holders in hooli.
	hrole, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: hooli.Bytes()}.Build(),
		Alias:   "hooli-reader",
		Methods: []string{listHolders},
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: hrole.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: erlich.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:   "admin",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	hers := mintFor(t, ctx, b, b.Who, "hers", []string{"/roster.*/*"}, time.Time{})

	c := app.NewVouchServiceClient(b.Conn)
	as := bearing(ctx, hers)

	made, err := c.Link(as, app.VouchLinkRequest_builder{
		Who: app.VouchWho_builder{Tenant: "hooli", Alias: "erlich"}.Build(),
	}.Build())
	x.NoError(err)

	res, err := c.Redeem(as, app.VouchRedeemRequest_builder{
		Token:   made.GetToken(),
		Methods: []string{listHolders},
	}.Build())
	x.NoError(err)
	x.True(res.GetVerified().GetOk())

	v, err := app.NewHolderServiceClient(b.Conn).List(acting(ctx, hers, res.GetToken()),
		app.HolderListRequest_builder{}.Build())
	x.NoError(err)
	for _, h := range v.GetItems() {
		t.Logf("saw %q", h.GetAlias())
	}

	me, err := app.NewMeServiceClient(b.Conn).Get(acting(ctx, hers, res.GetToken()), app.MeGetRequest_builder{}.Build())
	if err != nil {
		t.Logf("me err: %v", err)
	} else {
		t.Logf("me: %q", me.GetAlias())
	}
}
