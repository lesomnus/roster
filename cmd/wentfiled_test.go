package cmd_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"uuid"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent/audit"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/prove"
	"github.com/lesomnus/roster/server/session"
	"github.com/lesomnus/roster/server/vouch"
)

// keepingDigests has the deployment keep a digest of every row on its trail,
// and hands back the configuration a command at the box is run with.
func keepingDigests(c *cmd.Config) func(*cmd.Config) {
	return func(cc *cmd.Config) {
		cc.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		*c = *cc
	}
}

// wentUnder holds the newest line about each of `gone` to what it is: a line
// that says the row went, by one of `acts`, filed under the tenant the row
// was of.
func wentUnder(t *testing.T, ctx context.Context, b *built, gone map[pdid.Id]pdid.Id, acts ...string) {
	t.Helper()
	x := require.New(t)

	for id, tenant := range gone {
		vs := linesAbout(t, ctx, b.Ent, id)
		x.NotEmpty(vs, "the trail says nothing of a %s that went", id.Domain())

		last := vs[len(vs)-1]
		x.Contains(acts, last.Action, "the newest line about a %s that went", id.Domain())
		x.Equal(tenant.Uuid(), last.TenantId, "%s of a %s is not filed under the tenant it was of", last.Action, id.Domain())
	}
}

// TestWhatASweepCollectsIsSaidUnderTheTenantItWasOf: a line that says a row
// went is filed where the lines about the row were, under its tenant.
//
// # What it was
//
// A line that says a row was removed has no row to read a tenant from, so it
// is filed under the tenant its context is framed as (`pd.RecordErased`), and
// the sweeps record with no frame. So what a sweep collected of a tenant's --
// a session, an attempt, a link, a delegation, a claim of a name, a consent
// past its keep -- was said under no tenant at all: the tenant's own reader of
// its trail was answered the line that made the row and never the one that
// says it went, and a hold on the tenant did not keep that line (#280).
//
// Whoever says which rows are due says whose each is, before it is removed,
// and the line is recorded as that tenant's.
func TestWhatASweepCollectsIsSaidUnderTheTenantItWasOf(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	k := consentingAt(t, keepingDigests(&c))
	b, ctx := k.built, k.ctx
	cs, err := c.Audit.Contents()
	x.NoError(err)

	// Somebody in each of two tenants, and an app of each that keeps consents.
	finn := b.holder(t, ctx, b.Fabrikam, "finn")
	shop := b.holder(t, ctx, b.Fabrikam, "shop")
	b.mayAnything(shop, b.Fabrikam)
	consents, asRove, _ := k.as(t, "rove")
	asShop := auth.PlainProvider(shop.String()).Provide(ctx)

	past := timestamppb.New(time.Now().Add(-time.Minute))
	by := pdid.New(pd.ApiKeyDomain).Bytes()
	store := session.New(b.Ent, cs)

	// What each sweep is about to collect, and the tenant it is of.
	gone := map[pdid.Id]pdid.Id{}
	for _, of := range []struct {
		tenant, who pdid.Id
		as          context.Context
	}{
		{b.Contoso, k.account, asRove},
		{b.Fabrikam, finn, asShop},
	} {
		who := app.HolderRef_builder{Id: of.who.Bytes()}.Build()

		attempt, err := b.Ungated.Continuation().Add(ctx, app.ContinuationAddRequest_builder{
			Holder: who, Secret: sweepable(t), Issuer: by, DateExpires: past,
		}.Build())
		x.NoError(err)
		gone[mustId(t, attempt.GetId())] = of.tenant

		link, err := b.Ungated.Link().Add(ctx, app.LinkAddRequest_builder{
			Holder: who, Secret: sweepable(t), Issuer: by, DateExpires: past,
		}.Build())
		x.NoError(err)
		gone[mustId(t, link.GetId())] = of.tenant

		delegation, err := b.Ungated.Delegation().Add(ctx, app.DelegationAddRequest_builder{
			Holder: who, Secret: sweepable(t), Issuer: by, Methods: []string{app.HolderService_Get_FullMethodName}, DateExpires: past,
		}.Build())
		x.NoError(err)
		gone[mustId(t, delegation.GetId())] = of.tenant

		// How long a claim is good for is roster's to say when it is made, so
		// this one is made and then written as past it.
		claim, err := b.Ungated.HostProof().Add(ctx, app.HostProofAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: of.tenant.Bytes()}.Build(), Name: "stale." + of.tenant.String() + ".example",
		}.Build())
		x.NoError(err)
		_, err = b.Ungated.HostProof().Patch(ctx, app.HostProofPatchRequest_builder{
			Ref: app.HostProofRef_builder{Id: claim.GetId()}.Build(), DateExpires: past, DateUpdated: claim.GetDateUpdated(),
		}.Build())
		x.NoError(err)
		gone[mustId(t, claim.GetId())] = of.tenant

		cookie := "a-cookie-of-" + of.who.String()
		x.NoError(store.Put(ctx, authsession.Session{
			Key: cookie, Id: of.who.String(), Grant: frame.Whole(), Expires: time.Now().Add(-time.Minute),
		}))
		gone[sessionOf(t, ctx, b.Ent, cookie)] = of.tenant

		_, err = consents.Agree(of.as, app.ConsentAgreeRequest_builder{
			Holder: who, Documents: []*app.ConsentDocument{doc("terms", "1", false)},
		}.Build())
		x.NoError(err)
	}
	agreed, err := b.Ent.Consent.Query().All(ctx)
	x.NoError(err)
	x.Len(agreed, 2)
	kept := []uuid.UUID{}
	for _, v := range agreed {
		gone[pdid.Id(v.Id)] = pdid.Id(v.TenantId)
		kept = append(kept, v.Id)
	}
	x.Len(gone, 12)

	rows := func() error { return cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}) }
	x.NoError(rows())

	// A consent is destroyed once its account is erased, and that is asked
	// again where it is destroyed (#280): so the two of them are erased, as
	// somebody who left is.
	for _, who := range []pdid.Id{k.account, finn} {
		_, err := b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: who.Bytes()}.Build())
		x.NoError(err)
	}

	for _, collect := range []func() (int, error){
		func() (int, error) { return keys.Collect(ctx, b.Ent, cs) },
		func() (int, error) { return vouch.Collect(ctx, b.Ent, cs) },
		func() (int, error) { return prove.Collect(ctx, b.Ent, cs) },
		func() (int, error) { return session.Collect(ctx, b.Ent, cs) },
		func() (int, error) { return forget.DestroyConsents(ctx, b.Ent, kept, time.Now().Add(time.Hour), cs) },
	} {
		n, err := collect()
		x.NoError(err)
		x.NotZero(n)
	}

	wentUnder(t, ctx, b, gone, keys.Expired, vouch.Expired, prove.Expired, session.Expired, forget.DestroyedConsent)
	x.NoError(rows())

	// So a tenant's own reader of its trail is answered what went of theirs,
	// and nothing of what went of another's.
	reads := func(who, tenant, about pdid.Id) int {
		res, err := b.Walled.Audit().List(b.as(ctx, who, tenant), app.AuditListRequest_builder{
			Filters: []*app.AuditFilter{app.AuditFilter_builder{ObjectId: about.Bytes()}.Build()},
		}.Build())
		x.NoError(err)

		return len(res.GetItems())
	}
	// Read by somebody of each tenant who is still there: the two whose rows
	// went were erased above.
	for id, tenant := range gone {
		ours, theirs := b.ContosoUser, shop
		if tenant == b.Fabrikam {
			ours, theirs = shop, b.ContosoUser
		}
		other := b.Contoso
		if tenant == b.Contoso {
			other = b.Fabrikam
		}

		x.Equal(len(linesAbout(t, ctx, b.Ent, id)), reads(ours, tenant, id), "a tenant does not read every line about a %s of its own", id.Domain())
		x.Zero(reads(theirs, other, id), "a tenant reads what went of another's")
	}
}

// TestWhatGoesWithAPersonOrATenantIsSaidUnderThatTenant is the same of the
// two removals a clock or an operator makes: what a person forgotten took
// with them, and what was left of a tenant destroyed. Both knew the tenant
// and recorded with no frame, so each line that says a row went was filed
// under none (#280).
func TestWhatGoesWithAPersonOrATenantIsSaidUnderThatTenant(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, keepingDigests(&c))
	cs, err := c.Audit.Contents()
	x.NoError(err)
	p, err := c.Audit.Policy()
	x.NoError(err)
	rows := func() error { return cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}) }

	// Somebody with an address and a key, and a tenant with a site and a
	// team in it beside what it was made with.
	hal := b.holder(t, ctx, b.Contoso, "hal")
	address, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder: app.HolderRef_builder{Id: hal.Bytes()}.Build(), Address: "hal@contoso.example",
	}.Build())
	x.NoError(err)
	_, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	key, err := b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder: app.HolderRef_builder{Id: hal.Bytes()}.Build(), Alias: "laptop", Secret: sum,
		Methods: []string{app.HolderService_Get_FullMethodName},
	}.Build())
	x.NoError(err)

	_, err = forget.Forget(ctx, b.Ent, hal, p, cs, nil)
	x.NoError(err)
	wentUnder(t, ctx, b, map[pdid.Id]pdid.Id{
		mustId(t, address.GetId()): b.Contoso,
		mustId(t, key.GetId()):     b.Contoso,
	}, forget.ForgottenHolder)

	seoul := b.site(t, ctx, b.Fabrikam, "seoul")
	ops := b.team(t, ctx, seoul, "ops")
	x.NoError(b.Ent.Tenant.UpdateOneId(b.Fabrikam.Uuid()).SetDateClosed(time.Now()).Exec(ctx))
	_, err = forget.DestroyTenant(ctx, b.Ent, b.Fabrikam, p, cs, nil)
	x.NoError(err)
	wentUnder(t, ctx, b, map[pdid.Id]pdid.Id{seoul: b.Fabrikam, ops: b.Fabrikam}, forget.DestroyedTenant)

	// And no line of either is filed under no tenant.
	unfiled, err := b.Ent.Audit.Query().Where(
		audit.ActionIn(forget.ForgottenHolder, forget.DestroyedTenant),
		audit.TenantId(uuid.Nil()),
	).Count(ctx)
	x.NoError(err)
	x.Zero(unfiled, "%d line(s) of a forgetting or a destruction are filed under no tenant", unfiled)

	x.NoError(rows())
}

// TestAHoldKeepsTheLineThatSaysARowWent: what keeps a tenant's trail keeps
// the line that says a row of the tenant's went, beside the lines that say
// the row was there.
//
// # What it was
//
// A hold keeps what the held tenant may read. Somebody of a held tenant was
// forgotten, and the line that says their key went was filed under no tenant
// -- so a discard by hand took it, and left the held line that made the key.
// A check of the rows then answered `missing api-key <id>`: a thing the trail
// says is a row and is not, which is what a key somebody deleted in the
// database looks like. The hold had kept the half of the record that made the
// other half a finding (#280).
func TestAHoldKeepsTheLineThatSaysARowWent(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, keepingDigests(&c))
	cs, err := c.Audit.Contents()
	x.NoError(err)
	p, err := c.Audit.Policy()
	x.NoError(err)
	// What a hold keeps, it keeps from a forgetting and from a discard alike,
	// as `roster forget` and `roster trail prune` hand it over.
	p = forget.Held(p, b.Ent)

	hal := b.holder(t, ctx, b.Contoso, "hal")
	_, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	made, err := b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder: app.HolderRef_builder{Id: hal.Bytes()}.Build(), Alias: "laptop", Secret: sum,
		Methods: []string{app.HolderService_Get_FullMethodName},
	}.Build())
	x.NoError(err)
	key := mustId(t, made.GetId())

	x.NoError(forget.Hold(ctx, b.Ungated, b.Contoso, "case 77"))
	_, err = forget.Forget(ctx, b.Ent, hal, p, cs, nil)
	x.NoError(err)

	// A discard by hand of every line about a key, whenever it was written:
	// what `roster trail prune --kind api-key --discard` asks. Of that kind
	// alone, so that what the check finds afterwards is about this -- a
	// discard takes the newest line about a live row of a tenant nobody
	// holds, which is a finding of another kind.
	_, held, err := p.Collect(ctx, pd.TrailStore(b.Ent), trail.Only(pd.ApiKeyDomain), time.Now().Add(time.Minute))
	x.NoError(err)
	x.True(held.Any(), "nothing of a held tenant's was kept")

	found, err := findingsOf(t, &c)
	x.NoError(err, "a hold kept the line that made a key and not the one that says it went:\n%s", found)

	vs := linesAbout(t, ctx, b.Ent, key)
	x.Len(vs, 2, "a held tenant's key is said to have been made, and to have gone")
	x.Equal(app.ApiKeyService_Add_FullMethodName, vs[0].Action)
	x.Equal(forget.ForgottenHolder, vs[1].Action)
	x.Equal(b.Contoso.Uuid(), vs[1].TenantId)
}
