package cmd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/z"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
)

// badgeEnrolled gives somebody a badge at the box, where nobody is refused.
func (b *built) badgeEnrolled(t *testing.T, ctx context.Context, who pdid.Id, code, pin string) *app.Badge {
	t.Helper()

	v, err := b.Ungated.Badge().Enrol(ctx, app.BadgeEnrolRequest_builder{
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Code:   code,
		Pin:    pin,
	}.Build())
	require.NoError(t, err)

	return v
}

// badgeStation is a station's holder, bound a role naming `Verify` across the
// tenant, or in `site` where one is given.
func (b *built) badgeStation(t *testing.T, ctx context.Context, in pdid.Id, alias string, site *pdid.Id) pdid.Id {
	t.Helper()

	who := b.holder(t, ctx, in, alias)
	req := app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:   "station-" + alias,
		Methods: []string{app.BadgeService_Verify_FullMethodName},
	}
	if site != nil {
		req.Site = app.SiteRef_builder{Id: site.Bytes()}.Build()
	}
	v, err := b.Ungated.Role().Add(ctx, req.Build())
	require.NoError(t, err)
	b.binds(t, who, mustId(t, v.GetId()), site)

	return who
}

// badgeAsked is a station asking about a badge, in process, through the
// walled stack as a call that arrived at a port reaches it.
func (b *built) badgeAsked(ctx context.Context, station, tenant pdid.Id, code, pin string) (*app.BadgeVerifyResponse, error) {
	at := cmd.Calling(b.asNobody(ctx, station, tenant), app.BadgeService_Verify_FullMethodName)

	return b.Walled.Badge().Verify(at, app.BadgeVerifyRequest_builder{Code: code, Pin: pin}.Build())
}

// TestABadgeAndItsPinOpenAStation, over the wire: an administrator enrols a
// badge, and a station's holder that holds `BadgeService/Verify` is answered
// who it is for the right PIN and nothing for a wrong one.
func TestABadgeAndItsPinOpenAStation(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	b.mayAnything(b.ContosoUser, b.Contoso)
	park := b.holder(t, ctx, b.Contoso, "park")
	station := b.badgeStation(t, ctx, b.Contoso, "line-3", nil)

	badges := app.NewBadgeServiceClient(served(t, b.Server))

	v, err := badges.Enrol(asOverTheWire(ctx, b.ContosoUser), app.BadgeEnrolRequest_builder{
		Holder: app.HolderRef_builder{Id: park.Bytes()}.Build(),
		Code:   "0042-7781",
		Pin:    "2580",
	}.Build())
	x.NoError(err)
	x.Equal("0042-7781", v.GetCode())
	x.Equal(park.Bytes(), v.GetHolder().GetId())
	x.Empty(v.GetSecret(), "the answer carried the verifier")
	x.NotNil(v.GetDateRotated())

	at := asOverTheWire(ctx, station)
	got, err := badges.Verify(at, app.BadgeVerifyRequest_builder{Code: "0042-7781", Pin: "2580"}.Build())
	x.NoError(err)
	x.True(got.GetOk())
	x.Equal(park.Bytes(), got.GetHolder())
	x.Equal(b.Contoso.Bytes(), got.GetTenant())
	x.False(got.GetLocked())

	got, err = badges.Verify(at, app.BadgeVerifyRequest_builder{Code: "0042-7781", Pin: "2581"}.Build())
	x.NoError(err)
	x.False(got.GetOk())
	x.Empty(got.GetHolder(), "a wrong PIN said whose the badge is")
	x.Empty(got.GetTenant())
	x.False(got.GetLocked())

	t.Run("and the badge is read without its verifier", func(t *testing.T) {
		x := require.New(t)

		v, err := badges.Get(asOverTheWire(ctx, b.ContosoUser), app.BadgeGetRequest_builder{
			Ref:    app.BadgeRef_builder{Id: v.GetId()}.Build(),
			Select: app.BadgeSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("0042-7781", v.GetCode())
		x.Empty(v.GetSecret())
		x.EqualValues(1, v.GetFailures())
	})

	t.Run("and nobody makes one by Add or writes one by Patch", func(t *testing.T) {
		x := require.New(t)

		_, err := badges.Add(asOverTheWire(ctx, b.ContosoUser), app.BadgeAddRequest_builder{
			Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Code:   "chosen",
			Secret: []byte("a verifier somebody chose"),
		}.Build())
		x.Equal(codes.Unimplemented, status.Code(err), "%v", err)

		_, err = badges.Patch(asOverTheWire(ctx, b.ContosoUser), app.BadgePatchRequest_builder{
			Ref:      app.BadgeRef_builder{Id: v.GetId()}.Build(),
			Failures: z.Ptr(int32(0)),
		}.Build())
		x.Equal(codes.Unimplemented, status.Code(err), "%v", err)
	})
}

// TestTheFifthWrongPinLocksABadgeUntilSomebodyUnlocksIt: five unless the
// deployment says, and nothing lifts it but `Unlock` unless the deployment
// gives it a time. A locked badge is not compared, so the right PIN is
// `locked` too, and not counted.
func TestTheFifthWrongPinLocksABadgeUntilSomebodyUnlocksIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	b.mayAnything(b.ContosoUser, b.Contoso)
	park := b.holder(t, ctx, b.Contoso, "park")
	station := b.badgeStation(t, ctx, b.Contoso, "line-3", nil)
	b.badgeEnrolled(t, ctx, park, "0042", "2580")

	badges := app.NewBadgeServiceClient(served(t, b.Server))
	at := asOverTheWire(ctx, station)
	ask := func(pin string) *app.BadgeVerifyResponse {
		t.Helper()

		v, err := badges.Verify(at, app.BadgeVerifyRequest_builder{Code: "0042", Pin: pin}.Build())
		require.NoError(t, err)

		return v
	}

	for i := range 4 {
		v := ask("1111")
		x.False(v.GetOk())
		x.False(v.GetLocked(), "locked after %d wrong PINs", i+1)
	}
	v := ask("1111")
	x.False(v.GetOk())
	x.True(v.GetLocked(), "five wrong PINs did not lock it")

	v = ask("2580")
	x.False(v.GetOk(), "the right PIN opened a locked badge")
	x.True(v.GetLocked())

	row, err := b.Ent.Badge.Query().Only(ctx)
	x.NoError(err)
	x.EqualValues(5, row.Failures, "an attempt at a locked badge was counted")
	x.NotNil(row.DateLocked)

	u, err := badges.Unlock(asOverTheWire(ctx, b.ContosoUser), app.BadgeUnlockRequest_builder{
		Holder: app.HolderRef_builder{Id: park.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.NotNil(u.GetDateLocked(), "unlocking a locked badge said it was not locked")

	v = ask("2580")
	x.True(v.GetOk(), "the right PIN after an unlock was refused")

	u, err = badges.Unlock(asOverTheWire(ctx, b.ContosoUser), app.BadgeUnlockRequest_builder{
		Holder: app.HolderRef_builder{Id: park.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.Nil(u.GetDateLocked(), "unlocking an open badge said it had been locked")
}

// TestABadgeLockGivenATimeLiftsByItself, and the count starts again from none.
// The clock is the row's: the lock is moved back past `lockout.for` rather
// than waited out.
func TestABadgeLockGivenATimeLiftsByItself(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, func(c *cmd.Config) {
		c.Vouch.Badge.Lockout = cmd.BadgeLockoutConfig{Failures: 2, For: time.Hour}
	})
	park := b.holder(t, ctx, b.Contoso, "park")
	station := b.badgeStation(t, ctx, b.Contoso, "line-3", nil)
	v := b.badgeEnrolled(t, ctx, park, "0042", "2580")

	ask := func(pin string) *app.BadgeVerifyResponse {
		t.Helper()

		got, err := b.badgeAsked(ctx, station, b.Contoso, "0042", pin)
		require.NoError(t, err)

		return got
	}

	ask("1111")
	x.True(ask("1111").GetLocked(), "two wrong PINs did not lock it, where the deployment said two")
	x.True(ask("2580").GetLocked(), "the lock did not hold for its hour")

	// An hour and a minute ago.
	row, err := b.Ent.Badge.Get(ctx, mustId(t, v.GetId()).Uuid())
	x.NoError(err)
	_, err = b.Ungated.Badge().Patch(ctx, app.BadgePatchRequest_builder{
		Ref:              app.BadgeRef_builder{Id: v.GetId()}.Build(),
		DateLocked:       timestamppb.New(row.DateLocked.Add(-time.Hour - time.Minute)),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	got := ask("1111")
	x.False(got.GetOk())
	x.False(got.GetLocked(), "a wrong PIN after the lift locked it again at once: the count did not start again")

	row, err = b.Ent.Badge.Get(ctx, row.Id)
	x.NoError(err)
	x.EqualValues(1, row.Failures)
	x.Nil(row.DateLocked)

	x.True(ask("2580").GetOk(), "the right PIN after the lift was refused")
}

// TestEveryBadgeThatOpensNothingIsOneAnswerAtOneCost: a code nobody has, a
// wrong PIN, somebody suspended, erased or of a closed tenant, and somebody
// the station's grant does not reach are each `ok: false` and nothing else,
// and none of them answers sooner than a code nobody has -- which is a hash
// and an unseal. Told apart, a station's port would answer who is somebody,
// who is suspended and who is of which site.
func TestEveryBadgeThatOpensNothingIsOneAnswerAtOneCost(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, func(c *cmd.Config) {
		// Enough that the wrong PINs below lock nothing.
		c.Vouch.Badge.Lockout.Failures = 1000
	})

	seoul, busan := b.site(t, ctx, b.Contoso, "seoul"), b.site(t, ctx, b.Contoso, "busan")
	station := b.badgeStation(t, ctx, b.Contoso, "seoul-line", &seoul)

	in := func(alias string, site pdid.Id, code string) pdid.Id {
		who := b.holder(t, ctx, b.Contoso, alias)
		b.joins(t, ctx, who, site)
		b.badgeEnrolled(t, ctx, who, code, "2580")

		return who
	}
	in("park", seoul, "park")
	stopped := in("kim", seoul, "kim")
	gone := in("choi", seoul, "choi")
	in("lee", busan, "lee")

	_, err := b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: app.HolderRef_builder{Id: stopped.Bytes()}.Build()}.Build())
	x.NoError(err)
	_, err = b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: gone.Bytes()}.Build())
	x.NoError(err)

	// And a station of a tenant that closed, asking about one of its own.
	theirs := b.badgeStation(t, ctx, b.Fabrikam, "line-1", nil)
	b.badgeEnrolled(t, ctx, b.holder(t, ctx, b.Fabrikam, "ahn"), "ahn", "2580")
	_, err = b.Ungated.Tenant().Close(ctx, app.TenantCloseRequest_builder{Ref: app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build()}.Build())
	x.NoError(err)

	// The quickest of a few, so that what is compared is the work and not
	// whatever else the machine was doing.
	ask := func(station, tenant pdid.Id, code, pin string) (*app.BadgeVerifyResponse, time.Duration) {
		t.Helper()

		var (
			res  *app.BadgeVerifyResponse
			best time.Duration
		)
		for range 5 {
			began := time.Now()
			got, err := b.badgeAsked(ctx, station, tenant, code, pin)
			took := time.Since(began)
			require.NoError(t, err)

			if res == nil || took < best {
				res, best = got, took
			}
		}

		return res, best
	}

	_, nobody := ask(station, b.Contoso, "nobody-has-this", "2580")
	for name, v := range map[string]struct {
		station, tenant pdid.Id
		code, pin       string
	}{
		"a code nobody has":           {station, b.Contoso, "nor-this", "2580"},
		"a wrong PIN":                 {station, b.Contoso, "park", "1111"},
		"somebody suspended":          {station, b.Contoso, "kim", "2580"},
		"somebody erased":             {station, b.Contoso, "choi", "2580"},
		"somebody of a closed tenant": {theirs, b.Fabrikam, "ahn", "2580"},
		"somebody of another site":    {station, b.Contoso, "lee", "2580"},
	} {
		got, took := ask(v.station, v.tenant, v.code, v.pin)
		x.False(got.GetOk(), name)
		x.False(got.GetLocked(), name)
		x.Empty(got.GetHolder(), name)
		x.Empty(got.GetTenant(), name)

		// Not *equal*, which no clock would hold to: a third is far inside
		// what the same work on the same machine varies by, and an answer
		// that skipped the hash is many times sooner.
		x.Greater(took, nobody/3, "%s answered in %s where a code nobody has took %s", name, took, nobody)
	}

	got, _ := ask(station, b.Contoso, "park", "2580")
	x.True(got.GetOk(), "and the one badge here that opens anything did not, so this measured nothing")
}

// TestAStationBoundInASiteChecksTheBadgesOfThatSitesPeople and no others: the
// badge of somebody outside it is answered as one nobody has, and the wall is
// what says so.
func TestAStationBoundInASiteChecksTheBadgesOfThatSitesPeople(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seoul, busan := b.site(t, ctx, b.Contoso, "seoul"), b.site(t, ctx, b.Contoso, "busan")
	park, lee := b.holder(t, ctx, b.Contoso, "park"), b.holder(t, ctx, b.Contoso, "lee")
	b.joins(t, ctx, park, seoul)
	b.joins(t, ctx, lee, busan)
	b.badgeEnrolled(t, ctx, park, "park", "2580")
	b.badgeEnrolled(t, ctx, lee, "lee", "2580")
	station := b.badgeStation(t, ctx, b.Contoso, "seoul-line", &seoul)

	badges := app.NewBadgeServiceClient(served(t, b.Server))
	at := asOverTheWire(ctx, station)

	v, err := badges.Verify(at, app.BadgeVerifyRequest_builder{Code: "park", Pin: "2580"}.Build())
	x.NoError(err)
	x.True(v.GetOk(), "a station in Seoul did not open for somebody of Seoul")

	v, err = badges.Verify(at, app.BadgeVerifyRequest_builder{Code: "lee", Pin: "2580"}.Build())
	x.NoError(err)
	x.False(v.GetOk(), "a station in Seoul opened for somebody of Busan")
	x.Empty(v.GetHolder())

	// And a wrong PIN at a badge it does not reach counts nothing there.
	v, err = badges.Verify(at, app.BadgeVerifyRequest_builder{Code: "lee", Pin: "1111"}.Build())
	x.NoError(err)
	x.False(v.GetOk())
	row, err := b.Ent.Badge.Query().All(ctx)
	x.NoError(err)
	for _, r := range row {
		x.Zero(r.Failures, "a station counted a wrong PIN at a badge it cannot see")
	}
}

// TestNobodyEnrolsABadgeForSomebodyWhoHoldsMoreThanThey: a badge is a way in
// at every station, and the rule every way in is held to holds.
func TestNobodyEnrolsABadgeForSomebodyWhoHoldsMoreThanThey(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	boss := b.holder(t, ctx, b.Contoso, "boss")
	b.mayAnything(boss, b.Contoso)
	park := b.holder(t, ctx, b.Contoso, "park")

	clerk := b.holder(t, ctx, b.Contoso, "clerk")
	v, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "badges",
		Methods: []string{
			app.BadgeService_Enrol_FullMethodName,
			app.BadgeService_Unlock_FullMethodName,
			app.BadgeService_Erase_FullMethodName,
		},
	}.Build())
	x.NoError(err)
	b.binds(t, clerk, mustId(t, v.GetId()), nil)
	b.badgeEnrolled(t, ctx, boss, "boss", "2580")

	badges := app.NewBadgeServiceClient(served(t, b.Server))
	at := asOverTheWire(ctx, clerk)

	_, err = badges.Enrol(at, app.BadgeEnrolRequest_builder{
		Holder: app.HolderRef_builder{Id: boss.Bytes()}.Build(), Code: "mine-now", Pin: "1234",
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a clerk wrote a badge for somebody who holds everything: %v", err)

	_, err = badges.Unlock(at, app.BadgeUnlockRequest_builder{Holder: app.HolderRef_builder{Id: boss.Bytes()}.Build()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

	_, err = badges.Erase(at, app.BadgeRef_builder{
		Holder: app.BadgeRefByHolder_builder{Holder: app.HolderRef_builder{Id: boss.Bytes()}.Build()}.Build(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

	row, err := b.Ent.Badge.Query().Only(ctx)
	x.NoError(err)
	x.Equal("boss", row.Code, "the badge changed")

	// And somebody who holds no more is theirs to give one to.
	_, err = badges.Enrol(at, app.BadgeEnrolRequest_builder{
		Holder: app.HolderRef_builder{Id: park.Bytes()}.Build(), Code: "park", Pin: "1234",
	}.Build())
	x.NoError(err)
}

// TestACodeNamesOnePerson: a code another person of the tenant has is
// refused, naming the field and nobody; the same code in another tenant is
// another operator's card; and enrolling somebody again replaces all of theirs.
func TestACodeNamesOnePerson(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	park, lee := b.holder(t, ctx, b.Contoso, "park"), b.holder(t, ctx, b.Contoso, "lee")
	b.badgeEnrolled(t, ctx, park, "0042", "2580")

	_, err := b.Ungated.Badge().Enrol(ctx, app.BadgeEnrolRequest_builder{
		Holder: app.HolderRef_builder{Id: lee.Bytes()}.Build(), Code: "0042", Pin: "1234",
	}.Build())
	x.Equal(codes.AlreadyExists, status.Code(err), "%v", err)
	x.Contains(status.Convert(err).Message(), "code:")
	x.NotContains(status.Convert(err).Message(), "park")
	x.NotContains(status.Convert(err).Message(), park.String())

	// Compared as written: another spelling is another card.
	b.badgeEnrolled(t, ctx, lee, "42", "1234")

	// Another tenant's.
	b.badgeEnrolled(t, ctx, b.holder(t, ctx, b.Fabrikam, "ahn"), "0042", "1234")

	// And park's again, whole: a new code and a new PIN, and the old ones
	// open nothing.
	b.badgeEnrolled(t, ctx, park, "0043", "9999")
	n, err := b.Ent.Badge.Query().Count(ctx)
	x.NoError(err)
	x.Equal(3, n, "a second enrolment made a second badge")

	station := b.badgeStation(t, ctx, b.Contoso, "line-3", nil)
	got, err := b.badgeAsked(ctx, station, b.Contoso, "0042", "2580")
	x.NoError(err)
	x.False(got.GetOk(), "the code park was given before opened a station")
	got, err = b.badgeAsked(ctx, station, b.Contoso, "0043", "9999")
	x.NoError(err)
	x.True(got.GetOk())
	x.Equal(park.Bytes(), got.GetHolder())
}

// TestAPinIsDecimalDigitsOfTheDeploymentsLength, refused naming the rule.
func TestAPinIsDecimalDigitsOfTheDeploymentsLength(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	enrol := func(code, pin string) error {
		_, err := b.Ungated.Badge().Enrol(ctx, app.BadgeEnrolRequest_builder{
			Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(), Code: code, Pin: pin,
		}.Build())

		return err
	}

	for pin, says := range map[string]string{
		"abcd":          "decimal digits",
		"12a4":          "decimal digits",
		"123":           "at least 4 digits",
		"1234567890123": "at most 12 digits",
		"":              "at least 4 digits",
		"１２３４":          "decimal digits",
	} {
		err := enrol("0042", pin)
		x.Equal(codes.InvalidArgument, status.Code(err), "%q: %v", pin, err)
		x.Contains(err.Error(), says, "%q", pin)
		x.Contains(err.Error(), "pin", "%q", pin)
	}
	x.NoError(enrol("0042", "1234"))
	x.NoError(enrol("0042", "123456789012"))

	for code, says := range map[string]string{
		"":                       "must not be empty",
		strings.Repeat("9", 129): "at most 128 bytes",
		"00\n42":                 "control character",
	} {
		err := enrol(code, "1234")
		x.Equal(codes.InvalidArgument, status.Code(err), "%q: %v", code, err)
		x.Contains(err.Error(), says, "%q", code)
	}

	t.Run("and the deployment may ask for more digits", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t, func(c *cmd.Config) { c.Vouch.Badge.Pin.MinLength = 6 })

		_, err := b.Ungated.Badge().Enrol(ctx, app.BadgeEnrolRequest_builder{
			Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(), Code: "0042", Pin: "12345",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
		x.Contains(err.Error(), "at least 6 digits")
	})

	t.Run("and never fewer than four or more than twelve", func(t *testing.T) {
		x := require.New(t)

		for _, n := range []int{1, 3, 13} {
			c := cmd.Config{Vouch: cmd.VouchConfig{Badge: cmd.BadgeConfig{Pin: cmd.BadgePinConfig{MinLength: n}}}}
			err := c.Meant()
			x.Error(err, "%d", n)
			x.Contains(err.Error(), "vouch.badge.pin.min_length")
		}
		for _, n := range []int{0, 4, 12} {
			c := cmd.Config{Vouch: cmd.VouchConfig{Badge: cmd.BadgeConfig{Pin: cmd.BadgePinConfig{MinLength: n}}}}
			x.NoError(c.Meant(), "%d", n)
		}

		c := cmd.Config{Vouch: cmd.VouchConfig{Badge: cmd.BadgeConfig{Lockout: cmd.BadgeLockoutConfig{Failures: -1}}}}
		err := c.Meant()
		x.Error(err)
		x.Contains(err.Error(), "vouch.badge.lockout.failures")
	})
}

// TestADeploymentWithNoKeyringHoldsNoBadge: the verifier is sealed under
// `vouch.keys`, so without one there is nothing to seal it with or read it
// by, and each verb says so rather than storing a PIN's hash in the clear.
func TestADeploymentWithNoKeyringHoldsNoBadge(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, withoutKeyring)
	station := b.badgeStation(t, ctx, b.Contoso, "line-3", nil)

	_, err := b.Ungated.Badge().Enrol(ctx, app.BadgeEnrolRequest_builder{
		Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(), Code: "0042", Pin: "2580",
	}.Build())
	x.Equal(codes.Unimplemented, status.Code(err), "%v", err)
	x.Contains(err.Error(), "holds no key")

	_, err = b.badgeAsked(ctx, station, b.Contoso, "0042", "2580")
	x.Equal(codes.Unimplemented, status.Code(err), "%v", err)
	x.Contains(err.Error(), "holds no key")

	n, err := b.Ent.Badge.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n)
}

// TestABadgeIsNoWayToSignIn: a person with a badge and a password signs in by
// the password and is offered no badge as a factor, and the rule that keeps
// somebody's last way in does not count one.
func TestABadgeIsNoWayToSignIn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sets(t, ctx, b.ContosoUser, "correct horse battery staple")
	b.badgeEnrolled(t, ctx, b.ContosoUser, "0042", "2580")

	v := b.verifies(t, ctx, b.ContosoUser, "correct horse battery staple")
	x.True(v.GetOk(), "a password beside a badge was not a sign-in")
	x.Empty(v.GetAvailable(), "a badge was offered as a second factor")
	x.Empty(v.GetContinuation())

	// Somebody whose ways in are one account at a provider and a badge: the
	// account is their last way in, and the badge is not another.
	park := b.holder(t, ctx, b.Contoso, "park")
	i := b.identity(t, ctx, park, "github", "gh-park")
	b.badgeEnrolled(t, ctx, park, "0043", "2580")

	_, err := b.Ungated.Identity().Erase(ctx, app.IdentityRef_builder{Id: i.GetId()}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "a badge was counted as a way in: %v", err)
}

// TestAForgottenPersonHasNoBadge: what the badge says is personal, and
// somebody forgotten opens nothing anywhere.
func TestAForgottenPersonHasNoBadge(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	park := b.holder(t, ctx, b.Contoso, "park")
	b.badgeEnrolled(t, ctx, park, "0042", "2580")
	b.badgeEnrolled(t, ctx, b.ContosoUser, "0043", "2580")

	_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: park.Bytes()}.Build())
	x.NoError(err)
	_, err = forget.Forget(ctx, b.Ent, park, trail.Policy{}, audit.Contents{}, nil)
	x.NoError(err)

	vs, err := b.Ent.Badge.Query().All(ctx)
	x.NoError(err)
	x.Len(vs, 1, "the forgotten person's badge is still there, or somebody else's went with it")
	x.Equal("0043", vs[0].Code)
}
