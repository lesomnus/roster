package cmd_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/me"
)

// TestAnAccountAtTheDirectorysConnectionIsTakenAwayAcrossTheTenant: at the
// connection a tenant's directory provisions through, who has an account is
// the directory's to say, and that was held for the write that adds one and
// for no other. The identity there is what makes somebody one the directory
// speaks for, and what it finds them by. So somebody who took their own away
// -- the *unlink* on their own page -- was no longer anybody their directory
// could suspend; and an account moved to another subject is another of the
// directory's people.
//
// Taking one away, and moving one, ask what adding one asks: the verb held
// across the tenant by a binding. Not somebody's own doing, and not a grant
// made in a site, since the directory finds its people across the tenant. The
// endpoint's side of it is `scim`'s
// `TestSomebodyDoesNotTakeThemselvesOutOfTheirDirectorysHands`.
func TestAnAccountAtTheDirectorysConnectionIsTakenAwayAcrossTheTenant(t *testing.T) {
	b, ctx := build(t)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	const (
		erase     = app.IdentityService_Erase_FullMethodName
		resubject = app.IdentityService_Resubject_FullMethodName
		get       = app.IdentityService_Get_FullMethodName
	)

	// Two doors: one the directory provisions through, which came to name its
	// people by `oid` after they had signed in through it, and one nothing
	// provisions through.
	b.connected(t, ctx, b.Contoso, "github")
	_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/contoso/v2.0", ClientId: "the-app",
	}.Build())
	require.NoError(t, err)

	// Four people of one site, each with an account at both doors and a
	// password, so that no account of theirs is their last way in.
	seoul := b.site(t, ctx, b.Contoso, "seoul")
	type person struct {
		id                pdid.Id
		theirs, elsewhere *app.Identity
	}
	people := map[string]person{}
	for i, alias := range []string{"park", "choi", "lee", "han"} {
		who := b.holder(t, ctx, b.Contoso, alias)
		b.joins(t, ctx, who, seoul)
		people[alias] = person{
			id:        who,
			theirs:    b.identity(t, ctx, who, "entra", strings.Repeat(string(rune('P'+i)), 21)+"IkzqFVrSaSaFHy782bbtaQ"),
			elsewhere: b.identity(t, ctx, who, "github", fmt.Sprintf("424%d", i)),
		}
		b.sets(t, ctx, who, "correct horse battery staple")
	}
	park, choi, lee, han := people["park"], people["choi"], people["lee"], people["han"]

	c := b.connectionOf(t, ctx, b.Contoso, "entra")
	_, err = b.Ungated.Connection().Update(ctx, app.ConnectionUpdateRequest_builder{
		Ref: app.ConnectionRef_builder{Id: c.GetId()}.Build(), DateUpdated: c.GetDateUpdated(),
		SubjectClaim: z.Ptr("oid"), Provisions: z.Ptr(true),
	}.Build())
	require.NoError(t, err)

	// Somebody bound the verbs in park's site, and the tenant's administrator.
	kim := b.holder(t, ctx, b.Contoso, "kim")
	b.boundIn(t, ctx, kim, seoul, "seoul-desk", erase, resubject, get)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)

	unlink := func(who pdid.Id, v *app.Identity) error {
		t.Helper()
		s := me.New(b.Ent, cmd.Everything(b.Ent), me.WithWrites(b.Walled))
		_, err := s.Unlink(b.asNobody(ctx, who, b.Contoso), app.MeUnlinkRequest_builder{Id: v.GetId()}.Build())

		return err
	}
	stillThere := func(v *app.Identity) {
		t.Helper()
		got, err := b.Ungated.Identity().Get(ctx, app.IdentityGetRequest_builder{
			Ref:    app.IdentityRef_builder{Id: v.GetId()}.Build(),
			Select: app.IdentitySelect_builder{Subject: z.Ptr(true)}.Build(),
		}.Build())
		require.NoError(t, err, "the account the directory finds them by is gone")
		require.Equal(t, v.GetSubject(), got.GetSubject(), "the account the directory finds them by was moved")
	}

	t.Run("not by the person", func(t *testing.T) {
		x := require.New(t)
		err := unlink(park.id, park.theirs)
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		x.ErrorContains(err, "directory", "the refusal does not say whose these are")
		stillThere(park.theirs)
	})

	t.Run("nor on a grant made in a site", func(t *testing.T) {
		x := require.New(t)
		_, err := b.Walled.Identity().Erase(b.calling(ctx, kim, erase), app.IdentityRef_builder{Id: choi.theirs.GetId()}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		stillThere(choi.theirs)
	})

	t.Run("nor moved on one", func(t *testing.T) {
		x := require.New(t)
		_, err := b.Walled.Identity().Resubject(b.calling(ctx, kim, resubject), app.IdentityResubjectRequest_builder{
			Ref: app.IdentityRef_builder{Id: lee.theirs.GetId()}.Build(), Subject: z.Ptr(fredOid),
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		stillThere(lee.theirs)
	})

	t.Run("an account anywhere else is taken away as it was", func(t *testing.T) {
		x := require.New(t)
		x.NoError(unlink(park.id, park.elsewhere))
		_, err := b.Walled.Identity().Erase(b.calling(ctx, kim, erase), app.IdentityRef_builder{Id: choi.elsewhere.GetId()}.Build())
		x.NoError(err)
	})

	t.Run("and across the tenant it is moved, and taken away", func(t *testing.T) {
		x := require.New(t)
		v, err := b.Walled.Identity().Resubject(admin, app.IdentityResubjectRequest_builder{
			Ref: app.IdentityRef_builder{Id: han.theirs.GetId()}.Build(), Subject: z.Ptr(erinOid),
		}.Build())
		x.NoError(err)
		x.Equal(erinOid, v.GetSubject())

		res, err := b.Walled.Identity().Erase(admin, app.IdentityRef_builder{Id: han.theirs.GetId()}.Build())
		x.NoError(err)
		x.True(res.GetErased())
	})
}
