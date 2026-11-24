package cmd_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/keys"
)

// A key is for one app (`ApiKey.app`, #567).
//
// A key was a list of methods, and whoever it was presented to read the list:
// a key minted for one app was the same key at roster, and at every other app
// that read the same names. A key says the app it is spent at now, its methods
// are held to what its minter holds there, and at roster a key for an app is
// no key at all.

// keyForApp is an `rt_` on `who` for the app `of` -- or for roster, where
// `of` is [pdid.Nil] -- naming `methods`, written at the box.
func keyForApp(t *testing.T, ctx context.Context, s *cmd.Server, who, of pdid.Id, alias string, methods ...string) (string, pdid.Id) {
	t.Helper()
	x := require.New(t)

	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)

	req := app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:   alias,
		Secret:  sum,
		Methods: methods,
	}
	if of != pdid.Nil {
		req.App = app.HolderRef_builder{Id: of.Bytes()}.Build()
	}
	v, err := s.Ungated.ApiKey().Add(ctx, req.Build())
	x.NoError(err)

	return token, mustId(t, v.GetId())
}

// TestAKeyForAnAppIsMintedByWhoeverHoldsItsMethodsThere.
//
// What a key may call was held to what its minter holds at roster, and no key
// could name an app's methods at all since a role says whose methods it names:
// somebody who holds `/foo.Permission/Y` at Foo was refused a key naming it,
// and a key for Foo could not be said. The hold rule is asked at the key's
// app: they mint one for Foo naming it, and are refused the same key for Bar
// and for roster, `PermissionDenied` naming where; somebody who does not hold
// it at Foo is refused it for Foo. The app is a holder of the key's own tenant,
// and a key of the control plane's is for no app (`InvalidArgument`).
func TestAKeyForAnAppIsMintedByWhoeverHoldsItsMethodsThere(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const y = "/foo.Permission/Y"
	foo := b.holder(t, ctx, b.Contoso, "foo")
	bar := b.holder(t, ctx, b.Contoso, "bar")
	erin := b.holder(t, ctx, b.Contoso, "erin")
	dave := b.holder(t, ctx, b.Contoso, "dave")
	b.binds(t, erin, b.roleAtApp(t, ctx, foo, "foo-y", y), nil)

	minter := app.NewApiKeyServiceClient(served(t, b.Server))
	issue := func(who pdid.Id, alias string, of pdid.Id) (*app.ApiKeyIssueResponse, error) {
		req := app.ApiKeyIssueRequest_builder{
			Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Alias:   alias,
			Methods: []string{y},
		}
		if of != pdid.Nil {
			req.App = app.HolderRef_builder{Id: of.Bytes()}.Build()
		}

		return minter.Issue(asOverTheWire(ctx, who), req.Build())
	}

	v, err := issue(erin, "for-foo", foo)
	x.NoError(err, "somebody who holds a method at Foo minted no key for Foo naming it")
	x.True(strings.HasPrefix(v.GetToken(), keys.PrefixTenant), "%q", v.GetToken())
	got, err := b.Ungated.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
		Ref:    app.ApiKeyRef_builder{Id: v.GetKey().GetId()}.Build(),
		Select: app.ApiKeySelect_builder{App: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(foo.Bytes(), got.GetApp().GetId(), "the key does not say it is for Foo")

	_, err = issue(erin, "for-bar", bar)
	x.Equal(codes.PermissionDenied, status.Code(err), "a key for Bar, naming what is held at Foo: %v", err)
	// Named by its identifier: the person reads no holder but themselves, and
	// a refusal names an app they cannot see by what they wrote.
	x.Contains(status.Convert(err).Message(), "at "+bar.String())

	_, err = issue(erin, "for-roster", pdid.Nil)
	x.Equal(codes.PermissionDenied, status.Code(err), "a key of roster's, naming what is held at Foo: %v", err)
	x.Contains(status.Convert(err).Message(), "here")

	_, err = issue(dave, "for-foo", foo)
	x.Equal(codes.PermissionDenied, status.Code(err), "a key for Foo, by somebody who holds nothing there: %v", err)

	// The app is somebody of the key's own tenant, and live: one of another
	// tenant, or erased, is nobody there.
	_, err = issue(erin, "elsewhere", b.holder(t, ctx, b.Fabrikam, "foo"))
	x.Equal(codes.NotFound, status.Code(err), "a key for an app of another tenant: %v", err)
	gone := b.holder(t, ctx, b.Contoso, "gone")
	_, err = b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: gone.Bytes()}.Build())
	x.NoError(err)
	_, err = issue(erin, "for-gone", gone)
	x.Equal(codes.NotFound, status.Code(err), "a key for an app that is erased: %v", err)

	// And at the box, where the tenant is read as it is.
	_, err = b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: erin.Bytes()}.Build(),
		App:     app.HolderRef_builder{Id: b.holder(t, ctx, b.Fabrikam, "foo").Bytes()}.Build(),
		Alias:   "elsewhere",
		Secret:  freshSum(t),
		Methods: []string{y},
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a key for an app of another tenant: %v", err)
}

// freshSum is the verifier of a fresh tenant key.
func freshSum(t *testing.T) []byte {
	t.Helper()

	_, sum, err := keys.Mint(keys.PrefixTenant)
	require.NoError(t, err)

	return sum
}

// TestAKeyOfTheControlPlanesIsForNoApp.
//
// A key of the control plane's is the deployment's own: presented to roster,
// across every tenant or narrowed to one, and spent at no app. One naming an
// app is refused, `InvalidArgument`, at `Issue` and at the box's `Add`.
func TestAKeyOfTheControlPlanesIsForNoApp(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	foo := app.HolderRef_builder{Id: b.Service.Bytes()}.Build()
	_, err := b.Control.Ungated.ApiKey().Issue(ctx, app.ApiKeyIssueRequest_builder{
		HolderAlias: z.Ptr("portal"), Alias: "for-an-app", Methods: []string{"/roster.HolderService/Get"}, App: foo,
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	x.Contains(status.Convert(err).Message(), "app:")

	_, err = b.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder: foo, App: foo, Alias: "for-an-app", Secret: freshSum(t), Methods: []string{"/roster.HolderService/Get"},
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
}

// TestAKeyForAnAppIsNoCredentialAtRoster.
//
// A key's methods were read by roster whoever the key was for: a key minted
// for Foo naming `/roster.HolderService/Erase` erased somebody at roster,
// presented in `authorization` -- answered `{erased: true}`; right is
// `Unauthenticated`, as a key roster does not have is answered, and nobody
// erased. Beside `roster-as` the same: what a delegation narrows is a call
// the key is a credential for. A key of roster's, with the same methods,
// is answered as it was.
func TestAKeyForAnAppIsNoCredentialAtRoster(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	permits(t, ctx, b, b.Contoso, b.Who, "eraser", eraseHold, getHolder)
	foo := addHolder(t, ctx, b.Server, b.Contoso, "foo")
	victim := addHolder(t, ctx, b.Server, b.Contoso, "victim")

	forFoo, _ := keyForApp(t, ctx, b.Server, b.Who, foo, "for-foo", eraseHold, getHolder)
	rosters, _ := keyForApp(t, ctx, b.Server, b.Who, pdid.Nil, "rosters", getHolder)
	unknown, _, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)

	holders := app.NewHolderServiceClient(b.Conn)
	them := app.HolderRef_builder{Id: victim.Bytes()}.Build()

	_, err = holders.Get(bearing(ctx, rosters), app.HolderGetRequest_builder{Ref: them}.Build())
	x.NoError(err, "a key of roster's was refused at roster")

	_, none := holders.Erase(bearing(ctx, unknown), them)
	x.Equal(codes.Unauthenticated, status.Code(none))

	_, err = holders.Erase(bearing(ctx, forFoo), them)
	x.Equal(codes.Unauthenticated, status.Code(err), "a key for Foo was a credential at roster: %v", err)
	x.Equal(status.Convert(none).Message(), status.Convert(err).Message(), "and said apart from a key roster does not have")

	// Beside `roster-as`, with a delegation issued to whoever the key is of.
	as, _, err := keys.Delegate(ctx, b.Ungated, keys.Delegated{
		Holder: b.Who, Issuer: b.Who.Bytes(), Methods: []string{eraseHold, getHolder},
	})
	x.NoError(err)
	_, err = holders.Get(acting(ctx, rosters, as), app.HolderGetRequest_builder{Ref: them}.Build())
	x.NoError(err, "a key of roster's beside a delegation issued to its holder was refused")
	_, err = holders.Erase(acting(ctx, forFoo, as), them)
	x.Equal(codes.Unauthenticated, status.Code(err), "a key for Foo beside roster-as was a credential at roster: %v", err)

	v, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: them, Select: app.HolderSelect_builder{DateErased: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Nil(v.GetDateErased(), "somebody was erased by a key for Foo")
}

// TestWhatSomebodyIsToldOfTheirKeysSaysWhichAppEachIsFor.
//
// `MeService/Get` and `HolderService/SignsIn` answer every key somebody has,
// and did not say which app each is spent at: a page drew a key for Foo and
// one of roster's alike, and somebody ended the wrong one. Each says its
// `app`, and one of roster's says none.
func TestWhatSomebodyIsToldOfTheirKeysSaysWhichAppEachIsFor(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	foo := b.holder(t, ctx, b.Contoso, "foo")
	erin := b.holder(t, ctx, b.Contoso, "erin")
	_, ofFoo := keyForApp(t, ctx, b.Server, erin, foo, "for-foo", "/foo.Permission/Y")
	_, ofRoster := keyForApp(t, ctx, b.Server, erin, pdid.Nil, "rosters", getHolder)

	// Each key's app, by identifier and by alias.
	type whose struct {
		app   []byte
		alias string
	}
	said := func(ks []*app.SignInKey) map[pdid.Id]whose {
		out := map[pdid.Id]whose{}
		for _, k := range ks {
			out[mustId(t, k.GetId())] = whose{k.GetApp(), k.GetAppAlias()}
		}

		return out
	}
	want := map[pdid.Id]whose{ofFoo: {foo.Bytes(), "foo"}, ofRoster: {nil, ""}}

	conn := served(t, b.Server)
	me, err := app.NewMeServiceClient(conn).Get(asOverTheWire(ctx, erin), app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(want, said(me.GetKeys()))

	admin := b.holder(t, ctx, b.Contoso, "admin")
	v, err := b.Walled.Holder().SignsIn(b.asAtApp(ctx, admin), app.HolderSignsInRequest_builder{Ref: app.HolderRef_builder{Id: erin.Bytes()}.Build()}.Build())
	x.NoError(err)
	x.Equal(want, said(v.GetKeys()))
}

// TestWhatEndsAnAppsHolderEndsTheKeysForIt.
//
// Erasing an app's holder erased its roles, its groups and the bindings for
// it, and left the keys for it: listed among somebody's keys for an app that
// had ended. They are erased in the same act, whoever's they are, and a
// restore of the holder brings none of them back; a key for another app, and
// the app's own keys, stay.
func TestWhatEndsAnAppsHolderEndsTheKeysForIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	foo := b.holder(t, ctx, b.Contoso, "foo")
	bar := b.holder(t, ctx, b.Contoso, "bar")
	erin := b.holder(t, ctx, b.Contoso, "erin")
	_, forFoo := keyForApp(t, ctx, b.Server, erin, foo, "for-foo", "/foo.AssetService/Get")
	_, forBar := keyForApp(t, ctx, b.Server, erin, bar, "for-bar", "/bar.AssetService/Get")
	_, foos := keyForApp(t, ctx, b.Server, foo, pdid.Nil, "foos-own", getHolder)

	live := func(k pdid.Id) bool {
		v, err := b.Ent.ApiKey.Get(ctx, k.Uuid())
		x.NoError(err)

		return v.DateErased == nil
	}

	admin := b.holder(t, ctx, b.Contoso, "admin")
	_, err := b.Walled.Holder().Erase(b.asAtApp(ctx, admin), app.HolderRef_builder{Id: foo.Bytes()}.Build())
	x.NoError(err)
	x.False(live(forFoo), "a key for the erased app is still there")
	x.True(live(forBar), "a key for another app went with it")
	x.True(live(foos), "the app's own key went with it")

	x.NoError(forget.Restore(ctx, b.Ent, foo, audit.Contents{}, nil))
	x.False(live(forFoo), "a restore of the holder brought the key for it back")
}

// TestAPersonMintsTheirOwnKeyForAnAppAndIsToldWhichAppEachIsFor.
//
// `roster me issue-key` minted a key of roster's and nothing else, and `roster
// me get` and `roster key list` did not say which app a key is for. `--app`
// names it, by alias in their own tenant, and both say `app=@…` of a key for
// an app and nothing of one of roster's.
func TestAPersonMintsTheirOwnKeyForAnAppAndIsToldWhichAppEachIsFor(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, "/roster.ApiKeyService/Issue", "/roster.MeService/Get")
	ctx := t.Context()

	tenant := app.TenantRef_builder{Id: b.Tenant.GetId()}.Build()
	loft, err := b.Server.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: tenant, Alias: "loft"}.Build())
	x.NoError(err)
	r, err := b.Server.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: tenant, Alias: "loft-assets", Methods: []string{"/loft.AssetService/Get"}, App: app.HolderRef_builder{Id: loft.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = b.Server.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role: app.RoleRef_builder{Id: r.GetId()}.Build(), Holder: app.HolderRef_builder{Id: b.Alice.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	token := stdoutOf(t, root(t, &b.Hers), "me", "issue-key", "--app", "loft", "--name", "for-loft", "--allow", "/loft.AssetService/Get")
	x.NotEmpty(token)
	k, err := b.Server.Ungated.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
		Ref:    app.ApiKeyRef_builder{Secret: keys.Sum(strings.TrimSpace(token))}.Build(),
		Select: app.ApiKeySelect_builder{App: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(loft.GetId(), k.GetApp().GetId(), "the key is not for the app --app named")

	out, err := cliRun(t, &b.Hers, "me", "get")
	x.NoError(err)
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "for-loft"):
			x.Contains(line, "app=@loft", "me get does not say the key's app: %q", line)
		case strings.HasPrefix(line, "key"):
			x.NotContains(line, "app=", "me get says an app of a key of roster's: %q", line)
		}
	}

	var listed strings.Builder
	x.NoError(cmd.ListKeys(ctx, &listed, b.Server, cmd.Operator{}))
	for _, line := range strings.Split(listed.String(), "\n") {
		switch {
		case strings.Contains(line, "/for-loft"):
			x.Contains(line, "app=@newco/loft", "key list does not say the key's app: %q", line)
		case line != "":
			x.NotContains(line, "app=", "key list says an app of a key of roster's: %q", line)
		}
	}
}

// TestTheBoxMintsAKeyForAnApp.
//
// `roster key add` minted a key of roster's and nothing else, so a key for an
// app could be had only over the wire. `--app` names the app, by alias in
// `--tenant`, and what it prints says it; an app that is nobody there is
// refused naming the flag, and nothing is minted.
func TestTheBoxMintsAKeyForAnApp(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)
	ctx := t.Context()

	for _, alias := range []string{"@newco/alice", "@newco/loft"} {
		_, _, err := boxRun(t, at(b.c, "admin"), "", "holder", "add", alias)
		x.NoError(err)
	}

	_, said, err := boxRun(t, at(b.c, "admin"), "", "key", "add", "--tenant", "newco", "--holder", "alice",
		"--app", "loft", "--name", "for-loft", "--allow", "/loft.AssetService/Get")
	x.NoError(err, said)
	x.Contains(said, "for @newco/loft")

	s, err := cmd.Build(ctx, b.c)
	x.NoError(err)
	defer s.Close()
	k, err := pdid.Parse(keyIdSaid(t, said))
	x.NoError(err)
	v, err := s.Ungated.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
		Ref:    app.ApiKeyRef_builder{Id: k.Bytes()}.Build(),
		Select: app.ApiKeySelect_builder{App: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	loft, err := cmd.CustomerOf(ctx, s, "newco", "loft")
	x.NoError(err)
	x.Equal(loft.Bytes(), v.GetApp().GetId())
	x.NoError(s.Close())

	_, said, err = boxRun(t, at(b.c, "admin"), "", "key", "add", "--tenant", "newco", "--holder", "alice",
		"--app", "levyan", "--name", "for-levyan", "--allow", "/levyan.AssetService/Get")
	x.Error(err, said)
	x.ErrorContains(err, "--app")
}
