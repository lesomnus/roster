package cmd_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/cmd"
	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	entconsent "github.com/lesomnus/roster/internal/ent/consent"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
)

// An app's ledger of what its accounts agreed to (`proto/app/consent.proto`):
// recorded and asked over the wire as the app, the way an app reaches roster.

// consenting is a deployment with an account and an app that keeps consents.
type consenting struct {
	*built
	ctx     context.Context
	conn    *grpc.ClientConn
	account pdid.Id
}

func consentingAt(t *testing.T, with ...func(*cmd.Config)) *consenting {
	t.Helper()
	b, ctx := build(t, with...)

	return &consenting{built: b, ctx: ctx, conn: served(t, b.Server), account: b.holder(t, ctx, b.Contoso, "erin")}
}

// as is a call made as an app of contoso's, which may call anything.
func (c *consenting) as(t *testing.T, alias string) (app.ConsentServiceClient, context.Context, pdid.Id) {
	t.Helper()
	who := c.holder(t, c.ctx, c.Contoso, alias)
	c.mayAnything(who, c.Contoso)

	return app.NewConsentServiceClient(c.conn), auth.PlainProvider(who.String()).Provide(c.ctx), who
}

func (c *consenting) erin() *app.HolderRef {
	return app.HolderRef_builder{Id: c.account.Bytes()}.Build()
}

func doc(name, version string, optional bool) *app.ConsentDocument {
	return app.ConsentDocument_builder{Document: name, Version: version, Optional: optional}.Build()
}

// TestAnAppRecordsAndAsksConsent is the ledger as an app keeps it: what it
// asks for, what was agreed, a withdrawal, and an agreement again -- each a
// row of its own, the latest in force, and nothing written twice.
func TestAnAppRecordsAndAsksConsent(t *testing.T) {
	c := consentingAt(t)
	cl, as, _ := c.as(t, "rove")

	documents := []*app.ConsentDocument{doc("terms", "2026-10", false), doc("marketing", "1", true)}
	settled := func(t *testing.T, scope string, ds ...*app.ConsentDocument) []string {
		t.Helper()
		v, err := cl.Settled(as, app.ConsentSettledRequest_builder{Holder: c.erin(), Scope: scope, Documents: ds}.Build())
		require.NoError(t, err)
		owed := []string{}
		for _, d := range v.GetOwed() {
			owed = append(owed, d.GetDocument()+"@"+d.GetVersion())
		}
		require.Equal(t, len(owed) == 0, v.GetSettled())

		return owed
	}
	acts := func(t *testing.T) []*app.Consent {
		t.Helper()
		vs, err := cl.List(as, app.ConsentListRequest_builder{
			Filters: []*app.ConsentFilter{app.ConsentFilter_builder{Holder: c.erin()}.Build()},
		}.Build())
		require.NoError(t, err)

		return vs.GetItems()
	}

	t.Run("an account owes what it never agreed to", func(t *testing.T) {
		require.Equal(t, []string{"terms@2026-10", "marketing@1"}, settled(t, "", documents...))
	})

	var agreed []*app.Consent
	t.Run("and agreeing is an act per document", func(t *testing.T) {
		x := require.New(t)

		v, err := cl.Agree(as, app.ConsentAgreeRequest_builder{Holder: c.erin(), Documents: documents, Client: "web"}.Build())
		x.NoError(err)
		agreed = v.GetActs()
		x.Len(agreed, 2)
		for _, a := range agreed {
			x.Equal(app.ConsentAct_CONSENT_ACT_AGREE, a.GetAct())
			x.Equal(int64(1), a.GetSeq())
			x.Equal("web", a.GetClient())
		}
		x.Empty(settled(t, "", documents...))
	})

	t.Run("and agreeing again writes nothing", func(t *testing.T) {
		x := require.New(t)

		v, err := cl.Agree(as, app.ConsentAgreeRequest_builder{Holder: c.erin(), Documents: documents, Client: "cli"}.Build())
		x.NoError(err)
		x.Equal(agreed[0].GetId(), v.GetActs()[0].GetId(), "an agreement in force was recorded again")
		x.Equal(agreed[1].GetId(), v.GetActs()[1].GetId())
		x.Len(acts(t), 2)
	})

	t.Run("a required document is not withdrawn", func(t *testing.T) {
		_, err := cl.Withdraw(as, app.ConsentWithdrawRequest_builder{Holder: c.erin(), Document: "terms", Version: "2026-10"}.Build())
		require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	})

	t.Run("an optional one is, once, and is owed again", func(t *testing.T) {
		x := require.New(t)

		v, err := cl.Withdraw(as, app.ConsentWithdrawRequest_builder{Holder: c.erin(), Document: "marketing", Version: "1", Client: "web"}.Build())
		x.NoError(err)
		x.Equal(app.ConsentAct_CONSENT_ACT_WITHDRAW, v.GetAct())
		x.Equal(int64(2), v.GetSeq())

		_, err = cl.Withdraw(as, app.ConsentWithdrawRequest_builder{Holder: c.erin(), Document: "marketing", Version: "1"}.Build())
		x.Equal(codes.FailedPrecondition, status.Code(err), "a withdrawal was withdrawn")

		x.Equal([]string{"marketing@1"}, settled(t, "", documents...))
	})

	t.Run("and agreed to again is a third act, with the first two kept", func(t *testing.T) {
		x := require.New(t)

		v, err := cl.Agree(as, app.ConsentAgreeRequest_builder{Holder: c.erin(), Documents: documents[1:], Client: "web"}.Build())
		x.NoError(err)
		x.Equal(int64(3), v.GetActs()[0].GetSeq())
		x.Empty(settled(t, "", documents...))

		seqs := map[string][]int64{}
		for _, a := range acts(t) {
			seqs[a.GetDocument()] = append(seqs[a.GetDocument()], a.GetSeq())
		}
		x.ElementsMatch([]int64{1, 2, 3}, seqs["marketing"], "the ledger lost an act")
		x.ElementsMatch([]int64{1}, seqs["terms"])
	})

	t.Run("a version and a scope are their own", func(t *testing.T) {
		x := require.New(t)

		x.Equal([]string{"terms@2026-11"}, settled(t, "", doc("terms", "2026-11", false)))
		x.Equal([]string{"terms@2026-10"}, settled(t, "ws-1", doc("terms", "2026-10", false)))

		_, err := cl.Agree(as, app.ConsentAgreeRequest_builder{Holder: c.erin(), Scope: "ws-1", Documents: documents[:1]}.Build())
		x.NoError(err)
		x.Empty(settled(t, "ws-1", doc("terms", "2026-10", false)))
	})
}

// TestOneAppDoesNotAnswerForAnother: an app's consents are its own. Another
// app neither reads them, whatever its filter names, nor counts them as
// agreed, nor withdraws them.
func TestOneAppDoesNotAnswerForAnother(t *testing.T) {
	x := require.New(t)
	c := consentingAt(t)
	rove, asRove, roveId := c.as(t, "rove")
	other, asOther, _ := c.as(t, "other")

	v, err := rove.Agree(asRove, app.ConsentAgreeRequest_builder{
		Holder: c.erin(), Documents: []*app.ConsentDocument{doc("terms", "1", false), doc("news", "1", true)},
	}.Build())
	x.NoError(err)
	theirs := v.GetActs()[0]
	x.Equal(roveId.Bytes(), theirs.GetApp(), "the app was not the caller")

	vs, err := other.List(asOther, app.ConsentListRequest_builder{
		Filters: []*app.ConsentFilter{app.ConsentFilter_builder{Holder: c.erin(), App: roveId.Bytes()}.Build()},
	}.Build())
	x.NoError(err)
	x.Empty(vs.GetItems(), "an app listed another's consents")

	_, err = other.Get(asOther, app.ConsentGetRequest_builder{Ref: app.ConsentRef_builder{Id: theirs.GetId()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err), "an app read another's consent")

	s, err := other.Settled(asOther, app.ConsentSettledRequest_builder{
		Holder: c.erin(), Documents: []*app.ConsentDocument{doc("terms", "1", false)},
	}.Build())
	x.NoError(err)
	x.False(s.GetSettled(), "another app's agreement counted as this one's")

	_, err = other.Withdraw(asOther, app.ConsentWithdrawRequest_builder{Holder: c.erin(), Document: "news", Version: "1"}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "an app withdrew another's agreement")

	got, err := rove.Get(asRove, app.ConsentGetRequest_builder{Ref: app.ConsentRef_builder{Id: theirs.GetId()}.Build()}.Build())
	x.NoError(err, "the app that recorded it could not read it")
	x.Equal(theirs.GetId(), got.GetId())

	// And an app of one tenant records nothing about another's people: the
	// account is read through the wall first.
	stranger := c.holder(t, c.ctx, c.Fabrikam, "stranger")
	_, err = rove.Agree(asRove, app.ConsentAgreeRequest_builder{
		Holder:    app.HolderRef_builder{Id: stranger.Bytes()}.Build(),
		Documents: []*app.ConsentDocument{doc("terms", "1", false)},
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "an app recorded a consent for somebody of another tenant")
}

// TestAConsentIsNeverWrittenAsARow: the generated writes are not on the wire,
// so the app, the act and its place are only ever what `Agree` and `Withdraw`
// stamp -- and nothing changes or removes a row an app recorded.
func TestAConsentIsNeverWrittenAsARow(t *testing.T) {
	x := require.New(t)
	c := consentingAt(t)
	cl, as, id := c.as(t, "rove")

	v, err := cl.Agree(as, app.ConsentAgreeRequest_builder{Holder: c.erin(), Documents: []*app.ConsentDocument{doc("terms", "1", false)}}.Build())
	x.NoError(err)
	ref := app.ConsentRef_builder{Id: v.GetActs()[0].GetId()}.Build()

	// A row with nothing stamped in it. The app is required and only the
	// layer writes one, so this is refused whatever else is open -- and the
	// write is not served, and the layer refuses it besides.
	_, err = cl.Add(as, app.ConsentAddRequest_builder{Holder: c.erin(), Document: "terms", Version: "2"}.Build())
	x.Error(err, "a consent was written as a row")
	// And one naming the app, the act and its place, which a request may not
	// say at all.
	_, err = cl.Add(as, app.ConsentAddRequest_builder{
		Holder: c.erin(), App: id.Bytes(), Document: "terms", Version: "1",
		Act: app.ConsentAct_CONSENT_ACT_WITHDRAW, Seq: 2,
	}.Build())
	x.Error(err, "a consent was written with its app, act and place said by the request")
	_, err = cl.Erase(as, ref)
	x.Error(err, "a consent was erased")
	_, err = cl.Patch(as, app.ConsentPatchRequest_builder{Ref: ref, DateUpdatedForce: z.Ptr(true)}.Build())
	x.Error(err, "a consent was patched")

	vs, err := cl.List(as, app.ConsentListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 1)
}

// TestTwoAgreementsAtOnceAreOneAct: an app that sends the same agreement twice
// at once -- two tabs, a retry -- records it once. Both reach for the same
// next act, and the index unique over it refuses the second, which reads
// again and finds the agreement in force.
func TestTwoAgreementsAtOnceAreOneAct(t *testing.T) {
	x := require.New(t)
	c := consentingAt(t)
	cl, as, _ := c.as(t, "rove")

	const people = 20
	for i := range people {
		who := c.holder(t, c.ctx, c.Contoso, fmt.Sprintf("person-%d", i))
		ref := app.HolderRef_builder{Id: who.Bytes()}.Build()

		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, 2)
		for k := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[k] = cl.Agree(as, app.ConsentAgreeRequest_builder{
					Holder: ref, Documents: []*app.ConsentDocument{doc("terms", "1", false)},
				}.Build())
			}()
		}
		close(start)
		wg.Wait()

		for _, err := range errs {
			x.NoError(err, "an agreement lost a race and said so")
		}
		vs, err := cl.List(as, app.ConsentListRequest_builder{
			Filters: []*app.ConsentFilter{app.ConsentFilter_builder{Holder: ref}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1, "%s's agreement was recorded twice", who)
	}
}

// TestAConsentOutlivesItsAccountForAsLongAsItIsKept: erasing the account and
// forgetting the person leave its consents, which are what shows it agreed;
// `consent.keep` after the erase the deployment destroys them, on the trail as
// it does -- except under a hold, and not once the account is restored.
func TestAConsentOutlivesItsAccountForAsLongAsItIsKept(t *testing.T) {
	x := require.New(t)
	c := consentingAt(t)
	cl, as, _ := c.as(t, "rove")

	_, err := cl.Agree(as, app.ConsentAgreeRequest_builder{Holder: c.erin(), Documents: []*app.ConsentDocument{doc("terms", "1", false)}}.Build())
	x.NoError(err)
	of := func() []string {
		t.Helper()
		vs, err := c.Ent.Consent.Query().Where(entconsent.TenantId(c.Contoso.Uuid())).Ids(c.ctx)
		x.NoError(err)
		out := []string{}
		for _, v := range vs {
			out = append(out, pdid.Id(v).String())
		}

		return out
	}
	kept := of()
	x.Len(kept, 1)
	due := func(keep time.Duration) int {
		t.Helper()
		vs, err := forget.DueConsents(c.ctx, c.Ent, time.Now().Add(-keep))
		x.NoError(err)

		return len(vs)
	}

	_, err = c.Ungated.Holder().Erase(c.ctx, c.erin())
	x.NoError(err)
	x.Equal(kept, of(), "erasing the account took its consents")
	x.Zero(due(time.Hour), "a consent was due as soon as its account left")

	_, err = forget.Forget(c.ctx, c.Ent, c.account, trail.Policy{}, audit.Contents{})
	x.NoError(err)
	x.Equal(kept, of(), "forgetting the person took their consents")

	// An hour on, under a keep of one.
	x.NoError(c.Ent.Holder.UpdateOneId(c.account.Uuid()).SetDateErased(time.Now().Add(-2 * time.Hour)).Exec(c.ctx))

	t.Run("not under a hold", func(t *testing.T) {
		x := require.New(t)
		x.NoError(forget.Hold(c.ctx, c.Ungated, c.Contoso, "a dispute"))
		x.Zero(due(time.Hour), "a consent was due in a tenant an operator holds")
		x.NoError(forget.Lift(c.ctx, c.Ungated, c.Contoso))
	})

	t.Run("and then destroyed, on the trail", func(t *testing.T) {
		x := require.New(t)
		ids, err := forget.DueConsents(c.ctx, c.Ent, time.Now().Add(-time.Hour))
		x.NoError(err)
		x.Len(ids, 1)

		n, err := forget.DestroyConsents(c.ctx, c.Ent, ids, audit.Contents{})
		x.NoError(err)
		x.Equal(1, n)
		x.Empty(of(), "a consent past its keep is still there")

		said, err := c.Ent.Audit.Query().Where(entaudit.ObjectId(ids[0]), entaudit.Action(forget.DestroyedConsent)).Exist(c.ctx)
		x.NoError(err)
		x.True(said, "the destruction is not on the trail")
	})
}

// TestARestoredAccountKeepsItsConsents: a restore inside the window makes
// somebody again, and what they agreed to is theirs as it was -- not due, and
// still in force.
func TestARestoredAccountKeepsItsConsents(t *testing.T) {
	x := require.New(t)
	c := consentingAt(t)
	cl, as, _ := c.as(t, "rove")

	terms := []*app.ConsentDocument{doc("terms", "1", false)}
	_, err := cl.Agree(as, app.ConsentAgreeRequest_builder{Holder: c.erin(), Documents: terms}.Build())
	x.NoError(err)
	_, err = c.Ungated.Holder().Erase(c.ctx, c.erin())
	x.NoError(err)
	x.NoError(c.Ent.Holder.UpdateOneId(c.account.Uuid()).SetDateErased(time.Now().Add(-2 * time.Hour)).Exec(c.ctx))
	x.NoError(forget.Restore(c.ctx, c.Ent, c.account, audit.Contents{}))

	vs, err := forget.DueConsents(c.ctx, c.Ent, time.Now().Add(-time.Hour))
	x.NoError(err)
	x.Empty(vs, "a restored account's consents were due")

	s, err := cl.Settled(as, app.ConsentSettledRequest_builder{Holder: c.erin(), Documents: terms}.Build())
	x.NoError(err)
	x.True(s.GetSettled(), "a restored account owes what it agreed to")
}
