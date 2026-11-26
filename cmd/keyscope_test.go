package cmd_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/z"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"

	entapikey "github.com/lesomnus/roster/internal/ent/apikey"
)

// A key for an app carries the app's own word about it (`ApiKey.scope`).
//
// An app could not offer what a hosting service's access token offers, *this
// token reaches these two repositories*: `Issue` had no field to keep anything
// of the app's on a key, and `Introspect` answered the holder, the tenant, the
// methods and the end and nothing the app had asked to be said of the key. So
// a key is minted with a scope now, on every road that mints one for an app,
// and `Introspect` answers it to that app as it was given. roster reads none
// of it: a key is exactly as wide here as its methods say.

// scopeWorld is Contoso with two apps, Foo and Bar, each holding a key of
// roster's that may introspect, and erin, who holds `/foo.AssetService/Get`
// at Foo and holds a key of her own that mints keys.
type scopeWorld struct {
	*keyedBuilt
	foo, bar, erin pdid.Id
	fooKey, barKey string
	erinKey        string
}

const scopedMethod = "/foo.AssetService/Get"

func scopeWorldOf(t *testing.T) scopeWorld {
	t.Helper()
	x := require.New(t)
	b := keyFor(t)
	ctx := t.Context()

	w := scopeWorld{keyedBuilt: b}
	w.foo = addHolder(t, ctx, b.Server, b.Contoso, "foo")
	w.bar = addHolder(t, ctx, b.Server, b.Contoso, "bar")
	w.erin = addHolder(t, ctx, b.Server, b.Contoso, "erin")

	// What erin holds at Foo, a role of Foo's.
	tenant := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: tenant, Alias: "foo-assets", Methods: []string{scopedMethod},
		App: app.HolderRef_builder{Id: w.foo.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: w.erin.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// Each app's key of roster's. Foo reads the keys that are for it, as
	// `roster app install` binds it to.
	w.fooKey = mintFor(t, ctx, b, w.foo, "foo", []string{"/roster.*/*", introspect}, time.Time{})
	w.barKey = mintFor(t, ctx, b, w.bar, "bar", []string{"/roster.*/*", introspect}, time.Time{})
	permits(t, ctx, b, b.Contoso, w.foo, "foo-introspects", introspect)
	permits(t, ctx, b, b.Contoso, w.bar, "bar-introspects", introspect)
	administers, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: tenant, Alias: core.AppAdministrator, Methods: core.ForAnApp,
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: administers.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: w.foo.Bytes()}.Build(),
		App:    app.HolderRef_builder{Id: w.foo.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// erin's own: everybody of a tenant mints keys and hands delegations of
	// their own, over what is theirs.
	w.erinKey = mintFor(t, ctx, b, w.erin, "erin", []string{
		app.ApiKeyService_Issue_FullMethodName,
		app.DelegationService_Issue_FullMethodName,
		app.MeService_Get_FullMethodName,
	}, time.Time{})

	return w
}

// issued is a key for `of`, minted as erin over the wire, saying `scope`.
func (w scopeWorld) issued(t *testing.T, alias string, of pdid.Id, scope string) (*app.ApiKeyIssueResponse, error) {
	t.Helper()

	req := app.ApiKeyIssueRequest_builder{
		Holder:  app.HolderRef_builder{Id: w.erin.Bytes()}.Build(),
		Alias:   alias,
		Methods: []string{scopedMethod},
		Scope:   scope,
	}
	if of != pdid.Nil {
		req.App = app.HolderRef_builder{Id: of.Bytes()}.Build()
	}

	return app.NewApiKeyServiceClient(w.Conn).Issue(bearing(t.Context(), w.erinKey), req.Build())
}

// introspected is what `Introspect` answers the holder of `asker` about `token`.
func (w scopeWorld) introspected(t *testing.T, asker, token string) (*pdpb.TokenIntrospectResponse, error) {
	t.Helper()

	return pdpb.NewTokenServiceClient(w.Conn).Introspect(bearing(t.Context(), asker), pdpb.TokenIntrospectRequest_builder{Token: token}.Build())
}

// TestAKeyForAnAppCarriesTheAppsScope.
//
// `ApiKeyService/Issue` took no word of the app's, and `Introspect` answered
// none: a key that should reach one collection of the app's could not say
// which. A key minted as the person, and one minted by the app as them
// through a delegation, each carry the scope they were given, answered to
// the app by `Introspect` as it was given; two keys of one person for one
// app answer each its own; a key with none answers none; and another app
// asking is answered `NotFound: no such token`, as it was.
func TestAKeyForAnAppCarriesTheAppsScope(t *testing.T) {
	w := scopeWorldOf(t)
	ctx := t.Context()

	one, err := w.issued(t, "collection-one", w.foo, "assets:01")
	require.NoError(t, err)
	two, err := w.issued(t, "collection-two", w.foo, "assets:02 and the rest, as Foo writes it")
	require.NoError(t, err)
	none, err := w.issued(t, "everything", w.foo, "")
	require.NoError(t, err)

	t.Run("each is answered to the app its own", func(t *testing.T) {
		x := require.New(t)

		for token, scope := range map[string]string{
			one.GetToken():  "assets:01",
			two.GetToken():  "assets:02 and the rest, as Foo writes it",
			none.GetToken(): "",
		} {
			res, err := w.introspected(t, w.fooKey, token)
			x.NoError(err)
			x.Equal(scope, res.GetScope(), "not answered as it was given")
			x.Equal(w.erin.Bytes(), res.GetId())
			x.Equal([]string{scopedMethod}, res.GetGrant().GetActions(), "the scope moved what the key may call")
		}
		x.Equal("assets:01", one.GetKey().GetScope(), "the row answered at the mint does not say it")
	})

	t.Run("and to nobody else", func(t *testing.T) {
		x := require.New(t)

		_, err := w.introspected(t, w.barKey, one.GetToken())
		x.Equal(codes.NotFound, status.Code(err), "%v", err)
		x.Equal("no such token", status.Convert(err).Message())
	})

	t.Run("and the app minting as the person, through a delegation", func(t *testing.T) {
		x := require.New(t)

		d, err := app.NewDelegationServiceClient(w.Conn).Issue(bearing(ctx, w.erinKey), app.DelegationIssueRequest_builder{
			Holder:  app.HolderRef_builder{Id: w.erin.Bytes()}.Build(),
			App:     app.HolderRef_builder{Id: w.foo.Bytes()}.Build(),
			Methods: []string{app.ApiKeyService_Issue_FullMethodName},
		}.Build())
		x.NoError(err)

		v, err := app.NewApiKeyServiceClient(w.Conn).Issue(acting(ctx, w.fooKey, d.GetToken()), app.ApiKeyIssueRequest_builder{
			Holder:  app.HolderRef_builder{Id: w.erin.Bytes()}.Build(),
			App:     app.HolderRef_builder{Id: w.foo.Bytes()}.Build(),
			Alias:   "through-foo",
			Methods: []string{scopedMethod},
			Scope:   "assets:03",
		}.Build())
		x.NoError(err)

		res, err := w.introspected(t, w.fooKey, v.GetToken())
		x.NoError(err)
		x.Equal("assets:03", res.GetScope())
	})
}

// TestAScopeIsOnlyAnAppsAndHasALength.
//
// A scope is the word of the app a key is for, and only that app reads it. On
// a key of roster's own nobody would, so it is refused, `InvalidArgument` on
// `scope` -- on the data plane, at the box's `Add`, and on the control plane,
// where it leaves behind no holder the request named. And it is at most 256
// bytes: 256 is taken, 257 refused naming the length.
func TestAScopeIsOnlyAnAppsAndHasALength(t *testing.T) {
	w := scopeWorldOf(t)
	ctx := t.Context()

	t.Run("not on a key of roster's", func(t *testing.T) {
		x := require.New(t)

		_, err := w.issued(t, "rosters", pdid.Nil, "assets:01")
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.Contains(status.Convert(err).Message(), "scope:")
		x.Contains(status.Convert(err).Message(), "no app is there to read")

		_, err = w.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
			Holder: app.HolderRef_builder{Id: w.erin.Bytes()}.Build(),
			Alias:  "rosters", Secret: freshSum(t), Methods: []string{getHolder}, Scope: z.Ptr("assets:01"),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "at the box: %v", err)
	})

	t.Run("nor on the control plane's, and nobody is made for it", func(t *testing.T) {
		x := require.New(t)

		_, err := w.Control.Ungated.ApiKey().Issue(ctx, app.ApiKeyIssueRequest_builder{
			HolderAlias: z.Ptr("scoped-portal"), Alias: "portal", Methods: []string{getHolder}, Scope: "assets:01",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.Contains(status.Convert(err).Message(), "scope:")

		vs, err := w.Control.Ungated.Holder().List(ctx, app.HolderListRequest_builder{}.Build())
		x.NoError(err)
		for _, v := range vs.GetItems() {
			x.NotEqual("scoped-portal", v.GetAlias(), "a key that was refused left the holder it named")
		}
	})

	t.Run("at most 256 bytes", func(t *testing.T) {
		x := require.New(t)

		_, err := w.issued(t, "at-the-bound", w.foo, strings.Repeat("a", core.MaxKeyScope))
		x.NoError(err)

		_, err = w.issued(t, "past-the-bound", w.foo, strings.Repeat("a", core.MaxKeyScope+1))
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.Contains(status.Convert(err).Message(), "scope: at most 256 bytes, and this is 257")
	})
}

// TestNoWriteChangesAKeysScope.
//
// A key's reach is what it was minted with, as its app is: no request writes
// a scope after the mint. `Patch` has no field for one, and a patch of what
// it does write leaves the scope as it was.
func TestNoWriteChangesAKeysScope(t *testing.T) {
	x := require.New(t)
	w := scopeWorldOf(t)
	ctx := t.Context()

	x.Nil((&app.ApiKeyPatchRequest{}).ProtoReflect().Descriptor().Fields().ByName("scope"), "a patch writes a key's scope")

	v, err := w.issued(t, "kept", w.foo, "assets:01")
	x.NoError(err)
	_, err = w.Ungated.ApiKey().Patch(ctx, app.ApiKeyPatchRequest_builder{
		Ref:              app.ApiKeyRef_builder{Id: v.GetKey().GetId()}.Build(),
		Alias:            z.Ptr("renamed"),
		Desc:             z.Ptr("a note"),
		DateExpired:      timestamppb.New(time.Now().Add(time.Hour)),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	res, err := w.introspected(t, w.fooKey, v.GetToken())
	x.NoError(err)
	x.Equal("assets:01", res.GetScope())
}

// TestAKeyFromBeforeCarriesNoScope.
//
// The column comes to a table that has rows, and a row written before it is
// a key with no scope: answered empty by `Introspect` and by every list,
// whether the column holds its default or nothing at all.
func TestAKeyFromBeforeCarriesNoScope(t *testing.T) {
	x := require.New(t)
	w := scopeWorldOf(t)
	ctx := t.Context()

	token, k := keyForApp(t, ctx, w.Server, w.erin, w.foo, "from-before", scopedMethod)
	q, args := entsql.Dialect(w.Dialect).
		Update(entapikey.Table).
		SetNull(entapikey.FieldScope).
		Where(entsql.EQ(entapikey.FieldId, k.Uuid())).
		Query()
	res, err := w.Db.ExecContext(ctx, q, args...)
	x.NoError(err, "%s", q)
	n, err := res.RowsAffected()
	x.NoError(err)
	x.Equal(int64(1), n)

	v, err := w.introspected(t, w.fooKey, token)
	x.NoError(err)
	x.Empty(v.GetScope())

	me, err := app.NewMeServiceClient(w.Conn).Get(bearing(ctx, w.erinKey), app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	found := false
	for _, v := range me.GetKeys() {
		if bytes.Equal(v.GetId(), k.Bytes()) {
			found = true
			x.Empty(v.GetScope())
		}
	}
	x.True(found)
}

// TestAKeysScopeIsShownWhereTheKeyIs.
//
// Wherever a key is listed its scope is now said beside its methods: to the
// person (`MeService.Get`, `roster me get`), to the app the key is for
// (`ApiKeyService/Get` and `List`), to whoever reads how somebody signs in
// (`HolderService.SignsIn`), and at the box (`roster key list`). Nothing is
// said of a key with none.
func TestAKeysScopeIsShownWhereTheKeyIs(t *testing.T) {
	x := require.New(t)
	w := scopeWorldOf(t)
	ctx := t.Context()

	v, err := w.issued(t, "collection", w.foo, "assets:01")
	x.NoError(err)
	id := v.GetKey().GetId()

	me, err := app.NewMeServiceClient(w.Conn).Get(bearing(ctx, w.erinKey), app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	said := map[string]string{}
	for _, k := range me.GetKeys() {
		said[k.GetAlias()] = k.GetScope()
	}
	x.Equal("assets:01", said["collection"], "MeService.Get")
	x.Equal("", said["erin"], "a key with none")

	keysOf := app.NewApiKeyServiceClient(w.Conn)
	got, err := keysOf.Get(bearing(ctx, w.fooKey), app.ApiKeyGetRequest_builder{Ref: app.ApiKeyRef_builder{Id: id}.Build()}.Build())
	x.NoError(err)
	x.Equal("assets:01", got.GetScope(), "ApiKeyService/Get, by the app")
	list, err := keysOf.List(bearing(ctx, w.fooKey), app.ApiKeyListRequest_builder{
		Filters: []*app.ApiKeyFilter{app.ApiKeyFilter_builder{Holder: app.HolderRef_builder{Id: w.erin.Bytes()}.Build()}.Build()},
	}.Build())
	x.NoError(err)
	x.Len(list.GetItems(), 1)
	x.Equal("assets:01", list.GetItems()[0].GetScope(), "ApiKeyService/List, by the app")

	signs, err := w.Ungated.Holder().SignsIn(ctx, app.HolderSignsInRequest_builder{Ref: app.HolderRef_builder{Id: w.erin.Bytes()}.Build()}.Build())
	x.NoError(err)
	said = map[string]string{}
	for _, k := range signs.GetKeys() {
		said[k.GetAlias()] = k.GetScope()
	}
	x.Equal("assets:01", said["collection"], "HolderService.SignsIn")

	var out strings.Builder
	x.NoError(cmd.ListKeys(ctx, &out, w.Server, cmd.Operator{}))
	line := ""
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, "/erin/collection\t") {
			line = l
		}
	}
	x.Contains(line, "\tscope=\"assets:01\"", "roster key list: %q", out.String())
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, "/erin/erin\t") {
			x.NotContains(l, "scope=", "a key with none")
		}
	}
}

// TestRosterMeGetSaysAKeysScope is `roster me get`: a key that carries a
// scope is printed with it, quoted, beside the app it is for, and a key that
// carries none is printed as it was.
func TestRosterMeGetSaysAKeysScope(t *testing.T) {
	x := require.New(t)
	b := cliUp(t, "/roster.MeService/Get")
	ctx := t.Context()

	tenant := app.TenantRef_builder{Id: b.Tenant.GetId()}.Build()
	loft, err := b.Server.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: tenant, Alias: "loft"}.Build())
	x.NoError(err)
	_, err = b.Server.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.Alice.GetId()}.Build(),
		App:    app.HolderRef_builder{Id: loft.GetId()}.Build(),
		Alias:  "one-collection", Secret: freshSum(t), Methods: []string{"/loft.AssetService/Get"},
		Scope: z.Ptr("assets:01"),
	}.Build())
	x.NoError(err)
	_, err = b.Server.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder: app.HolderRef_builder{Id: b.Alice.GetId()}.Build(),
		App:    app.HolderRef_builder{Id: loft.GetId()}.Build(),
		Alias:  "every-collection", Secret: freshSum(t), Methods: []string{"/loft.AssetService/Get"},
	}.Build())
	x.NoError(err)

	out, err := cliRun(t, &b.Hers, "me", "get")
	x.NoError(err)
	seen := 0
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "\tone-collection\t"):
			seen++
			x.Contains(line, "app=@loft\tscope=\"assets:01\"", "me get does not say the key's scope: %q", line)
		case strings.HasPrefix(line, "key"):
			x.NotContains(line, "scope=", "me get says a scope of a key that carries none: %q", line)
		}
	}
	x.Equal(1, seen, "%s", out)
}
