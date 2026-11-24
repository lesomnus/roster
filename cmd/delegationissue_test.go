package cmd_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
)

// issuedFor is a delegation `who` hands `to` for `methods`, minted at the
// stack as `who` themselves, as a port would have framed them.
func issuedFor(t *testing.T, ctx context.Context, b *keyedBuilt, who, to pdid.Id, methods ...string) *app.DelegationIssueResponse {
	t.Helper()

	res, err := b.Walled.Delegation().Issue(issuingAs(ctx, b, who), app.DelegationIssueRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		App:     app.HolderRef_builder{Id: to.Bytes()}.Build(),
		Methods: methods,
	}.Build())
	require.NoError(t, err)
	require.NotEmpty(t, res.GetToken())

	return res
}

// issuingAs is a call of `DelegationService/Issue` made at the stack as
// somebody of the harness's tenant.
func issuingAs(ctx context.Context, b *keyedBuilt, who pdid.Id) context.Context {
	return installedAs(ctx, who, b.Contoso, app.DelegationService_Issue_FullMethodName)
}

// TestSomebodyHandsAnAppADelegationOfTheirOwn.
//
// An app's page that put somebody into the app's group because its
// administrator pressed the button could only ask roster as the app: the
// trail said the app and nobody else, and *you hand out only what you hold*
// was asked of the app. roster answers an app acting for a person where it
// presents its own key and a delegation issued to it in `roster-as`, and
// nothing handed an app one: only a front door was, at a sign-in it made.
// Answered: no such method; right is `DelegationService/Issue`, the person
// handing the app a delegation of their own, answered once.
//
// The app presents it beside its own key and is answered as the person,
// held to what they hold: a write the administrator may make is made, and
// the trail names them, the app and the delegation; the same write by somebody who does not hold it
// there is refused as it would be without the app. Another app presenting
// the token, and the token alone, are refused.
func TestSomebodyHandsAnAppADelegationOfTheirOwn(t *testing.T) {
	b := keyFor(t)
	ctx := t.Context()
	x := require.New(t)

	foo := addHolder(t, ctx, b.Server, b.Contoso, "foo")
	bar := addHolder(t, ctx, b.Server, b.Contoso, "bar")
	erin := addHolder(t, ctx, b.Server, b.Contoso, "erin")
	bob := addHolder(t, ctx, b.Server, b.Contoso, "bob")
	carol := addHolder(t, ctx, b.Server, b.Contoso, "carol")
	tenant := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	// Foo's group, and roster's verb for putting somebody into an app's
	// group, held by erin for Foo and by bob for Bar.
	users, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: tenant, Alias: "foo-users", App: app.HolderRef_builder{Id: foo.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	enrols, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: tenant, Alias: "enrols", Methods: []string{app.GroupMembershipService_Add_FullMethodName},
	}.Build())
	x.NoError(err)
	for who, of := range map[pdid.Id]pdid.Id{erin: foo, bob: bar} {
		_, err := b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: enrols.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			App:    app.HolderRef_builder{Id: of.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
	}

	// Each app's own key, of roster's, and nothing bound to either: what Foo
	// writes through the delegation it writes as erin, or not at all.
	fooKey := mintFor(t, ctx, b, foo, "foo", []string{"/roster.*/*", "/payday.TokenService/Introspect"}, time.Time{})
	barKey := mintFor(t, ctx, b, bar, "bar", []string{"/roster.*/*", "/payday.TokenService/Introspect"}, time.Time{})
	permits(t, ctx, b, b.Contoso, foo, "foo-introspects", "/payday.TokenService/Introspect")
	permits(t, ctx, b, b.Contoso, bar, "bar-introspects", "/payday.TokenService/Introspect")

	// erin hands Foo one over the wire, with a key of her own that lists the
	// one verb: everybody of a tenant may, over what is theirs.
	erinKey := mintFor(t, ctx, b, erin, "erin", []string{app.DelegationService_Issue_FullMethodName}, time.Time{})
	delegations := app.NewDelegationServiceClient(b.Conn)
	res, err := delegations.Issue(bearing(ctx, erinKey), app.DelegationIssueRequest_builder{
		Holder:  app.HolderRef_builder{Id: erin.Bytes()}.Build(),
		App:     app.HolderRef_builder{Id: foo.Bytes()}.Build(),
		Methods: []string{app.GroupMembershipService_Add_FullMethodName},
	}.Build())
	x.NoError(err)
	x.NotEmpty(res.GetToken())
	x.Equal(foo.Bytes(), res.GetDelegation().GetIssuedTo(), "issued to the app")
	x.Equal(erin.Bytes(), res.GetDelegation().GetHolder().GetId(), "of erin")
	x.Empty(res.GetDelegation().GetSecret(), "the hash left with the token")

	memberships := app.NewGroupMembershipServiceClient(b.Conn)
	enrol := func(ctx context.Context, who pdid.Id) (*app.GroupMembership, error) {
		return memberships.Add(ctx, app.GroupMembershipAddRequest_builder{
			Group:  app.GroupRef_builder{Id: users.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		}.Build())
	}

	t.Run("the app puts somebody into its group as the person", func(t *testing.T) {
		x := require.New(t)

		m, err := enrol(acting(ctx, fooKey, res.GetToken()), carol)
		x.NoError(err)

		line, err := b.Ent.Audit.Query().
			Where(entaudit.ObjectIdEQ(mustId(t, m.GetId()).Uuid()), entaudit.ActionEQ(app.GroupMembershipService_Add_FullMethodName)).
			Only(ctx)
		x.NoError(err)
		x.Equal(erin.Uuid(), line.ActorId, "the trail names somebody other than the administrator")
		x.NotNil(line.CallerId, "the trail names no app")
		x.Equal(foo.Uuid(), *line.CallerId, "the trail names somebody other than the app")
		x.NotNil(line.CallerTenantId)
		x.Equal(b.Contoso.Uuid(), *line.CallerTenantId)
		x.NotNil(line.Presence)
		x.Equal("roster-as:"+mustId(t, res.GetDelegation().GetId()).String(), *line.Presence, "the trail names no delegation")
	})

	t.Run("and the same call by somebody who does not hold the verb there is refused as without the app", func(t *testing.T) {
		x := require.New(t)

		bobs := issuedFor(t, ctx, b, bob, foo, app.GroupMembershipService_Add_FullMethodName)
		_, through := enrol(acting(ctx, fooKey, bobs.GetToken()), bob)
		x.Error(through, "bob holds the verb for Bar, and put somebody into Foo's group through Foo")

		_, itself := enrol(bearing(ctx, mintFor(t, ctx, b, bob, "bob", []string{"/roster.*/*"}, time.Time{})), bob)
		x.Error(itself)
		x.Equal(status.Code(itself), status.Code(through), "through: %v; without the app: %v", through, itself)
	})

	t.Run("another app presenting the token is refused", func(t *testing.T) {
		_, err := enrol(acting(ctx, barKey, res.GetToken()), bob)
		require.Equal(t, codes.Unauthenticated, status.Code(err), "%v", err)
	})

	t.Run("the token alone is refused", func(t *testing.T) {
		_, err := enrol(bearing(ctx, res.GetToken()), bob)
		require.Equal(t, codes.Unauthenticated, status.Code(err), "%v", err)
	})

	t.Run("and Introspect answers it to the app it was issued to alone", func(t *testing.T) {
		x := require.New(t)

		tokens := pdpb.NewTokenServiceClient(b.Conn)
		ask := func(key string) (*pdpb.TokenIntrospectResponse, error) {
			return tokens.Introspect(bearing(ctx, key), pdpb.TokenIntrospectRequest_builder{Token: res.GetToken()}.Build())
		}

		v, err := ask(fooKey)
		x.NoError(err)
		x.Equal(erin.Bytes(), v.GetId())

		_, err = ask(barKey)
		x.Equal(codes.NotFound, status.Code(err), "%v", err)
	})
}

// TestADelegationHandedToAnAppIsHeldToItsRules: what `Issue` refuses.
//
// A delegation is answered as the person it is of, so what it names is held
// to what they may call, and it adds nothing: a method they may call nowhere
// is refused, naming it. It lives no longer than `delegation.life` -- a day
// unless said -- and none is that bound; later is refused saying it, rather
// than shortened without a word. It is issued to an app of the person's own
// tenant, live, and not to the person themselves -- nor, through a
// delegation, to the app the call came through, which would be its own
// renewed. And it is the person's own act: for anybody else it is the rule
// about a way in.
func TestADelegationHandedToAnAppIsHeldToItsRules(t *testing.T) {
	b := keyFor(t)
	ctx := t.Context()

	foo := addHolder(t, ctx, b.Server, b.Contoso, "foo")
	erin := addHolder(t, ctx, b.Server, b.Contoso, "erin")
	carol := addHolder(t, ctx, b.Server, b.Contoso, "carol")
	permits(t, ctx, b, b.Contoso, erin, "lister", app.RoleService_List_FullMethodName)
	permits(t, ctx, b, b.Contoso, carol, "administers", "/roster.*/*")

	issue := func(ctx context.Context, req app.DelegationIssueRequest_builder) (*app.DelegationIssueResponse, error) {
		if req.Holder == nil {
			req.Holder = app.HolderRef_builder{Id: erin.Bytes()}.Build()
		}
		if req.App == nil {
			req.App = app.HolderRef_builder{Id: foo.Bytes()}.Build()
		}
		if req.Methods == nil {
			req.Methods = []string{app.RoleService_List_FullMethodName}
		}

		return b.Walled.Delegation().Issue(ctx, req.Build())
	}
	as := issuingAs(ctx, b, erin)

	t.Run("a method held nowhere is refused, naming it", func(t *testing.T) {
		x := require.New(t)

		for _, m := range []string{app.HolderService_Erase_FullMethodName, "/foo.AssetService/Get", "/roster.*/*"} {
			_, err := issue(as, app.DelegationIssueRequest_builder{Methods: []string{app.RoleService_List_FullMethodName, m}})
			x.Equal(codes.PermissionDenied, status.Code(err), "%s: %v", m, err)
			x.Contains(status.Convert(err).Message(), m)
		}
	})

	t.Run("and what is held, or asked of nobody, is taken", func(t *testing.T) {
		_, err := issue(as, app.DelegationIssueRequest_builder{Methods: []string{
			app.RoleService_List_FullMethodName, app.MeService_Get_FullMethodName, app.EmailService_Add_FullMethodName,
		}})
		require.NoError(t, err)
	})

	t.Run("none takes delegation.life", func(t *testing.T) {
		x := require.New(t)

		before := time.Now()
		res, err := issue(as, app.DelegationIssueRequest_builder{})
		x.NoError(err)
		after := time.Now()

		end := res.GetDelegation().GetDateExpired().AsTime()
		x.False(end.Before(before.Add(core.DelegationLife)), "ends at %s", end)
		x.False(end.After(after.Add(core.DelegationLife)), "ends at %s", end)
	})

	t.Run("an end within it is taken as asked", func(t *testing.T) {
		x := require.New(t)

		at := time.Now().Add(time.Hour).Truncate(time.Second)
		res, err := issue(as, app.DelegationIssueRequest_builder{DateExpired: timestamppb.New(at)})
		x.NoError(err)
		x.True(at.Equal(res.GetDelegation().GetDateExpired().AsTime()), "%s", res.GetDelegation().GetDateExpired().AsTime())
	})

	t.Run("an end later than delegation.life is refused, saying the bound", func(t *testing.T) {
		x := require.New(t)

		_, err := issue(as, app.DelegationIssueRequest_builder{DateExpired: timestamppb.New(time.Now().Add(core.DelegationLife + time.Hour))})
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.Contains(status.Convert(err).Message(), "delegation.life")
		x.Contains(status.Convert(err).Message(), core.DelegationLife.String())

		_, err = issue(as, app.DelegationIssueRequest_builder{DateExpired: timestamppb.New(time.Now().Add(-time.Minute))})
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	})

	t.Run("the app is the person themselves, of another tenant, or erased: refused", func(t *testing.T) {
		x := require.New(t)

		_, err := issue(as, app.DelegationIssueRequest_builder{App: app.HolderRef_builder{Id: erin.Bytes()}.Build()})
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

		elsewhere := addHolder(t, ctx, b.Server, add(t, ctx, b.Server, "fabrikam"), "foo")
		_, err = issue(as, app.DelegationIssueRequest_builder{App: app.HolderRef_builder{Id: elsewhere.Bytes()}.Build()})
		x.Equal(codes.NotFound, status.Code(err), "%v", err)

		gone := addHolder(t, ctx, b.Server, b.Contoso, "gone")
		_, err = b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: gone.Bytes()}.Build())
		x.NoError(err)
		_, err = issue(as, app.DelegationIssueRequest_builder{App: app.HolderRef_builder{Id: gone.Bytes()}.Build()})
		x.Equal(codes.NotFound, status.Code(err), "%v", err)
	})

	t.Run("whose, app and methods are each required", func(t *testing.T) {
		x := require.New(t)

		for _, req := range []*app.DelegationIssueRequest{
			app.DelegationIssueRequest_builder{App: app.HolderRef_builder{Id: foo.Bytes()}.Build(), Methods: []string{app.RoleService_List_FullMethodName}}.Build(),
			app.DelegationIssueRequest_builder{Holder: app.HolderRef_builder{Id: erin.Bytes()}.Build(), Methods: []string{app.RoleService_List_FullMethodName}}.Build(),
			app.DelegationIssueRequest_builder{Holder: app.HolderRef_builder{Id: erin.Bytes()}.Build(), App: app.HolderRef_builder{Id: foo.Bytes()}.Build()}.Build(),
		} {
			_, err := b.Walled.Delegation().Issue(as, req)
			x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		}
	})

	t.Run("the app a call came through is not handed a delegation by it", func(t *testing.T) {
		x := require.New(t)

		// Foo, acting for erin through a delegation of hers that may issue,
		// asks for one for itself, and for another app.
		bar := addHolder(t, ctx, b.Server, b.Contoso, "bar")
		fooKey := mintFor(t, ctx, b, foo, "foo", []string{"/roster.*/*"}, time.Time{})
		hers := issuedFor(t, ctx, b, erin, foo, app.DelegationService_Issue_FullMethodName)
		delegations := app.NewDelegationServiceClient(b.Conn)
		ask := func(to pdid.Id) error {
			_, err := delegations.Issue(acting(ctx, fooKey, hers.GetToken()), app.DelegationIssueRequest_builder{
				Holder:  app.HolderRef_builder{Id: erin.Bytes()}.Build(),
				App:     app.HolderRef_builder{Id: to.Bytes()}.Build(),
				Methods: []string{app.RoleService_List_FullMethodName},
			}.Build())
			return err
		}

		err := ask(foo)
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.Contains(status.Convert(err).Message()+fmt.Sprint(status.Convert(err).Details()), "the app this call came through is not handed a delegation by it")
		x.NoError(ask(bar), "another app is what a front door approves for")
	})

	t.Run("for anybody else it is the rule about a way in", func(t *testing.T) {
		x := require.New(t)

		// erin, who holds one verb, may not write a way into carol's account,
		// who holds everything; carol, who holds more than erin, may.
		_, err := issue(issuingAs(ctx, b, erin), app.DelegationIssueRequest_builder{Holder: app.HolderRef_builder{Id: carol.Bytes()}.Build()})
		x.Error(err)
		x.Contains([]codes.Code{codes.NotFound, codes.PermissionDenied}, status.Code(err), "%v", err)

		res, err := issue(issuingAs(ctx, b, carol), app.DelegationIssueRequest_builder{})
		x.NoError(err)
		x.Equal(erin.Bytes(), res.GetDelegation().GetHolder().GetId())
	})
}

// TestWhatEndsADelegationHandedToAnApp: it lives a day and not a quarter of
// an hour, so what ends a delegation of somebody's is asked of it one by one.
// Its own end; the app ending it with the token; the person ending it by
// reference; signing out everywhere; a suspension; an erasure: the next call
// through it is refused each time.
func TestWhatEndsADelegationHandedToAnApp(t *testing.T) {
	b := keyFor(t)
	ctx := t.Context()

	foo := addHolder(t, ctx, b.Server, b.Contoso, "foo")
	fooKey := mintFor(t, ctx, b, foo, "foo", []string{"/roster.*/*"}, time.Time{})
	permits(t, ctx, b, b.Contoso, foo, "foo-revokes", app.DelegationService_Revoke_FullMethodName)
	roles := app.NewRoleServiceClient(b.Conn)
	through := func(token string) error {
		_, err := roles.List(acting(ctx, fooKey, token), app.RoleListRequest_builder{}.Build())
		return err
	}

	person := func(t *testing.T, alias string) pdid.Id {
		who := addHolder(t, ctx, b.Server, b.Contoso, alias)
		permits(t, ctx, b, b.Contoso, who, alias+"-lists", app.RoleService_List_FullMethodName)

		return who
	}
	ended := func(t *testing.T, token string) {
		t.Helper()
		require.Equal(t, codes.Unauthenticated, status.Code(through(token)), "the next call through it was answered")
	}

	t.Run("its own end", func(t *testing.T) {
		x := require.New(t)
		who := person(t, "ends")

		res, err := b.Walled.Delegation().Issue(issuingAs(ctx, b, who), app.DelegationIssueRequest_builder{
			Holder:      app.HolderRef_builder{Id: who.Bytes()}.Build(),
			App:         app.HolderRef_builder{Id: foo.Bytes()}.Build(),
			Methods:     []string{app.RoleService_List_FullMethodName},
			DateExpired: timestamppb.New(time.Now().Add(time.Second)),
		}.Build())
		x.NoError(err)
		x.NoError(through(res.GetToken()))

		// Asked of the deadline the row says, and not of a stopwatch.
		time.Sleep(time.Until(res.GetDelegation().GetDateExpired().AsTime()))
		ended(t, res.GetToken())
	})

	t.Run("the app ending it with the token", func(t *testing.T) {
		x := require.New(t)
		who := person(t, "revokes")

		res := issuedFor(t, ctx, b, who, foo, app.RoleService_List_FullMethodName)
		x.NoError(through(res.GetToken()))

		_, err := app.NewDelegationServiceClient(b.Conn).Revoke(bearing(ctx, fooKey),
			app.DelegationRevokeRequest_builder{Token: res.GetToken()}.Build())
		x.NoError(err)
		ended(t, res.GetToken())
	})

	t.Run("the person ending their own by reference", func(t *testing.T) {
		x := require.New(t)
		who := person(t, "erases")

		res := issuedFor(t, ctx, b, who, foo, app.RoleService_List_FullMethodName)
		x.NoError(through(res.GetToken()))

		_, err := b.Walled.Delegation().Erase(installedAs(ctx, who, b.Contoso, app.DelegationService_Erase_FullMethodName),
			app.DelegationRef_builder{Id: res.GetDelegation().GetId()}.Build())
		x.NoError(err)
		ended(t, res.GetToken())
	})

	t.Run("signing out everywhere", func(t *testing.T) {
		x := require.New(t)
		who := person(t, "leaves")

		res := issuedFor(t, ctx, b, who, foo, app.RoleService_List_FullMethodName)
		x.NoError(through(res.GetToken()))

		// The next millisecond, since a delegation minted in the one an
		// invalidation was written in is read as too old, and this is the
		// other way round.
		time.Sleep(2 * time.Millisecond)
		_, err := app.NewMeServiceClient(b.Conn).SignOutEverywhere(
			bearing(ctx, mintFor(t, ctx, b, who, "leaves", []string{app.MeService_SignOutEverywhere_FullMethodName}, time.Time{})),
			app.MeSignOutEverywhereRequest_builder{}.Build())
		x.NoError(err)
		ended(t, res.GetToken())
	})

	t.Run("a suspension", func(t *testing.T) {
		x := require.New(t)
		who := person(t, "suspended")

		res := issuedFor(t, ctx, b, who, foo, app.RoleService_List_FullMethodName)
		x.NoError(through(res.GetToken()))

		_, err := b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: app.HolderRef_builder{Id: who.Bytes()}.Build()}.Build())
		x.NoError(err)
		ended(t, res.GetToken())
	})

	t.Run("an erasure", func(t *testing.T) {
		x := require.New(t)
		who := person(t, "erased")

		res := issuedFor(t, ctx, b, who, foo, app.RoleService_List_FullMethodName)
		x.NoError(through(res.GetToken()))

		_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
		x.NoError(err)
		ended(t, res.GetToken())
	})

}
