package cmd_test

import (
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	enthostproof "github.com/lesomnus/roster/internal/ent/hostproof"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/prove"
)

// TestAnExpiredClaimOfANameIsReplacedByMakingAnother: a claim of a name is
// good for a day, and `HostService/Add` on one that has run out says *that
// claim expired; make another and publish what it answers with*. Making
// another was refused: a tenant has one claim of a name, the expired one was
// still that claim, and `HostProofService/Add` answered `AlreadyExists` until
// the daily sweep collected it -- unless somebody erased the expired claim
// first, which nothing said to do. Right is what the refusal says: another.
//
// A claim that has run out proves nothing and is spent by nothing, so a new
// one replaces it. One still good is not replaced: its token is what the
// tenant was told to publish, and a second `Add` handing out another would
// make the first a record that never matches.
func TestAnExpiredClaimOfANameIsReplacedByMakingAnother(t *testing.T) {
	const name = "late.example.com"

	x := require.New(t)
	published := map[string][]string{}
	b, ctx := build(t, zone(published))
	b.mayAnything(b.ContosoUser, b.Contoso)
	as := b.as(ctx, b.ContosoUser, b.Contoso)
	at := func(in pdid.Id) *app.TenantRef { return app.TenantRef_builder{Id: in.Bytes()}.Build() }

	again := func() (*app.HostProof, error) {
		return b.Walled.HostProof().Add(as, app.HostProofAddRequest_builder{Tenant: at(b.Contoso), Name: name}.Build())
	}
	// expires is a day having passed for one claim.
	expires := func(v *app.HostProof) {
		t.Helper()
		_, err := b.Ungated.HostProof().Patch(ctx, app.HostProofPatchRequest_builder{
			Ref:              app.HostProofRef_builder{Id: v.GetId()}.Build(),
			DateExpired:      timestamppb.New(time.Now().Add(-time.Minute)),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		x.NoError(err)
	}
	claims := func(in pdid.Id) []string {
		t.Helper()
		vs, err := b.Ent.HostProof.Query().
			Where(enthostproof.TenantId(in.Uuid()), enthostproof.NameEQ(name), enthostproof.DateErasedIsNil()).
			All(ctx)
		x.NoError(err)

		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = v.Token
		}

		return out
	}

	first := claiming(t, b, b.ContosoUser, b.Contoso, name)
	published[prove.Record+"."+name] = []string{first.GetToken()}

	// A claim still good stands, and a second is refused as it was.
	_, err := again()
	x.Equal(codes.AlreadyExists, status.Code(err), "a claim still good was replaced: %v", err)
	x.Equal([]string{first.GetToken()}, claims(b.Contoso))

	// Somebody else's claim of the same name, which runs out too: a name is
	// not held until it is proved, so two tenants may be claiming one.
	other := b.holder(t, ctx, b.Fabrikam, "other")
	b.mayAnything(other, b.Fabrikam)
	theirs := claiming(t, b, other, b.Fabrikam, name)

	expires(first)
	expires(theirs)

	_, err = adding(t, b, b.ContosoUser, b.Contoso, name)
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.ErrorContains(err, "make another", "the refusal this test holds true says something else now")

	// As told.
	second, err := again()
	x.NoError(err, "making another, as the refusal said to, was refused")
	x.NotEqual(first.GetToken(), second.GetToken(), "the claim that ran out was handed back")
	x.True(second.GetDateExpired().AsTime().After(time.Now()), "the new claim has run out already")
	x.Equal([]string{second.GetToken()}, claims(b.Contoso), "the tenant has two claims of one name, or the old one still")
	x.Equal([]string{theirs.GetToken()}, claims(b.Fabrikam), "another tenant's claim went with this one's")

	// What the old claim answered with proves nothing now, and the new one's
	// does.
	_, err = adding(t, b, b.ContosoUser, b.Contoso, name)
	x.Equal(codes.FailedPrecondition, status.Code(err), "a name was proved by a claim that had run out: %v", err)
	x.ErrorContains(err, second.GetToken())

	published[prove.Record+"."+name] = []string{second.GetToken()}
	v, err := adding(t, b, b.ContosoUser, b.Contoso, name)
	x.NoError(err)
	x.NotNil(v.GetDateProved())
}
