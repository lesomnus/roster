package cmd_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/prove"
)

// zone is what DNS says, for a test that would otherwise need one.
//
// Keyed by the whole record name, so a test says *this is published* rather than
// how roster would go looking for it -- the going looking is `prove`'s and has
// its own test.
func zone(records map[string][]string) func(*cmd.Config) {
	return func(c *cmd.Config) { c.Host.Asks = prove.Static(records) }
}

// claiming is a tenant asking for a name, through the walled plane as one of
// their own people.
func claiming(t *testing.T, b *built, who, in pdid.Id, name string) *app.HostProof {
	t.Helper()

	v, err := b.Walled.HostProof().Add(b.as(t.Context(), who, in), app.HostProofAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Name:   name,
	}.Build())
	require.NoError(t, err)

	return v
}

// adding is the second half: the row, which is where the lookup happens.
func adding(t *testing.T, b *built, who, in pdid.Id, name string) (*app.Host, error) {
	t.Helper()

	return b.Walled.Host().Add(b.as(t.Context(), who, in), app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Name:   name,
	}.Build())
}

// TestATenantRegistersItsOwnHostname is #42.
//
// `Host.name` is unique across the deployment, so the first writer took a name
// and the rightful owner was refused -- and the conclusion was a permission
// nothing could enforce: *do not put `/roster.HostService/Add` on a role a
// tenant's own administrators hold*. Every other row a tenant needs became
// theirs to write (#37, #38) and this one could not, which is #26 one row
// smaller.
//
// What makes it theirs is a proof: roster answers with a value, the tenant
// publishes it under the name, and roster reads it back.
func TestATenantRegistersItsOwnHostname(t *testing.T) {
	const name = "contoso.example.com"

	x := require.New(t)
	published := map[string][]string{}
	b, ctx := build(t, zone(published))
	b.mayAnything(b.ContosoUser, b.Contoso)

	c := claiming(t, b, b.ContosoUser, b.Contoso, name)
	x.NotEmpty(c.GetToken())
	x.True(len(c.GetToken()) > len(prove.Prefix), "a token is a prefix and nothing else")
	x.NotNil(c.GetDateExpires())

	t.Run("and nothing is claimed by claiming", func(t *testing.T) {
		x := require.New(t)

		// The whole reason the claim is a row of its own: a name is not held
		// until it is proved, so two tenants may be claiming one at once.
		_, err := b.Ungated.Host().Get(ctx, app.HostGetRequest_builder{
			Ref: app.HostRef_builder{Name: z.Ptr(name)}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})

	t.Run("a name whose record is not there is refused, saying what to publish", func(t *testing.T) {
		x := require.New(t)

		_, err := adding(t, b, b.ContosoUser, b.Contoso, name)
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.ErrorContains(err, prove.Record+"."+name)
		x.ErrorContains(err, c.GetToken())
	})

	t.Run("and one that says something else is refused too", func(t *testing.T) {
		x := require.New(t)

		published[prove.Record+"."+name] = []string{"roster-verify=somebody-elses"}
		_, err := adding(t, b, b.ContosoUser, b.Contoso, name)
		x.Equal(codes.FailedPrecondition, status.Code(err))
	})

	t.Run("published, the name is theirs and the row says roster checked", func(t *testing.T) {
		x := require.New(t)

		// Beside the wrong one, because a zone holds more than one TXT record at
		// a name and the answer is whether ours is among them.
		published[prove.Record+"."+name] = append(published[prove.Record+"."+name], c.GetToken())

		v, err := adding(t, b, b.ContosoUser, b.Contoso, name)
		x.NoError(err)
		x.Equal(name, v.GetName())
		x.NotNil(v.GetDateProved(), "a row a tenant proved does not say so")
	})

	t.Run("and the claim is spent, so the same name cannot be added twice", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.HostProof().Get(b.as(ctx, b.ContosoUser, b.Contoso),
			app.HostProofGetRequest_builder{
				Ref: app.HostProofRef_builder{Id: c.GetId()}.Build(),
			}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})

	t.Run("and a front door resolves the name to them", func(t *testing.T) {
		x := require.New(t)

		res, err := front.New(b.Ungated).WhoseHost(ctx, app.FrontWhoseHostRequest_builder{Host: name}.Build())
		x.NoError(err)
		x.Equal(b.Contoso.Bytes(), res.GetTenant())
	})
}

// TestTheLatestProofTakesTheName is the decision that a name can move.
//
// Two tenants, one name, and the one who can put a record under it has it.
// Nobody is asked, which is the right way round: proving requires present
// control of DNS, and whoever has that has the name whatever a row here says.
//
// The mechanism is the unique index being partial -- it covers the rows that are
// not erased -- so the incumbent's row is erased and the name is free in the same
// transaction the new one is written in.
func TestTheLatestProofTakesTheName(t *testing.T) {
	const name = "shared.example.com"

	x := require.New(t)
	published := map[string][]string{}
	b, ctx := build(t, zone(published))
	b.mayAnything(b.ContosoUser, b.Contoso)

	theirs := b.holder(t, ctx, b.Fabrikam, "someone")
	b.mayAnything(theirs, b.Fabrikam)

	// contoso has it first.
	first := claiming(t, b, b.ContosoUser, b.Contoso, name)
	published[prove.Record+"."+name] = []string{first.GetToken()}
	_, err := adding(t, b, b.ContosoUser, b.Contoso, name)
	x.NoError(err)

	t.Run("fabrikam may claim a name contoso holds", func(t *testing.T) {
		x := require.New(t)

		// Which is what the claim being its own row buys. A claim kept on the
		// `Host` row could not be made at all here, and that is the case that
		// has to work.
		second := claiming(t, b, theirs, b.Fabrikam, name)
		x.NotEqual(first.GetToken(), second.GetToken(),
			"two claims on one name share a token, so nothing tells them apart")

		t.Run("and cannot take it while contoso's record is the one published", func(t *testing.T) {
			x := require.New(t)

			_, err := adding(t, b, theirs, b.Fabrikam, name)
			x.Equal(codes.FailedPrecondition, status.Code(err))

			// And contoso still has it.
			res, err := front.New(b.Ungated).WhoseHost(ctx, app.FrontWhoseHostRequest_builder{Host: name}.Build())
			x.NoError(err)
			x.Equal(b.Contoso.Bytes(), res.GetTenant())
		})

		t.Run("and takes it the moment theirs is", func(t *testing.T) {
			x := require.New(t)

			published[prove.Record+"."+name] = []string{second.GetToken()}

			v, err := adding(t, b, theirs, b.Fabrikam, name)
			x.NoError(err)
			x.NotNil(v.GetDateProved())

			res, err := front.New(b.Ungated).WhoseHost(ctx, app.FrontWhoseHostRequest_builder{Host: name}.Build())
			x.NoError(err)
			x.Equal(b.Fabrikam.Bytes(), res.GetTenant())
		})

		t.Run("and contoso's row is gone rather than re-pointed", func(t *testing.T) {
			x := require.New(t)

			// Erased and not moved, because `Host.tenant` is immutable and a row
			// that changed tenant would mean something else. What contoso sees is
			// the name gone, and the erase is in the trail.
			vs, err := b.Walled.Host().List(b.as(ctx, b.ContosoUser, b.Contoso),
				app.HostListRequest_builder{}.Build())
			x.NoError(err)
			for _, v := range vs.GetItems() {
				x.NotEqual(name, v.GetName(), "contoso still holds a name fabrikam proved")
			}
		})
	})
}

// TestAHostnameSomebodyWasTrustedToWriteNeedsNoProof is the other road.
//
// A roster operator writing a name into a tenant is the person who routed it, so
// there is nothing for roster to check -- and in an air gap there is no route to
// check by, which is `Email.Attest`'s argument word for word. `date_proved` is
// how the row says which road it was.
func TestAHostnameSomebodyWasTrustedToWriteNeedsNoProof(t *testing.T) {
	x := require.New(t)

	// A resolver that cannot answer, so a lookup happening at all would fail
	// loudly rather than pass by luck.
	b, ctx := build(t, func(c *cmd.Config) {
		c.Host.Asks = prove.Refusing{Err: errNoDns}
	})

	v, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:   "written.example.com",
	}.Build())
	x.NoError(err)
	x.Nil(v.GetDateProved(), "a row nobody proved says it was")
}

// TestADeploymentThatCannotAskRefusesAClaim is `host.resolver: none`.
//
// An air gap, said rather than inferred. Without the setting the same deployment
// answers *claim your name* and then times out on a resolver that is not there,
// thirty seconds at a time; with it, the refusal names the setting and points at
// the road that works.
func TestADeploymentThatCannotAskRefusesAClaim(t *testing.T) {
	const name = "airgap.example.com"

	x := require.New(t)
	b, _ := build(t, func(c *cmd.Config) { c.Host.Resolver = prove.None })
	b.mayAnything(b.ContosoUser, b.Contoso)

	// Claiming is allowed, which is the smaller half: the row is inert and says
	// nothing about DNS. What cannot happen is spending it.
	claiming(t, b, b.ContosoUser, b.Contoso, name)

	_, err := adding(t, b, b.ContosoUser, b.Contoso, name)
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.ErrorContains(err, "host.resolver")
}

// TestAClaimIsRostersToWrite is the two fields on it that a caller may not set.
func TestAClaimIsRostersToWrite(t *testing.T) {
	b, ctx := build(t, zone(map[string][]string{}))
	b.mayAnything(b.ContosoUser, b.Contoso)

	as := b.as(ctx, b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	t.Run("a token, because a name proved by a record you did not write is not proof", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.HostProof().Add(as, app.HostProofAddRequest_builder{
			Tenant: at, Name: "chosen.example.com", Token: "roster-verify=mine",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
		x.ErrorContains(err, "token")
	})

	t.Run("and a window, because one that never closes is a name anybody may find", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.HostProof().Add(as, app.HostProofAddRequest_builder{
			Tenant: at, Name: "forever.example.com",
			DateExpires: timestamppb.New(time.Now().Add(100 * 365 * 24 * time.Hour)),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
		x.ErrorContains(err, "date_expires")
	})
}

// TestAnExpiredClaimIsRefusedRatherThanCollected is the sentence `prove.Swept`
// carries: the read decides and the sweep is hygiene.
//
// A claim past its window is refused the moment it is spent, whether or not
// anything has collected it -- because a sweep that is the mechanism is a sweep
// whose outage is a security incident.
func TestAnExpiredClaimIsRefusedRatherThanCollected(t *testing.T) {
	const name = "stale.example.com"

	x := require.New(t)
	published := map[string][]string{}
	b, ctx := build(t, zone(published))
	b.mayAnything(b.ContosoUser, b.Contoso)

	c := claiming(t, b, b.ContosoUser, b.Contoso, name)
	published[prove.Record+"."+name] = []string{c.GetToken()}

	// Wound back past its window, through the unwalled server: what is being
	// tested is the read, not how a row got old.
	_, err := b.Ungated.HostProof().Patch(ctx, app.HostProofPatchRequest_builder{
		Ref:         app.HostProofRef_builder{Id: c.GetId()}.Build(),
		DateExpires: timestamppb.New(time.Now().Add(-time.Minute)),
		DateUpdated: c.GetDateUpdated(),
	}.Build())
	x.NoError(err)

	_, err = adding(t, b, b.ContosoUser, b.Contoso, name)
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.ErrorContains(err, "expired")

	t.Run("and the sweep collects it", func(t *testing.T) {
		x := require.New(t)

		n, err := prove.Collect(ctx, b.Ent)
		x.NoError(err)
		x.Equal(1, n)
	})
}

// TestAClaimCannotBeMadeForSomebodyElsesTenant is the hole the discriminator
// would be if the wall were not behind it.
//
// `Host.Add` asks for a proof when the tenant being written into is the
// caller's own, so a caller who could name another tenant would be a caller who
// could skip it. The gate refuses an `Add` whose edges point into a tenant the
// caller cannot see -- `cmd/foreignedge_test.go` is that rule -- and this is it
// said about the one verb that now depends on it.
func TestAClaimCannotBeMadeForSomebodyElsesTenant(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t, zone(map[string][]string{}))
	b.mayAnything(b.ContosoUser, b.Contoso)

	_, err := b.Walled.Host().Add(b.as(ctx, b.ContosoUser, b.Contoso), app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(),
		Name:   "theirs.example.com",
	}.Build())
	x.Error(err)
	x.NotEqual(codes.OK, status.Code(err))

	// And nothing was written, whichever way it was refused.
	_, err = b.Ungated.Host().Get(ctx, app.HostGetRequest_builder{
		Ref: app.HostRef_builder{Name: z.Ptr("theirs.example.com")}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err))
}

// errNoDns is what a resolver that cannot ask answers with.
var errNoDns = errors.New("there is no route to a resolver from here")
