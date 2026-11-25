package cmd_test

import (
	"encoding/base64"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
	entaccess "github.com/lesomnus/roster/internal/ent/access"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/pd"
)

// TestTheTrailOfABadgeKeepsNoPinAndNoVerifier: an enrolment, a wrong PIN
// counted, a lock, an unlock and an erase are each on the trail as any write
// is, and none of them carries the PIN, the verifier, or what the badge says
// -- even where the deployment keeps the row each write left.
func TestTheTrailOfABadgeKeepsNoPinAndNoVerifier(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, keepingRows, func(c *cmd.Config) { c.Vouch.Badge.Lockout.Failures = 1 })
	park := b.holder(t, ctx, b.Contoso, "park")
	station := b.badgeStation(t, ctx, b.Contoso, "line-3", nil)

	v := b.badgeEnrolled(t, ctx, park, "card-0042-7781", "258036")
	row, err := b.Ent.Badge.Get(ctx, mustId(t, v.GetId()).Uuid())
	x.NoError(err)
	sealed := row.Secret
	x.NotEmpty(sealed)

	got, err := b.badgeAsked(ctx, station, b.Contoso, "card-0042-7781", "111111")
	x.NoError(err)
	x.True(got.GetLocked())
	_, err = b.Ungated.Badge().Unlock(ctx, app.BadgeUnlockRequest_builder{Holder: app.HolderRef_builder{Id: park.Bytes()}.Build()}.Build())
	x.NoError(err)
	_, err = b.Ungated.Badge().Erase(ctx, app.BadgeRef_builder{Id: v.GetId()}.Build())
	x.NoError(err)

	vs, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	writes := 0
	for _, u := range vs {
		if u.Domain != uint32(pd.BadgeDomain) {
			continue
		}
		writes++
		for _, c := range [][]byte{u.Patch, u.Value} {
			x.NotContains(string(c), "258036", "the trail holds the PIN")
			x.NotContains(string(c), "111111", "the trail holds a wrong PIN")
			x.NotContains(string(c), string(sealed), "the trail holds the verifier")
			x.NotContains(string(c), base64.StdEncoding.EncodeToString(sealed), "the trail holds the verifier")
			x.NotContains(string(c), "card-0042-7781", "the trail holds what the badge says")
		}
	}
	// The enrolment, the count that locked it, the unlock and the erase.
	x.GreaterOrEqual(writes, 4, "the badge's writes are not on the trail")
}

// TestAVerificationThatAnsweredIsOnTheAccessRecord: a station told who a
// badge is has read who somebody is, and the record files it under their
// tenant, naming the station and the badge. One that answered nobody is no
// read of anybody, and a code nobody has leaves nothing at all.
func TestAVerificationThatAnsweredIsOnTheAccessRecord(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	park := b.holder(t, ctx, b.Contoso, "park")
	station := b.badgeStation(t, ctx, b.Contoso, "line-3", nil)
	v := b.badgeEnrolled(t, ctx, park, "0042", "2580")

	badges := app.NewBadgeServiceClient(served(t, b.Server))
	at := asOverTheWire(ctx, station)
	ask := func(code, pin string) {
		t.Helper()

		_, err := badges.Verify(at, app.BadgeVerifyRequest_builder{Code: code, Pin: pin}.Build())
		require.NoError(t, err)
		require.NoError(t, b.Access.Flush(ctx))
	}
	reads := func() []string {
		t.Helper()

		rs, err := b.Ent.Access.Query().Where(
			entaccess.ActionEQ(app.BadgeService_Verify_FullMethodName),
		).All(ctx)
		require.NoError(t, err)

		var out []string
		for _, r := range rs {
			out = append(out, r.Kind)
		}

		return out
	}

	ask("nobody-has-this", "2580")
	x.Empty(reads(), "a code nobody has left a row")

	ask("0042", "2580")
	rs, err := b.Ent.Access.Query().Where(
		entaccess.ActionEQ(app.BadgeService_Verify_FullMethodName),
		entaccess.KindEQ("read"),
	).All(ctx)
	x.NoError(err)
	x.Len(rs, 1, "a verification that answered somebody is not on the record")
	x.Equal(b.Contoso.Uuid(), rs[0].TenantId)
	x.Equal(station.Uuid(), rs[0].ActorId)
	x.Equal([][]byte{v.GetId()}, rs[0].Subjects)

	// A wrong PIN read nobody. Its count is a write of the badge's row, which
	// payday's recorder files as a write of whose the row is, as it files any
	// write of a row that holds a field the schema calls somebody's.
	ask("0042", "1111")
	n := 0
	for _, k := range reads() {
		if k == "read" {
			n++
		}
	}
	x.Equal(1, n, "a wrong PIN was a read of somebody")
}

// TestSignsInSaysABadgeBesideTheRest: in a field of its own and not among the
// credentials -- that there is one, when its PIN was set, whether it is
// locked -- and never what it says or its verifier.
func TestSignsInSaysABadgeBesideTheRest(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, func(c *cmd.Config) { c.Vouch.Badge.Lockout.Failures = 1 })
	b.mayAnything(b.ContosoUser, b.Contoso)
	park := b.holder(t, ctx, b.Contoso, "park")

	signsIn := func() *app.HolderSignsInResponse {
		t.Helper()

		v, err := b.Walled.Holder().SignsIn(b.as(ctx, b.ContosoUser, b.Contoso), app.HolderSignsInRequest_builder{
			Ref: app.HolderRef_builder{Id: park.Bytes()}.Build(),
		}.Build())
		require.NoError(t, err)

		return v
	}

	x.Nil(signsIn().GetBadge(), "somebody with no badge was said to have one")

	b.badgeEnrolled(t, ctx, park, "0042", "2580")
	v := signsIn()
	x.NotNil(v.GetBadge())
	x.NotNil(v.GetBadge().GetDateRotated())
	x.Nil(v.GetBadge().GetDateLocked())
	x.Empty(v.GetCredentials(), "a badge was said among the credentials")

	station := b.badgeStation(t, ctx, b.Contoso, "line-3", nil)
	_, err := b.badgeAsked(ctx, station, b.Contoso, "0042", "1111")
	x.NoError(err)
	x.NotNil(signsIn().GetBadge().GetDateLocked(), "a locked badge was not said to be")
}

// TestNothingOfABadgeIsSomebodysOwnToDo: no verb of `BadgeService` is in the
// tenant's `self-service` role, so a person sets no PIN of their own and
// checks no badge, and a role that names one there is refused.
func TestNothingOfABadgeIsSomebodysOwnToDo(t *testing.T) {
	x := require.New(t)

	x.False(slices.ContainsFunc(core.Owning, func(m string) bool {
		return slices.Contains([]string{
			app.BadgeService_Enrol_FullMethodName,
			app.BadgeService_Unlock_FullMethodName,
			app.BadgeService_Verify_FullMethodName,
			app.BadgeService_Get_FullMethodName,
			app.BadgeService_List_FullMethodName,
			app.BadgeService_Erase_FullMethodName,
		}, m)
	}))
	x.Empty(core.Owned([]string{"/roster.BadgeService/*"}))
}
