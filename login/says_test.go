package login_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// saidOf is what a token says of `who`, signed in afresh at their tenant's
// client and asking the scopes given: the claims the consent hop hands Hydra.
func (d *deployment) saidOf(t *testing.T, challenge, tenant, who string, scope ...string) map[string]any {
	t.Helper()
	x := require.New(t)
	b := d.browser(t)

	d.hydra.raiseWith(challenge, tenant+"-web", scope...)
	_, code := d.signIn(t, b, challenge, who, password)
	x.Equal(http.StatusOK, code)

	res, err := b.Get(d.app.URL + "/consent?consent_challenge=" + challenge)
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusSeeOther, res.StatusCode)

	_, claims := d.hydra.told()

	return claims
}

// called gives somebody a display name the way a console does, over the wire:
// `HolderService/Update` with a profile.
func (d *deployment) called(t *testing.T, who pdid.Id, name string) {
	t.Helper()
	x := require.New(t)

	holders := rstr.NewHolderServiceClient(d.conn)
	ref := rstr.HolderRef_builder{Id: who.Bytes()}.Build()

	was, err := holders.Get(d.ops, rstr.HolderGetRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	_, err = holders.Update(d.ops, rstr.HolderUpdateRequest_builder{
		Ref:         ref,
		Profile:     rstr.Profile_builder{DisplayName: name}.Build(),
		DateUpdated: was.GetDateUpdated(),
	}.Build())
	x.NoError(err)
}

// TestATokenNamesSomebodyAsTheyAreCalledNow: the `name` a product is handed is
// the person's name as it stands.
//
// A holder carries three things that look like one (`holder_svc.ext.proto`):
// the alias; `name`, which is set when the row is added and which nothing may
// change; and the profile's display name, which is what `Update` writes, what a
// tenant's profile setting fills and what a directory sets. `Me.Get` answered
// the second, so everything that renames somebody wrote where no token read:
// after an `Update` to "Erin Kim" the claim was still "Erin of contoso", and
// somebody added with no name had no `name` at all however their profile was
// filled afterwards. It is the display name where there is one, and what they
// were added as where there is none.
func TestATokenNamesSomebodyAsTheyAreCalledNow(t *testing.T) {
	x := require.New(t)
	d := serve(t)
	ctx := t.Context()

	scope := []string{"openid", "profile"}
	x.Equal("Erin of contoso", d.saidOf(t, "c1", "contoso", "erin", scope...)["name"],
		"somebody with no display name is called what they were added as")

	d.called(t, d.who["contoso"], "Erin Kim")
	x.Equal("Erin Kim", d.saidOf(t, "c2", "contoso", "erin", scope...)["name"],
		"a token went on saying the name somebody was added with after they were renamed")

	// And somebody added by their alias alone, who is called something since.
	dana := addPerson(t, ctx, d, "dana")
	_, err := d.s.Ungated.Credential().Set(ctx, rstr.CredentialSetRequest_builder{
		Ref: rstr.HolderRef_builder{Id: dana.Bytes()}.Build(), Secret: []byte(password),
	}.Build())
	x.NoError(err)
	x.NotContains(d.saidOf(t, "c3", "contoso", "dana", scope...), "name", "nobody has said what they are called")

	d.called(t, dana, "Dana Park")
	x.Equal("Dana Park", d.saidOf(t, "c4", "contoso", "dana", scope...)["name"],
		"somebody added with no name had none in a token whatever their profile says")
}

// TestTheAddressATokenSaysIsTheOneProvedFirst: `email` is the first address
// somebody proved, among the ones they still have.
//
// It was whichever proved address the store returned first, and the read asked
// for no order. A relying party keys accounts on this claim, so proving a
// second address could make somebody a stranger to it: with `zed@` proved and
// then `abe@`, the token said `abe@`. The rule is one a person can know
// beforehand -- when each was proved, the row's identifier between two proved
// at one instant -- and it is not how an address is spelled nor when its row
// was written: proving another never changes the claim, and the next one
// proved is the answer only when the first is erased.
func TestTheAddressATokenSaysIsTheOneProvedFirst(t *testing.T) {
	x := require.New(t)
	d := serve(t)
	ctx := t.Context()

	scope := []string{"openid", "email"}
	began := time.Now().Add(-24 * time.Hour).Truncate(time.Second)

	// add writes an address onto somebody's row, proved by nobody yet.
	add := func(who pdid.Id, address string) *rstr.Email {
		t.Helper()
		v, err := d.s.Ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: who.Bytes()}.Build(), Address: address,
		}.Build())
		require.NoError(t, err)

		return v
	}
	// proved stamps it, as a link spent or a directory's word does, `after`
	// the moment this began.
	proved := func(v *rstr.Email, after time.Duration) {
		t.Helper()
		_, err := d.s.Ungated.Email().Patch(ctx, rstr.EmailPatchRequest_builder{
			Ref:          rstr.EmailRef_builder{Id: v.GetId()}.Build(),
			DateVerified: timestamppb.New(began.Add(after)),
			DateUpdated:  v.GetDateUpdated(),
		}.Build())
		require.NoError(t, err)
	}

	// Contoso's Erin writes `abe@` and then `zed@`, and proves them the other
	// way round: the one proved first is later by its spelling and by its row.
	erin := d.who["contoso"]
	abe, zed := add(erin, "abe@contoso.example"), add(erin, "zed@contoso.example")
	proved(zed, time.Hour)
	proved(abe, 2*time.Hour)
	x.Equal("zed@contoso.example", d.saidOf(t, "c1", "contoso", "erin", scope...)["email"],
		"of two proved addresses the token said the one proved second")

	// Fabrikam's has them the other way about: the one proved first is earlier
	// by its spelling and later by its row. So the rule is neither.
	other := d.who["fabrikam"]
	zed2, abe2 := add(other, "zed@fabrikam.example"), add(other, "abe@fabrikam.example")
	proved(abe2, time.Hour)
	proved(zed2, 2*time.Hour)
	x.Equal("abe@fabrikam.example", d.saidOf(t, "c2", "fabrikam", "erin", scope...)["email"],
		"of two proved addresses the token said the one proved second")

	// A third proved changes nothing, wherever it sorts; nor does one nobody
	// has proved.
	proved(add(erin, "aaa@contoso.example"), 3*time.Hour)
	add(erin, "aab@contoso.example")
	got := d.saidOf(t, "c3", "contoso", "erin", scope...)
	x.Equal("zed@contoso.example", got["email"], "proving another address changed the one a relying party keys on")
	x.Equal(true, got["email_verified"])

	// The first erased, the next one proved is the answer.
	_, err := d.s.Ungated.Email().Erase(ctx, rstr.EmailRef_builder{Id: zed.GetId()}.Build())
	x.NoError(err)
	x.Equal("abe@contoso.example", d.saidOf(t, "c4", "contoso", "erin", scope...)["email"],
		"with the first address erased, the claim is not the next one proved")
}

// TestATeamOfOneNameAtTwoSitesIsSaidOnce: `teams` is each alias once, in
// order.
//
// A team's alias is unique within a site, so one person is in `operators` at
// Seoul and in `operators` at Busan, and the claim was
// `["operators","operators"]` in whatever order the store answered. It is the
// aliases, as the manual says, and an alias said twice says nothing the first
// did not: the site is not in a token, and a team is nothing a product grants
// on.
func TestATeamOfOneNameAtTwoSitesIsSaidOnce(t *testing.T) {
	x := require.New(t)
	d := serve(t)
	ctx := t.Context()

	contoso := rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build()
	erin := rstr.HolderRef_builder{Id: d.who["contoso"].Bytes()}.Build()
	join := func(site *rstr.Site, alias string) {
		t.Helper()
		team, err := d.s.Ungated.Team().Add(ctx, rstr.TeamAddRequest_builder{
			Tenant: contoso, Site: rstr.SiteRef_builder{Id: site.GetId()}.Build(), Alias: alias,
		}.Build())
		require.NoError(t, err)
		_, err = d.s.Ungated.TeamMembership().Add(ctx, rstr.TeamMembershipAddRequest_builder{
			Holder: erin, Team: rstr.TeamRef_builder{Id: team.GetId()}.Build(),
		}.Build())
		require.NoError(t, err)
	}

	x.NotContains(d.saidOf(t, "c1", "contoso", "erin", "openid", "profile"), "teams", "somebody in no team")

	for _, at := range []string{"seoul", "busan"} {
		site, err := d.s.Ungated.Site().Add(ctx, rstr.SiteAddRequest_builder{Tenant: contoso, Alias: at}.Build())
		x.NoError(err)
		join(site, "release")
		join(site, "operators")
	}

	got := d.saidOf(t, "c2", "contoso", "erin", "openid", "profile")
	x.Equal([]any{"operators", "release"}, got["teams"], "an alias two sites share was said once for each")
}
