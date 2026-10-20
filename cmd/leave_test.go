package cmd_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/me"
)

const (
	leaveMe    = "/roster.MeService/Leave"
	watchSync  = "/roster.SyncService/Watch"
	invalidate = "/roster.HolderService/Invalidate"
)

// offersLeave writes a tenant's `config.leave`, the way its administrator's
// screen or a resources file would leave it.
func offersLeave(t *testing.T, ctx context.Context, s *cmd.Server, tenant pdid.Id, on bool) {
	t.Helper()
	x := require.New(t)

	v, err := s.Ent.Tenant.Get(ctx, tenant.Uuid())
	x.NoError(err)
	cfg := &app.TenantConfig{}
	if v.Config != nil {
		cfg = proto.Clone(v.Config).(*app.TenantConfig)
	}
	cfg.SetLeave(on)
	x.NoError(s.Ent.Tenant.UpdateOneId(tenant.Uuid()).SetConfig(cfg).Exec(ctx))
}

// signedInAt is a delegation for somebody minted at `at`: what a front door
// holds for a person who signed in then.
func signedInAt(t *testing.T, ctx context.Context, b *keyedBuilt, who pdid.Id, methods []string, at time.Time) string {
	t.Helper()
	x := require.New(t)

	token, sum, err := keys.Mint(keys.PrefixDelegation)
	x.NoError(err)

	_, err = b.Ungated.Delegation().Add(ctx, app.DelegationAddRequest_builder{
		Holder:      app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Secret:      sum,
		Issuer:      issuerOf(t, ctx, b),
		Methods:     methods,
		DateExpires: timestamppb.New(time.Now().Add(keys.DelegateFor)),
		DateCreated: timestamppb.New(at),
	}.Build())
	x.NoError(err)

	return token
}

// ownKeyAt is an `rt_` of somebody's own, minted at `at`: what a terminal is
// handed by `roster sign-in`.
func ownKeyAt(t *testing.T, ctx context.Context, b *keyedBuilt, who pdid.Id, methods []string, at time.Time) string {
	t.Helper()
	x := require.New(t)

	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	terminals++

	_, err = b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:      app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:       fmt.Sprintf("terminal-%d", terminals),
		Secret:      sum,
		Methods:     methods,
		DateCreated: timestamppb.New(at),
	}.Build())
	x.NoError(err)

	return token
}

// terminals names each key [ownKeyAt] mints, since a holder's keys are named
// apart.
var terminals int

func erasedAt(t *testing.T, ctx context.Context, s *cmd.Server, who pdid.Id) (*time.Time, *time.Time) {
	t.Helper()

	v, err := s.Ent.Holder.Get(ctx, who.Uuid())
	require.NoError(t, err)

	return v.DateErased, v.DateInvalidated
}

// TestAPersonLeavesWhereTheirTenantOffersIt is `MeService.Leave`, over the wire
// the way the account page reaches it: a front door's key, and the person's
// delegation beside it.
//
// What it pins is the order the three refusals come in and what each one is
// about -- the tenant's setting, a sign-in minutes old, the alias typed -- and
// then that what happens is an operator's erase and nothing more: the person is
// gone, every app hears it, nobody else is touched, and an operator can bring
// them back without bringing back how they were signed in.
func TestAPersonLeavesWhereTheirTenantOffersIt(t *testing.T) {
	b := keyedFor(t, func(c *cmd.Config) { c.Holder.ForgetAfter = 720 * time.Hour },
		verify, watchSync, invalidate)
	ctx, stop := context.WithCancel(t.Context())
	defer stop()

	// Served with the configuration it was built from, which `served` does not
	// pass on: the forget window is how `Leave` says when.
	g, err := b.Grpc(ctx, b.Config)
	require.NoError(t, err)
	b.Conn = pdtest.Serve(t, g)

	c := app.NewMeServiceClient(b.Conn)
	may := []string{leaveMe, getMe}
	leave := func(as context.Context, confirm string) (*app.MeLeaveResponse, error) {
		return c.Leave(as, app.MeLeaveRequest_builder{Confirm: confirm}.Build())
	}
	fresh := func() context.Context {
		return acting(ctx, b.Token, delegates(t, ctx, b, b.Who, may, 0))
	}
	bob := addHolder(t, ctx, b.Server, b.Contoso, "bob")

	t.Run("not where the tenant does not offer it", func(t *testing.T) {
		x := require.New(t)

		_, err := leave(fresh(), "someone")
		x.Equal(codes.PermissionDenied, status.Code(err), "a tenant that never said so offered it")
	})

	offersLeave(t, ctx, b.Server, b.Contoso, true)

	t.Run("not from a sign-in that is not minutes old", func(t *testing.T) {
		x := require.New(t)

		stale := signedInAt(t, ctx, b, b.Who, may, time.Now().Add(-2*me.LeaveWithin))
		_, err := leave(acting(ctx, b.Token, stale), "someone")
		x.Equal(codes.FailedPrecondition, status.Code(err), "a session open all afternoon deleted an account")
		x.Contains(status.Convert(err).Message(), "sign in again")
	})

	t.Run("not from a key that has been around", func(t *testing.T) {
		x := require.New(t)

		old := ownKeyAt(t, ctx, b, b.Who, []string{leaveMe}, time.Now().Add(-time.Hour))
		_, err := leave(bearing(ctx, old), "someone")
		x.Equal(codes.FailedPrecondition, status.Code(err))
	})

	t.Run("not by accident", func(t *testing.T) {
		x := require.New(t)

		for _, wrong := range []string{"", "Someone", "bob"} {
			_, err := leave(fresh(), wrong)
			x.Equal(codes.InvalidArgument, status.Code(err), "confirm %q", wrong)
		}
	})

	t.Run("not by a holder a file declares", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:  "front",
			Labels: map[string]string{cmd.Declared: "config: account"},
		}.Build())
		x.NoError(err)
		front := mustId(t, v.GetId())

		k := ownKeyAt(t, ctx, b, front, []string{leaveMe}, time.Now())
		_, err = leave(bearing(ctx, k), "front")
		x.Equal(codes.FailedPrecondition, status.Code(err))

		erased, invalidated := erasedAt(t, ctx, b.Server, front)
		x.Nil(erased)
		x.Nil(invalidated, "refused, and signed out on the way to being refused")
	})

	// Everything refused above wrote nothing.
	erased, invalidated := erasedAt(t, ctx, b.Server, b.Who)
	require.Nil(t, erased)
	require.Nil(t, invalidated)

	var used string
	t.Run("and then they are gone, and every app hears it", func(t *testing.T) {
		x := require.New(t)

		heard := syncing(t, bearing(ctx, b.Token), b.Conn, addHolder(t, ctx, b.Server, b.Contoso, "canary"))

		used = delegates(t, ctx, b, b.Who, may, 0)
		before := time.Now()
		v, err := leave(acting(ctx, b.Token, used), "someone")
		x.NoError(err)
		x.WithinDuration(before, v.GetDateErased().AsTime(), time.Minute)
		x.WithinDuration(v.GetDateErased().AsTime().Add(720*time.Hour), v.GetDateForgotten().AsTime(), time.Second,
			"and told when it will be forgotten")

		erased, invalidated := erasedAt(t, ctx, b.Server, b.Who)
		x.NotNil(erased)
		x.NotNil(invalidated, "signed out everywhere before the erase")

		ev := about(t, heard, b.Who)
		x.Equal(app.SyncReason_SYNC_REASON_ERASED, ev.GetReason())
		x.Equal(b.Contoso.Bytes(), ev.GetTenant())

		_, err = c.Get(acting(ctx, b.Token, used), app.MeGetRequest_builder{}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err), "the sign-in that left went on working")

		gone, _ := erasedAt(t, ctx, b.Server, bob)
		x.Nil(gone, "somebody else left with them")
	})

	t.Run("and an operator can bring them back, and not how they were signed in", func(t *testing.T) {
		x := require.New(t)

		x.NoError(forget.Restore(ctx, b.Ent, b.Who, audit.Contents{}))
		erased, _ := erasedAt(t, ctx, b.Server, b.Who)
		x.Nil(erased)

		_, err := c.Get(acting(ctx, b.Token, used), app.MeGetRequest_builder{}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err), "a restore brought back the sign-in that left")

		_, err = c.Get(fresh(), app.MeGetRequest_builder{}.Build())
		x.NoError(err, "a new sign-in after the restore")
	})
}

// TestATerminalLeavesWithAKeyItWasJustHanded: `roster sign-in` hands a terminal
// a key in the browser, minutes old, and that is a sign-in as much as the
// account page's is.
func TestATerminalLeavesWithAKeyItWasJustHanded(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()
	offersLeave(t, ctx, b.Server, b.Contoso, true)

	kim := addHolder(t, ctx, b.Server, b.Contoso, "kim")
	k := ownKeyAt(t, ctx, b, kim, []string{leaveMe}, time.Now())

	v, err := app.NewMeServiceClient(b.Conn).Leave(bearing(ctx, k), app.MeLeaveRequest_builder{Confirm: "kim"}.Build())
	x.NoError(err)
	x.False(v.HasDateForgotten(), "a deployment that forgets nobody on a clock gave a date")

	erased, _ := erasedAt(t, ctx, b.Server, kim)
	x.NotNil(erased)

	// And somebody else's key, however fresh, is not a sign-in of theirs: it
	// resolves to whoever holds it, and that is who would leave.
	lee := addHolder(t, ctx, b.Server, b.Contoso, "lee")
	theirs := ownKeyAt(t, ctx, b, lee, []string{leaveMe}, time.Now())
	_, err = app.NewMeServiceClient(b.Conn).Leave(bearing(ctx, theirs), app.MeLeaveRequest_builder{Confirm: "someone"}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "a key answered as somebody it does not belong to")
	gone, _ := erasedAt(t, ctx, b.Server, b.Who)
	x.Nil(gone)
}

// TestSomebodyTheirDirectoryProvisionsDoesNotLeaveOnTheirOwn: a tenant may
// offer leaving and have a directory provision its people as well. The people
// the directory provisions -- anybody who signs in through its connection -- are
// its to remove: one who erased themselves would be somebody it can neither find
// nor make again until they are forgotten, and then made again. So they are
// refused before anything is written -- and `MeService.Get` says so, for a page
// to offer them nothing -- and anybody else leaves as before.
func TestSomebodyTheirDirectoryProvisionsDoesNotLeaveOnTheirOwn(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()
	offersLeave(t, ctx, b.Server, b.Contoso, true)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	c := app.NewMeServiceClient(b.Conn)

	for _, v := range []*app.ConnectionAddRequest{
		app.ConnectionAddRequest_builder{
			Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/contoso/v2.0", ClientId: "the-app",
			SubjectClaim: z.Ptr("oid"), Provisions: z.Ptr(true),
		}.Build(),
		app.ConnectionAddRequest_builder{Tenant: at, Name: "okta", Issuer: "https://contoso.okta.example", ClientId: "the-app"}.Build(),
	} {
		_, err := b.Ungated.Connection().Add(ctx, v)
		x.NoError(err)
	}
	linked := func(alias, provider, subject string) pdid.Id {
		t.Helper()
		who := addHolder(t, ctx, b.Server, b.Contoso, alias)
		_, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
			Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(), Provider: provider, Subject: subject,
		}.Build())
		x.NoError(err)

		return who
	}
	leave := func(who pdid.Id, alias string) error {
		k := ownKeyAt(t, ctx, b, who, []string{leaveMe}, time.Now())
		_, err := c.Leave(bearing(ctx, k), app.MeLeaveRequest_builder{Confirm: alias}.Build())

		return err
	}
	// What a page is told, so that it offers leaving to nobody it would refuse.
	provisioned := func(who pdid.Id) string {
		k := ownKeyAt(t, ctx, b, who, []string{getMe}, time.Now())
		v, err := c.Get(bearing(ctx, k), app.MeGetRequest_builder{}.Build())
		x.NoError(err)

		return v.GetProvisionedThrough()
	}

	mia := linked("mia", "entra", erinOid)
	x.Equal("entra", provisioned(mia))
	err := leave(mia, "mia")
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	x.Contains(status.Convert(err).Message(), "directory")
	erased, invalidated := erasedAt(t, ctx, b.Server, mia)
	x.Nil(erased)
	x.Nil(invalidated, "refused, and signed out on the way to being refused")

	ned := linked("ned", "okta", "00u1abcd")
	x.Empty(provisioned(ned))
	x.NoError(leave(ned, "ned"), "somebody who signs in another way is not the directory's")
	erased, _ = erasedAt(t, ctx, b.Server, ned)
	x.NotNil(erased)

	// And once the tenant's directory provisions through nothing, its people
	// are anybody else.
	_, err = b.Ungated.Connection().Erase(ctx, app.ConnectionRef_builder{At: app.ConnectionRefByAt_builder{
		Tenant: at, Name: proto.String("entra"),
	}.Build()}.Build())
	x.NoError(err)
	x.Empty(provisioned(mia))
	x.NoError(leave(mia, "mia"))
}

// TestLeavingMeetsTheTenantsOwnLifecycle: a tenant can be closed or held
// (#105). Somebody in a closed tenant reaches nothing, this included; somebody
// in a held one may still leave -- an erase destroys nothing -- and is not told
// a date the hold is there to break.
func TestLeavingMeetsTheTenantsOwnLifecycle(t *testing.T) {
	x := require.New(t)
	b := keyedFor(t, func(c *cmd.Config) { c.Holder.ForgetAfter = 720 * time.Hour }, verify)
	ctx := t.Context()
	g, err := b.Grpc(ctx, b.Config)
	x.NoError(err)
	c := app.NewMeServiceClient(pdtest.Serve(t, g))
	offersLeave(t, ctx, b.Server, b.Contoso, true)

	pat := addHolder(t, ctx, b.Server, b.Contoso, "pat")
	quin := addHolder(t, ctx, b.Server, b.Contoso, "quin")
	pats := ownKeyAt(t, ctx, b, pat, []string{leaveMe}, time.Now())
	quins := ownKeyAt(t, ctx, b, quin, []string{leaveMe}, time.Now())

	x.NoError(forget.Hold(ctx, b.Ungated, b.Contoso, "a case"))
	v, err := c.Leave(bearing(ctx, pats), app.MeLeaveRequest_builder{Confirm: "pat"}.Build())
	x.NoError(err, "a hold kept somebody from leaving")
	x.False(v.HasDateForgotten(), "promised a date a hold is there to break")
	erased, _ := erasedAt(t, ctx, b.Server, pat)
	x.NotNil(erased)

	_, err = b.Ungated.Tenant().Close(ctx, app.TenantCloseRequest_builder{
		Ref: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = c.Leave(bearing(ctx, quins), app.MeLeaveRequest_builder{Confirm: "quin"}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err), "somebody in a closed tenant reached roster")
	erased, _ = erasedAt(t, ctx, b.Server, quin)
	x.Nil(erased)
}
