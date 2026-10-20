package cmd_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	entbinding "github.com/lesomnus/roster/internal/ent/binding"
	entcredential "github.com/lesomnus/roster/internal/ent/credential"
	entemail "github.com/lesomnus/roster/internal/ent/email"
	entgroup "github.com/lesomnus/roster/internal/ent/group"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	enthost "github.com/lesomnus/roster/internal/ent/host"
	entidentity "github.com/lesomnus/roster/internal/ent/identity"
	entmaildomain "github.com/lesomnus/roster/internal/ent/maildomain"
	entrole "github.com/lesomnus/roster/internal/ent/role"
	entsite "github.com/lesomnus/roster/internal/ent/site"
	entteam "github.com/lesomnus/roster/internal/ent/team"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/vouch"
)

// A tenant leaving (#98): close, restore while the grace lasts, destroy, and
// its alias held after it. `server/core/close.go` and `server/forget/tenant.go`.

func closes(t *testing.T, ctx context.Context, s app.Server, tenant pdid.Id) *app.Tenant {
	t.Helper()

	v, err := s.Tenant().Close(ctx, app.TenantCloseRequest_builder{
		Ref: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)

	return v
}

func restores(ctx context.Context, s app.Server, tenant pdid.Id) error {
	_, err := s.Tenant().Restore(ctx, app.TenantRestoreRequest_builder{
		Ref: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
	}.Build())

	return err
}

// TestAClosedTenantLetsNobodyIn is `TestADisabledHolderIsNotToSignInAndNotSignedIn`
// one level up, and reaches the same places for the same reason: nothing
// generated reads a timestamp, so `date_closed` is what the refusals make it.
// A password, a tenant key and a delegation -- asked of roster and introspected
// by an app in front -- and the tenant's own name at a front door. And a
// restore gives every one of them back, because a close destroys nothing.
func TestAClosedTenantLetsNobodyIn(t *testing.T) {
	const listHolders = "/roster.HolderService/List"

	x := require.New(t)
	b := keyFor(t, verify, introspect)
	ctx := t.Context()

	// Somebody else of contoso who may list, for a call over the wire; the
	// person whose tokens an app introspects holds nothing, as in
	// `TestASuspendedPersonIsSuspendedInFrontOfEveryApp`.
	lister := addHolder(t, ctx, b.Server, b.Contoso, "lister")
	mayList(t, ctx, b, lister, listHolders)

	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:   "contoso.example.com",
	}.Build())
	x.NoError(err)

	v := vouch.New(b.Ungated, b.Ungated)
	verifies := func() bool {
		res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
		require.NoError(t, err)

		return res.GetOk()
	}
	key := mintFor(t, ctx, b, b.Who, "hers", []string{listHolders}, time.Time{})
	held := delegates(t, ctx, b, b.Who, []string{listHolders}, time.Hour)
	listing := mintFor(t, ctx, b, lister, "theirs", []string{listHolders}, time.Time{})
	tokens := pdpb.NewTokenServiceClient(b.Conn)
	introspects := func(token string) error {
		_, err := tokens.Introspect(bearing(ctx, b.Token), pdpb.TokenIntrospectRequest_builder{Token: token}.Build())

		return err
	}
	lists := func() error {
		_, err := app.NewHolderServiceClient(b.Conn).List(bearing(ctx, listing), app.HolderListRequest_builder{}.Build())

		return err
	}
	whose := func() error {
		_, err := front.New(b.Ungated).WhoseHost(ctx, app.FrontWhoseHostRequest_builder{Host: "contoso.example.com"}.Build())

		return err
	}

	x.True(verifies(), "the control: they sign in to begin with")
	x.NoError(lists())
	x.NoError(introspects(key))
	x.NoError(introspects(held))
	x.NoError(whose())

	got := closes(t, ctx, b.Ungated, b.Contoso)
	x.NotNil(got.GetDateClosed())

	t.Run("nobody of it signs in", func(t *testing.T) {
		require.False(t, verifies())
	})
	t.Run("and what they already held stops working", func(t *testing.T) {
		x := require.New(t)

		x.Equal(codes.Unauthenticated, status.Code(lists()), "a tenant key outlived its tenant closing")
		x.Equal(codes.NotFound, status.Code(introspects(key)), "an app in front kept letting them in with a key")
		x.Equal(codes.NotFound, status.Code(introspects(held)), "and with a delegation")
	})
	t.Run("and its name says it is closed", func(t *testing.T) {
		require.Equal(t, codes.FailedPrecondition, status.Code(whose()))
	})

	// Nothing was destroyed, so everything comes back.
	x.NoError(restores(ctx, b.Ungated, b.Contoso))
	x.True(verifies())
	x.NoError(lists())
	x.NoError(introspects(key))
	x.NoError(introspects(held))
	x.NoError(whose())
}

// TestANominationIntoAClosedTenantActsAsNobody: a deployment key narrowed with
// `roster-at` is answered as the holder the tenant nominated, and that holder's
// tenant is closed -- so the key is refused there, as everybody of it is.
func TestANominationIntoAClosedTenantActsAsNobody(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, app.TenantService_Get_FullMethodName)
	ctx := t.Context()

	nominates(t, b.Server, b.Contoso, b.Service, b.Who)
	asks := func() error {
		_, err := app.NewMeServiceClient(b.Conn).Get(arrivedAt(ctx, b.Token, front.AtTenant("contoso")), app.MeGetRequest_builder{}.Build())

		return err
	}
	x.NoError(asks(), "the control: the nomination answers to begin with")

	closes(t, ctx, b.Ungated, b.Contoso)
	x.Equal(codes.Unauthenticated, status.Code(asks()), "a deployment key went on acting inside a closed tenant")
}

// TestClosingIsRefusedWhereNobodyCouldUndoIt.
func TestClosingIsRefusedWhereNobodyCouldUndoIt(t *testing.T) {
	b := keyFor(t)
	ctx := t.Context()

	t.Run("twice, which would restart the grace", func(t *testing.T) {
		x := require.New(t)
		t0 := add(t, ctx, b.Server, "twice")

		closes(t, ctx, b.Ungated, t0)
		_, err := b.Ungated.Tenant().Close(ctx, app.TenantCloseRequest_builder{
			Ref: app.TenantRef_builder{Id: t0.Bytes()}.Build(),
		}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
	})

	t.Run("on the control plane, whose one tenant is the deployment", func(t *testing.T) {
		x := require.New(t)
		k := add(t, ctx, b.Control, "operators")

		_, err := b.Control.Ungated.Tenant().Close(ctx, app.TenantCloseRequest_builder{
			Ref: app.TenantRef_builder{Id: k.Bytes()}.Build(),
		}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
	})

	t.Run("on a tenant a file declared, from inside it", func(t *testing.T) {
		x := require.New(t)
		t0 := add(t, ctx, b.Server, "declared")
		who := addHolder(t, ctx, b.Server, t0, "someone")
		// The mark `roster resources` leaves, written as it writes it.
		x.NoError(b.Ent.Tenant.UpdateOneId(t0.Uuid()).SetLabels(map[string]string{cmd.Declared: "customers.yaml"}).Exec(ctx))

		bt := &built{Server: b.Server}
		_, err := b.Ungated.Tenant().Close(bt.asNobody(ctx, who, t0), app.TenantCloseRequest_builder{
			Ref: app.TenantRef_builder{Id: t0.Bytes()}.Build(),
		}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err))
	})

	t.Run("and a tenant is made open", func(t *testing.T) {
		x := require.New(t)

		for _, req := range []*app.TenantAddRequest{
			app.TenantAddRequest_builder{Alias: "born-closed", DateClosed: timestamppb.Now()}.Build(),
			app.TenantAddRequest_builder{Alias: "born-destroyed", DateDestroyed: timestamppb.Now()}.Build(),
			app.TenantAddRequest_builder{Alias: "born-held", Hold: z.Ptr("a case nobody opened")}.Build(),
		} {
			_, err := b.Ungated.Tenant().Add(ctx, req)
			x.Equal(codes.InvalidArgument, status.Code(err), req.GetAlias())
		}
	})
}

// TestARestoreHasAGrace: `tenant.restore_within` after a close, the tenant is
// destroyed and `Restore` refuses -- even before a sweep has got to it, because
// what decides is the window and not how recently something ran.
func TestARestoreHasAGrace(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, func(c *cmd.Config) { c.Tenant.RestoreWithin = time.Hour })

	closes(t, ctx, b.Ungated, b.Contoso)
	x.NoError(restores(ctx, b.Ungated, b.Contoso), "inside the grace")
	x.Equal(codes.FailedPrecondition, status.Code(restores(ctx, b.Ungated, b.Contoso)), "nothing to undo on an open tenant")

	closes(t, ctx, b.Ungated, b.Contoso)
	x.NoError(b.Ent.Tenant.UpdateOneId(b.Contoso.Uuid()).SetDateClosed(time.Now().Add(-2 * time.Hour)).Exec(ctx))
	x.Equal(codes.FailedPrecondition, status.Code(restores(ctx, b.Ungated, b.Contoso)), "a close restored after its grace ran out")

	vs, err := forget.DueTenants(ctx, b.Ent, time.Now().Add(-time.Hour))
	x.NoError(err)
	x.Contains(vs, b.Contoso)
	x.NotContains(vs, b.Fabrikam)
}

// TestADestroyedTenantLeavesNothingOfItsOwn.
//
// Every person forgotten -- their rows gone and their holder blank -- and every
// other row of the tenant removed, while the row and its alias stay: the trail
// names it by identifier, and the alias is held. The other tenant keeps every
// row it had. And a tenant half destroyed cannot be restored: the first write
// is the stamp.
func TestADestroyedTenantLeavesNothingOfItsOwn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Contoso, with a bit of everything; fabrikam, the same, to stay put.
	stock := func(in pdid.Id, alias string) {
		who := b.holder(t, ctx, in, "erin")
		b.identity(t, ctx, who, "github", "gh-erin-"+alias)
		b.addressOf(t, ctx, who, "erin@"+alias+".example")
		_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
		x.NoError(err)
		b.mayAnything(who, in)
		s := b.site(t, ctx, in, "hq")
		_, err = b.Ungated.Team().Add(ctx, app.TeamAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(), Alias: "ops",
			Site: app.SiteRef_builder{Id: s.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		_, err = b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(), Alias: "all",
		}.Build())
		x.NoError(err)
		_, err = b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(), Name: alias + ".example.com",
		}.Build())
		x.NoError(err)
		_, err = b.Ungated.MailDomain().Add(ctx, app.MailDomainAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(), Name: alias + ".example",
		}.Build())
		x.NoError(err)
	}
	stock(b.Contoso, "contoso")
	stock(b.Fabrikam, "fabrikam")

	count := func(in pdid.Id) map[string]int {
		k := in.Uuid()
		n := map[string]int{}
		var err error
		n["role"], err = b.Ent.Role.Query().Where(entrole.TenantId(k)).Count(ctx)
		x.NoError(err)
		n["site"], err = b.Ent.Site.Query().Where(entsite.TenantId(k)).Count(ctx)
		x.NoError(err)
		n["team"], err = b.Ent.Team.Query().Where(entteam.TenantId(k)).Count(ctx)
		x.NoError(err)
		n["group"], err = b.Ent.Group.Query().Where(entgroup.TenantId(k)).Count(ctx)
		x.NoError(err)
		n["host"], err = b.Ent.Host.Query().Where(enthost.TenantId(k)).Count(ctx)
		x.NoError(err)
		n["mail domain"], err = b.Ent.MailDomain.Query().Where(entmaildomain.TenantId(k)).Count(ctx)
		x.NoError(err)
		n["living holder"], err = b.Ent.Holder.Query().Where(entholder.TenantId(k), entholder.AliasNEQ("")).Count(ctx)
		x.NoError(err)
		n["email"], err = b.Ent.Email.Query().Where(entemail.HasHolderWith(entholder.TenantId(k))).Count(ctx)
		x.NoError(err)
		n["identity"], err = b.Ent.Identity.Query().Where(entidentity.HasHolderWith(entholder.TenantId(k))).Count(ctx)
		x.NoError(err)
		n["credential"], err = b.Ent.Credential.Query().Where(entcredential.HasHolderWith(entholder.TenantId(k))).Count(ctx)
		x.NoError(err)
		n["binding"], err = b.Ent.Binding.Query().Where(entbinding.Or(entbinding.HasRoleWith(entrole.TenantId(k)), entbinding.HasHolderWith(entholder.TenantId(k)))).Count(ctx)
		x.NoError(err)

		return n
	}
	theirs := count(b.Fabrikam)
	for k, v := range count(b.Contoso) {
		x.Positive(v, "contoso had no %s to begin with, so this proves nothing", k)
	}

	_, err := forget.DestroyTenant(ctx, b.Ent, b.Contoso, trail.Policy{}, audit.Contents{})
	x.ErrorContains(err, "is open", "an open tenant destroyed under the people signed in to it")

	closes(t, ctx, b.Ungated, b.Contoso)
	res, err := forget.DestroyTenant(ctx, b.Ent, b.Contoso, trail.Policy{}, audit.Contents{})
	x.NoError(err)
	x.Positive(res.People)
	x.Positive(res.Rows)

	for k, v := range count(b.Contoso) {
		x.Zero(v, "a destroyed tenant still has its %s", k)
	}
	x.Equal(theirs, count(b.Fabrikam), "destroying one tenant reached another")

	v, err := b.Ent.Tenant.Get(ctx, b.Contoso.Uuid())
	x.NoError(err)
	x.NotNil(v.DateDestroyed)
	x.Equal("contoso", v.Alias, "the alias is held, and the row is what holds it")
	x.Empty(v.Name)
	x.Empty(v.Labels)

	x.Equal(codes.FailedPrecondition, status.Code(restores(ctx, b.Ungated, b.Contoso)), "a destroyed tenant came back")

	// Again is nothing new, which is what lets a sweep finish one that stopped.
	again, err := forget.DestroyTenant(ctx, b.Ent, b.Contoso, trail.Policy{}, audit.Contents{})
	x.NoError(err)
	x.Zero(again.People)
	x.Zero(again.Rows)

	t.Run("and one that stopped part way is finished", func(t *testing.T) {
		x := require.New(t)

		// A tenant stamped and with somebody left: what an interrupted
		// destruction leaves.
		t0 := b.tenant(t, ctx, "halfway")
		b.holder(t, ctx, t0, "left")
		closes(t, ctx, b.Ungated, t0)
		x.NoError(b.Ent.Tenant.UpdateOneId(t0.Uuid()).SetDateDestroyed(time.Now()).Exec(ctx))

		x.Equal(codes.FailedPrecondition, status.Code(restores(ctx, b.Ungated, t0)), "a tenant half destroyed came back")

		vs, err := forget.Unfinished(ctx, b.Ent)
		x.NoError(err)
		x.Equal([]pdid.Id{t0}, vs)
		vs, err = forget.DueTenants(ctx, b.Ent, time.Now().Add(-time.Hour))
		x.NoError(err)
		x.Contains(vs, t0, "the sweep would not have finished it")
	})
}

// TestAHoldStopsEveryDestruction: an operator's hold keeps a tenant as it is.
// A destruction by hand is refused, the sweep passes it by, the people of it
// whose own grace runs out are not forgotten on a clock -- and lifted, the
// sweep finishes what it stopped. Placing and lifting are in the trail.
func TestAHoldStopsEveryDestruction(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, func(c *cmd.Config) { c.Tenant.RestoreWithin = time.Hour })

	erin := b.holder(t, ctx, b.Contoso, "erin")
	_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: erin.Bytes()}.Build())
	x.NoError(err)
	closes(t, ctx, b.Ungated, b.Contoso)
	x.NoError(b.Ent.Tenant.UpdateOneId(b.Contoso.Uuid()).SetDateClosed(time.Now().Add(-2 * time.Hour)).Exec(ctx))

	x.Error(forget.Hold(ctx, b.Ungated, b.Contoso, "  "), "a hold nobody can read the reason for")
	x.NoError(forget.Hold(ctx, b.Ungated, b.Contoso, "Contoso v. Fabrikam, 2026-123"))

	_, err = forget.DestroyTenant(ctx, b.Ent, b.Contoso, trail.Policy{}, audit.Contents{})
	x.ErrorIs(err, forget.ErrHeld)

	vs, err := forget.DueTenants(ctx, b.Ent, time.Now().Add(-time.Hour))
	x.NoError(err)
	x.NotContains(vs, b.Contoso, "the sweep would have destroyed a held tenant")

	people, err := forget.Due(ctx, b.Ent, time.Now().Add(time.Hour))
	x.NoError(err)
	x.NotContains(people, erin, "a held tenant's people were forgotten on a clock")

	// And the trail answers to it, as it answers to payday's own holds.
	ans, err := forget.Holds(b.Ent)(ctx, b.Contoso)
	x.NoError(err)
	x.NotNil(ans.Hold)
	x.Contains(ans.Hold.Why, "2026-123")
	ans, err = forget.Holds(b.Ent)(ctx, b.Fabrikam)
	x.NoError(err)
	x.Nil(ans.Hold)

	x.NoError(forget.Lift(ctx, b.Ungated, b.Contoso))
	x.Error(forget.Lift(ctx, b.Ungated, b.Contoso), "lifting what is not there")

	vs, err = forget.DueTenants(ctx, b.Ent, time.Now().Add(-time.Hour))
	x.NoError(err)
	x.Contains(vs, b.Contoso, "lifted, and still not due")
	people, err = forget.Due(ctx, b.Ent, time.Now().Add(time.Hour))
	x.NoError(err)
	x.Contains(people, erin)

	// Both acts are recorded: an operator holding and releasing a customer's
	// records is the kind of thing somebody later asks about.
	n, err := b.Ent.Audit.Query().Where(entaudit.ObjectId(b.Contoso.Uuid())).Count(ctx)
	x.NoError(err)
	x.GreaterOrEqual(n, 3, "close, hold and lift each leave a trail row")
}

// TestAnAliasIsHeldAfterItsTenantIsGone.
//
// A name that found somebody still finds them after the rows are gone, so it
// is not free the moment they are: the destroyed row keeps it for
// `tenant.alias_hold`, and then it is `gone-<identifier>` and anybody may take
// it. An operator may free it sooner, on purpose, and the trail says they did.
func TestAnAliasIsHeldAfterItsTenantIsGone(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	closes(t, ctx, b.Ungated, b.Contoso)
	_, err := forget.DestroyTenant(ctx, b.Ent, b.Contoso, trail.Policy{}, audit.Contents{})
	x.NoError(err)

	_, err = b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.Equal(codes.AlreadyExists, status.Code(err), "a destroyed tenant's name was free the moment it was destroyed")

	vs, err := forget.DueAliases(ctx, b.Ent, time.Now().Add(-cmd.DefaultAliasHold))
	x.NoError(err)
	x.NotContains(vs, b.Contoso, "held for a year, and due today")
	vs, err = forget.DueAliases(ctx, b.Ent, time.Now().Add(time.Minute))
	x.NoError(err)
	x.Equal([]pdid.Id{b.Contoso}, vs)

	before, err := b.Ent.Audit.Query().Where(entaudit.ObjectId(b.Contoso.Uuid())).Count(ctx)
	x.NoError(err)
	x.NoError(forget.FreeAlias(ctx, b.Ungated, b.Contoso))
	after, err := b.Ent.Audit.Query().Where(entaudit.ObjectId(b.Contoso.Uuid())).Count(ctx)
	x.NoError(err)
	x.Greater(after, before, "the name was released and nothing recorded it")

	v, err := b.Ent.Tenant.Get(ctx, b.Contoso.Uuid())
	x.NoError(err)
	x.Equal(forget.Gone(b.Contoso), v.Alias)
	x.True(strings.HasPrefix(v.Alias, "gone-"))

	_, err = b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err, "a released name is free")

	vs, err = forget.DueAliases(ctx, b.Ent, time.Now().Add(time.Minute))
	x.NoError(err)
	x.Empty(vs, "a name released once is due again")
}

// TestTheTenantSweepDestroysAndReleases is the two clocks on their own.
func TestTheTenantSweepDestroysAndReleases(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	closes(t, ctx, b.Ungated, b.Contoso)
	x.NoError(b.Ent.Tenant.UpdateOneId(b.Contoso.Uuid()).SetDateClosed(time.Now().Add(-2 * time.Hour)).Exec(ctx))

	pass := func(within, hold time.Duration) {
		t.Helper()
		ctx, stop := context.WithCancel(ctx)
		f := forget.SweepTenants(b.Ent, b.Ungated, within, hold, trail.Policy{}, audit.Contents{}, time.Hour)
		done := make(chan struct{})
		go func() { defer close(done); _ = f(ctx) }()
		// One pass runs at once; wait for its effect, then stop the loop.
		time.Sleep(200 * time.Millisecond)
		stop()
		<-done
	}

	pass(0, time.Hour)
	v, err := b.Ent.Tenant.Get(ctx, b.Contoso.Uuid())
	x.NoError(err)
	x.Nil(v.DateDestroyed, "with no window, destroying is an operator's act")

	pass(time.Hour, time.Hour)
	v, err = b.Ent.Tenant.Get(ctx, b.Contoso.Uuid())
	x.NoError(err)
	x.NotNil(v.DateDestroyed, "the grace ran out and nothing destroyed it")
	x.Equal("contoso", v.Alias)

	pass(time.Hour, time.Nanosecond)
	v, err = b.Ent.Tenant.Get(ctx, b.Contoso.Uuid())
	x.NoError(err)
	x.Equal(forget.Gone(b.Contoso), v.Alias, "the hold ran out and the name was not released")
}

// TestATenantClosingIsHeardOnEachOfItsPeople: what an app holds is sessions for
// people, so a tenant closing comes down the sync stream as an event for each
// person in it, carrying the tenant's closing beside their own three stamps --
// and is heard by nobody who cannot see the tenant. Over the wire, because only
// a write through the served stack is published.
func TestATenantClosingIsHeardOnEachOfItsPeople(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	conn := served(t, b.Server)
	b.mayAnything(b.ContosoUser, b.Contoso)
	as := asOverTheWire(ctx, b.ContosoUser)
	other := b.holder(t, ctx, b.Contoso, "other")
	elsewhere := b.holder(t, ctx, b.Fabrikam, "elsewhere")

	c := syncing(t, as, conn, b.holder(t, ctx, b.Contoso, "canary"))
	tenants := app.NewTenantServiceClient(conn)
	ref := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	// The stream sees the tenant before it closes, which is what makes the
	// closing a movement it can name for everybody -- a rename says nothing
	// about anybody, and nothing arrives for it.
	was, err := tenants.Get(as, app.TenantGetRequest_builder{
		Ref: ref, Select: app.TenantSelect_builder{DateUpdated: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	_, err = tenants.Update(as, app.TenantUpdateRequest_builder{
		Ref: ref, DateUpdated: was.GetDateUpdated(), Name: z.Ptr("Contoso Ltd"),
	}.Build())
	x.NoError(err)

	_, err = tenants.Close(as, app.TenantCloseRequest_builder{Ref: ref}.Build())
	x.NoError(err)

	want := map[string]bool{string(b.ContosoUser.Bytes()): true, string(other.Bytes()): true}
	for len(want) > 0 {
		v := about2(t, c)
		x.NotEqual(elsewhere.Bytes(), v.GetHolder(), "an app was told about a tenant it cannot read")
		if !want[string(v.GetHolder())] {
			continue
		}
		delete(want, string(v.GetHolder()))

		x.Equal(app.SyncReason_SYNC_REASON_CLOSED, v.GetReason())
		x.NotNil(v.GetDateClosed(), "the fact itself was left off the event")
		x.Equal(b.Contoso.Bytes(), v.GetTenant())
		x.Nil(v.GetDateDisabled(), "a tenant closing is not its people suspended")
	}
}

// TestARestoredTenantIsHeardToo: an app that dropped a tenant's sessions has no
// other way to learn it may stop refusing them. Heard by a deployment key, which
// hears every tenant -- and may close and restore one, which is the operator's
// path over the wire.
func TestARestoredTenantIsHeardToo(t *testing.T) {
	x := require.New(t)
	b := keyFor(t,
		app.SyncService_Watch_FullMethodName,
		app.HolderService_Invalidate_FullMethodName,
		app.TenantService_Get_FullMethodName,
		app.TenantService_Update_FullMethodName,
		app.TenantService_Close_FullMethodName,
		app.TenantService_Restore_FullMethodName,
	)
	ctx, stop := context.WithCancel(t.Context())
	defer stop()

	as := bearing(ctx, b.Token)
	c := syncing(t, as, b.Conn, addHolder(t, ctx, b.Server, b.Contoso, "canary"))
	tenants := app.NewTenantServiceClient(b.Conn)
	ref := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	was, err := tenants.Get(as, app.TenantGetRequest_builder{
		Ref: ref, Select: app.TenantSelect_builder{DateUpdated: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	_, err = tenants.Update(as, app.TenantUpdateRequest_builder{Ref: ref, DateUpdated: was.GetDateUpdated(), Desc: z.Ptr("seen")}.Build())
	x.NoError(err)

	_, err = tenants.Close(as, app.TenantCloseRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	v := about(t, c, b.Who)
	x.Equal(app.SyncReason_SYNC_REASON_CLOSED, v.GetReason())
	x.NotNil(v.GetDateClosed())

	_, err = tenants.Restore(as, app.TenantRestoreRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	v = about(t, c, b.Who)
	x.Equal(app.SyncReason_SYNC_REASON_REOPENED, v.GetReason())
	x.Nil(v.GetDateClosed(), "restored, and the event still says closed")
}

// TestATenantLeavesFromATerminal is the commands, from a shell on the box: close
// and restore, a hold that stops `forget` and is listed, and `forget --reuse`.
// And `@newco` naming a tenant and a person at once is refused rather than
// guessed at, since destroying the wrong one is not a mistake anybody takes back.
func TestATenantLeavesFromATerminal(t *testing.T) {
	x := require.New(t)
	// Alice's key needs a method of some kind; nothing here goes through it.
	b := cliUp(t, "/roster.MeService/Get")
	ctx := t.Context()

	newco := mustId(t, b.Tenant.GetId())
	row := func() *ent.Tenant {
		t.Helper()
		v, err := b.Server.Ent.Tenant.Get(ctx, newco.Uuid())
		require.NoError(t, err)

		return v
	}

	_, err := cliRun(t, &b.Local, "tenant", "close", "@newco")
	x.NoError(err)
	x.NotNil(row().DateClosed)

	_, err = term(t, cli.NewCmdRestore(&b.Local), "@newco")
	x.NoError(err)
	x.Nil(row().DateClosed, "restored, and still closed")

	// Somebody called newco, in another tenant.
	other := add(t, ctx, b.Server, "other")
	addHolder(t, ctx, b.Server, other, "newco")
	_, err = term(t, cli.NewCmdRestore(&b.Local), "@newco")
	x.ErrorContains(err, "#tenant", "a name that is a tenant and a person was guessed at")

	_, err = cliRun(t, &b.Local, "tenant", "close", "@newco")
	x.NoError(err)
	_, err = term(t, cli.NewCmdHold(&b.Local), "place", "--why", "Contoso v. Newco", "@newco#tenant")
	x.NoError(err)
	out, err := term(t, cli.NewCmdHold(&b.Local), "ls")
	x.NoError(err)
	x.Contains(out, "@newco")
	x.Contains(out, "Contoso v. Newco")

	_, err = term(t, cli.NewCmdForget(&b.Local), "@newco#tenant")
	x.ErrorIs(err, forget.ErrHeld)
	x.Nil(row().DateDestroyed)

	_, err = term(t, cli.NewCmdHold(&b.Local), "lift", "@newco#tenant")
	x.NoError(err)
	_, err = term(t, cli.NewCmdForget(&b.Local), "--reuse", "@newco#tenant")
	x.NoError(err)

	v := row()
	x.NotNil(v.DateDestroyed)
	x.Equal(forget.Gone(newco), v.Alias, "--reuse, and the name is still held")
}

// term runs one of the commands that are not entity verbs, as a shell does.
func term(t *testing.T, k *xli.Command, args ...string) (string, error) {
	t.Helper()

	out := &bytes.Buffer{}
	k.Writer = out
	err := k.Run(t.Context(), args)

	return out.String(), err
}
