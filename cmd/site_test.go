package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestASiteBelongsToOneTenant is the ordinary wall, and it is here because
// everything below narrows further than this.
func TestASiteBelongsToOneTenant(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")

	fabrikamUser := b.holder(t, ctx, b.Fabrikam, "theirs")
	_, err := b.Walled.Site().Get(
		b.as(ctx, fabrikamUser, b.Fabrikam),
		app.SiteGetRequest_builder{Ref: app.SiteRef_builder{Id: seoul.Bytes()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err))
}

// TestASiteAliasIsUniqueWithinItsTenantAndNotBeyond, which is what makes
// `@contoso/seoul` a name somebody can put in a configuration file while another
// customer is free to have a Seoul of their own.
func TestASiteAliasIsUniqueWithinItsTenantAndNotBeyond(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.site(t, ctx, b.Contoso, "seoul")

	// The same name in another tenant is a different site.
	b.site(t, ctx, b.Fabrikam, "seoul")

	// The same name in the same tenant is the same site.
	_, err := b.Ungated.Site().Add(ctx, app.SiteAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "seoul",
	}.Build())
	x.Equal(codes.AlreadyExists, status.Code(err))
}

// TestLabelsAreCarried is what an administrative grant selects on.
//
// "every site in Asia" is a match over these. Resolving that match to a set of
// sites is roster's, because payday's grant carries identifiers rather than
// selectors.
func TestLabelsAreCarried(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Site().Add(ctx, app.SiteAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "seoul",
		Labels: map[string]string{"region": "asia", "tier": "production"},
	}.Build())
	x.NoError(err)
	x.Equal("asia", v.GetLabels()["region"])

	got, err := b.Ungated.Site().Get(ctx, app.SiteGetRequest_builder{Ref: v.Ref()}.Build())
	x.NoError(err)
	x.Equal("production", got.GetLabels()["tier"], "labels did not survive the round trip")
}

// TestAnErasedSiteFreesItsAlias, because a site is decommissioned and a later
// one takes the name. The trail keeps what the old one was.
func TestAnErasedSiteFreesItsAlias(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Site().Add(ctx, app.SiteAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "seoul",
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Site().Erase(ctx, v.Ref())
	x.NoError(err)

	_, err = b.Ungated.Site().Add(ctx, app.SiteAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "seoul",
	}.Build())
	x.NoError(err)
}
