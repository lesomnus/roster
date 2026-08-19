package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// issuerOf is the identifier a request carrying `b.Token` arrives as.
//
// The **key row**, not the service holder it hangs off: `cmd.Resolver` reads a
// deployment key with an empty select and frames the caller as the row. So this
// is what a delegation minted for that caller has to be bound to, and getting
// it from the same place the resolver does is what stops this test from passing
// against a binding nothing would ever satisfy.
func issuerOf(t *testing.T, ctx context.Context, b *keyedBuilt) []byte {
	t.Helper()

	v, err := b.Control.Ungated.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
		Ref:    app.ApiKeyRef_builder{Secret: keys.Sum(b.Token)}.Build(),
		Select: app.ApiKeySelect_builder{}.Build(),
	}.Build())
	require.NoError(t, err)

	return v.GetId()
}

// delegates mints one for somebody and answers with the token.
//
// Through `Ungated`, which is where `keys.Delegate` is meant to be called from
// -- `VouchService.Verify` reads the unwalled server for the same reason, and
// this rides back on its answer. Nothing mints one over the wire, and D24 is
// why: a page decides the lifetime and the scope, and there is no page yet.
func delegates(t *testing.T, ctx context.Context, b *keyedBuilt, who pdid.Id, methods []string, in time.Duration) string {
	t.Helper()

	token, _, err := keys.Delegate(ctx, b.Ungated, keys.Delegated{
		Holder:  who,
		Issuer:  issuerOf(t, ctx, b),
		Methods: methods,
		For:     in,
	})
	require.NoError(t, err)

	return token
}

// wrote puts a delegation row down directly, with whatever expiry is wanted.
//
// `keys.Delegate` refuses to mint one that has already expired, which is the
// right refusal and leaves the read's own check with nothing to be tested by.
// So this writes the row, the way `mintFor` does for a key.
func wrote(t *testing.T, ctx context.Context, b *keyedBuilt, who pdid.Id, methods []string, expires *timestamppb.Timestamp) string {
	t.Helper()
	x := require.New(t)

	token, sum, err := keys.Mint(keys.PrefixDelegation)
	x.NoError(err)

	req := app.DelegationAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Secret:  sum,
		Issuer:  issuerOf(t, ctx, b),
		Methods: methods,
	}
	if expires != nil {
		req.DateExpires = expires
	}

	_, err = b.Ungated.Delegation().Add(ctx, req.Build())
	x.NoError(err)

	return token
}

// mayList gives somebody a binding that allows one method across their tenant.
func mayList(t *testing.T, ctx context.Context, b *keyedBuilt, who pdid.Id, method string) {
	t.Helper()
	x := require.New(t)

	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Alias:   "reader-" + method[len(method)-4:],
		Methods: []string{method},
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
}

// TestADelegationIsThePersonAndNotTheApp is PLAN.md D23, which exists because
// there was no way for a product app to ask roster a question **as** somebody
// it had signed in.
//
// The two obvious ways are both wrong and D23 says why: the app's own `rk_` key
// belongs to the deployment and sees every tenant there is, and the app
// filtering in its own code is what D17 already named -- *that is the kind of
// thing that leaks by being forgotten*, and on a self-service screen one bug in
// one app exposes everybody's identities while roster answered every read
// correctly.
//
// So this is the same claim `rt_` makes, on a credential that lives for
// minutes: the wall, the bindings and the sites are the person's.
func TestADelegationIsThePersonAndNotTheApp(t *testing.T) {
	b := keyFor(t, verify)
	ctx := t.Context()

	// A second customer, so that "sees one tenant" is a claim with something to
	// be wrong about.
	hooli := add(t, ctx, b.Server, "hooli")
	addHolder(t, ctx, b.Server, hooli, "erlich")

	const listHolders = "/roster.HolderService/List"
	mayList(t, ctx, b, b.Who, listHolders)

	hers := delegates(t, ctx, b, b.Who, []string{listHolders}, 0)

	c := app.NewHolderServiceClient(b.Conn)
	list := func(token string) (*app.HolderListResponse, error) {
		return c.List(bearing(ctx, token), app.HolderListRequest_builder{}.Build())
	}

	t.Run("it reads the tenant of the person it was minted for", func(t *testing.T) {
		x := require.New(t)

		v, err := list(hers)
		x.NoError(err)
		x.NotEmpty(v.GetItems())

		for _, h := range v.GetItems() {
			x.NotEqual("erlich", h.GetAlias(),
				"a delegation read a tenant its person is not in")
		}
	})

	t.Run("and no further than it was minted for", func(t *testing.T) {
		x := require.New(t)

		narrow := delegates(t, ctx, b, b.Who, []string{"/roster.MeService/Get"}, 0)

		_, err := list(narrow)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	// The whole guarantee, and the one that makes handing an app a delegation
	// safer than handing it anything else: the methods on the row are an
	// attenuation of what that person may do, never a grant of their own.
	t.Run("and no further than the person", func(t *testing.T) {
		x := require.New(t)

		// Bob may do nothing -- no binding at all.
		bob := addHolder(t, ctx, b.Server, b.Acme, "bob")
		his := delegates(t, ctx, b, bob, []string{listHolders}, 0)

		_, err := list(his)
		x.Equal(codes.PermissionDenied, status.Code(err),
			"the methods on a delegation granted what its holder does not hold")
	})

	// Minutes is the whole of what makes a credential minted without the person
	// present acceptable, so the expiry is not decoration.
	t.Run("and not after it has expired", func(t *testing.T) {
		x := require.New(t)

		stale := wrote(t, ctx, b, b.Who, []string{listHolders},
			timestamppb.New(time.Now().Add(-time.Minute)))

		_, err := list(stale)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	// And a row with no expiry at all is refused rather than read as one that
	// never expires. The schema cannot say required -- D6, and F3 is open --
	// so an absent value means a row nothing should have written, and treating
	// it as endless would turn a minting bug into a credential that outlives
	// everybody. `ApiKey` reads the same column the other way, deliberately.
	t.Run("and not without one at all", func(t *testing.T) {
		x := require.New(t)

		forever := wrote(t, ctx, b, b.Who, []string{listHolders}, nil)

		_, err := list(forever)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})
}

// TestADelegationIsBoundToWhoeverWasGivenIt is D21's condition and D23's,
// written the same way in both: *bound to the caller it was issued to*, so one
// product app cannot pick up an authentication another one started.
//
// Where it is checked is the part worth pinning. `auth.TokenStore.Lookup` is
// handed the token and nothing else -- no caller, no peer, no frame -- so the
// in-process path has nothing to compare against and a check written there
// compiles, runs, and binds nothing. `Introspect` runs behind roster's own
// authentication, so that is where it lives.
func TestADelegationIsBoundToWhoeverWasGivenIt(t *testing.T) {
	b := keyFor(t, introspect)
	ctx := t.Context()

	const get = "/roster.HolderService/Get"
	mine := delegates(t, ctx, b, b.Who, []string{get}, 0)

	c := pdpb.NewTokenServiceClient(b.Conn)

	t.Run("the app it was issued to is told who it is", func(t *testing.T) {
		x := require.New(t)

		res, err := c.Introspect(bearing(ctx, b.Token),
			pdpb.TokenIntrospectRequest_builder{Token: mine}.Build())
		x.NoError(err)

		x.Equal(b.Who.Bytes(), res.GetId(), "the holder, not the row")
		x.Equal(b.Acme.Bytes(), res.GetTenantId())

		id, err := auth.IdentityFrom(res)
		x.NoError(err)
		x.True(id.Grant.Allows(get))
		x.False(id.Grant.IsWhole())
		x.False(id.Expires.IsZero(), "a delegation that never expires is not one")
	})

	// A second app, holding its own key, presenting the first one's delegation.
	t.Run("and another app is told nothing", func(t *testing.T) {
		x := require.New(t)

		other := addHolder(t, ctx, b.Control, controlTenantOf(t, ctx, b), "other-app")

		token, sum, err := keys.Mint(keys.PrefixDeployment)
		x.NoError(err)

		_, err = b.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: other.Bytes()}.Build(),
			Alias:   "theirs",
			Secret:  sum,
			Methods: []string{introspect},
		}.Build())
		x.NoError(err)

		_, err = c.Introspect(bearing(ctx, token),
			pdpb.TokenIntrospectRequest_builder{Token: mine}.Build())
		x.Equal(codes.NotFound, status.Code(err),
			"one app picked up what another was issued")
	})
}

// controlTenantOf is the control plane's one tenant.
func controlTenantOf(t *testing.T, ctx context.Context, b *keyedBuilt) pdid.Id {
	t.Helper()
	x := require.New(t)

	v, err := b.Control.Ungated.Tenant().List(ctx, app.TenantListRequest_builder{}.Build())
	x.NoError(err)
	x.NotEmpty(v.GetItems())

	return mustId(t, v.GetItems()[0].GetId())
}

// TestNothingMintsADelegationThatOpensNoDoor is the three refusals `Delegate`
// makes, and each of them is a way to write a row that reads as working.
func TestNothingMintsADelegationThatOpensNoDoor(t *testing.T) {
	b := keyFor(t, verify)
	ctx := t.Context()

	who := issuerOf(t, ctx, b)

	for _, tc := range []struct {
		desc string
		req  keys.Delegated
	}{
		{"for nobody", keys.Delegated{Issuer: who, Methods: []string{verify}}},
		{
			// Empty is not a state the column can hold: two empty slices
			// compare **equal** in constant time, so a delegation bound to
			// nobody would match a caller whose own identifier failed to
			// resolve.
			"bound to nobody",
			keys.Delegated{Holder: b.Who, Methods: []string{verify}},
		},
		{"allowing nothing", keys.Delegated{Holder: b.Who, Issuer: who}},
		{
			"already expired",
			keys.Delegated{Holder: b.Who, Issuer: who, Methods: []string{verify}, For: -time.Minute},
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			_, _, err := keys.Delegate(ctx, b.Ungated, tc.req)
			x.Equal(codes.InvalidArgument, status.Code(err))
		})
	}
}

// TestADelegationIsNotOnTheWire is the pair of doors `CredentialService` is
// behind, closed over the same kind of column.
//
// The generated `Get` answers with whatever it was asked for and one of those
// is the verifier, so registration is left out -- which is also the only
// control that covers a stream, since this app installs `grpcx.ClosedUnary`
// rather than `grpcx.Closed`. The batch is the second door: it arrives as one
// method carrying many and dispatches through the app's own table, so "not
// registered" never reaches it.
func TestADelegationIsNotOnTheWire(t *testing.T) {
	b := keyFor(t, "/roster.DelegationService/Get", "/roster.DelegationService/Add",
		pdpb.BatchService_Do_FullMethodName)
	ctx := t.Context()

	_, v, err := keys.Delegate(ctx, b.Ungated, keys.Delegated{
		Holder:  b.Who,
		Issuer:  issuerOf(t, ctx, b),
		Methods: []string{verify},
	})
	require.NoError(t, err)

	t.Run("no service answers for it", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewDelegationServiceClient(b.Conn).Get(bearing(ctx, b.Token),
			app.DelegationGetRequest_builder{
				Ref:    app.DelegationRef_builder{Id: v.GetId()}.Build(),
				Select: app.DelegationSelect_builder{All: z.Ptr(true)}.Build(),
			}.Build())
		x.Equal(codes.Unimplemented, status.Code(err), "the delegation service answered")
	})

	t.Run("and a batch cannot carry one either", func(t *testing.T) {
		x := require.New(t)

		req, err := anypb.New(app.DelegationGetRequest_builder{
			Ref:    app.DelegationRef_builder{Id: v.GetId()}.Build(),
			Select: app.DelegationSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)

		_, err = pdpb.NewBatchServiceClient(b.Conn).Do(bearing(ctx, b.Token),
			pdpb.BatchRequest_builder{
				Ops: []*pdpb.Op{pdpb.Op_builder{
					Method:  app.DelegationService_Get_FullMethodName,
					Request: req,
				}.Build()},
			}.Build())
		x.Error(err, "a batch read the delegation the wire will not serve")
		x.NotEqual(codes.OK, status.Code(err))
	})

	// And what was minted is a hash, so even a reader that got past both doors
	// finds nothing to present.
	t.Run("and what is stored is not the token", func(t *testing.T) {
		x := require.New(t)

		row, err := b.Ungated.Delegation().Get(ctx, app.DelegationGetRequest_builder{
			Ref:    app.DelegationRef_builder{Id: v.GetId()}.Build(),
			Select: app.DelegationSelect_builder{Secret: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Len(row.GetSecret(), 32, "a sha256 and not a token")
	})
}
