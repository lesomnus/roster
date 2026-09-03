package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

const (
	delegate    = "/roster.VouchService/Delegate"
	revoke      = "/roster.DelegationService/Revoke"
	listHolders = "/roster.HolderService/List"
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
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
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

// TestADelegationIsThePersonAndNotTheApp is D23, which exists because
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
	fabrikam := add(t, ctx, b.Server, "fabrikam")
	addHolder(t, ctx, b.Server, fabrikam, "erlich")

	const listHolders = "/roster.HolderService/List"
	mayList(t, ctx, b, b.Who, listHolders)

	hers := delegates(t, ctx, b, b.Who, []string{listHolders}, 0)

	c := app.NewHolderServiceClient(b.Conn)
	list := func(token string) (*app.HolderListResponse, error) {
		return c.List(acting(ctx, b.Token, token), app.HolderListRequest_builder{}.Build())
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
		bob := addHolder(t, ctx, b.Server, b.Contoso, "bob")
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
		x.Equal(b.Contoso.Bytes(), res.GetTenantId())

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

// TestADelegationsTokenIsNotOnTheWire is the door `Delegation`'s reads are
// behind, and it moved: from *the service is not registered* to *the column
// never leaves*.
//
// The generated `Get` answers with whatever it is asked for, and one of those
// is the verifier -- which is why the service was once left off the wire whole.
// `ts/plan.md` § C opened `Get`, `List` and `Erase`, because a person listing
// where they are signed in is a read of these rows and a curated copy on
// `MeService.Get` would have been a second name for them. What makes that safe
// is the layer roster already has for every `(payday.field).secret`: the sink
// strips it on the way out, on the direct road and through a batch alike, so
// a caller asking for **everything** gets a row with no token in it. The
// stronger statement is the one this keeps: the hash is in the store and never
// in an answer.
func TestADelegationsTokenIsNotOnTheWire(t *testing.T) {
	b := keyFor(t, "/roster.DelegationService/Get", "/roster.DelegationService/Add",
		pdpb.BatchService_Do_FullMethodName)
	ctx := t.Context()

	_, v, err := keys.Delegate(ctx, b.Ungated, keys.Delegated{
		Holder:  b.Who,
		Issuer:  issuerOf(t, ctx, b),
		Methods: []string{verify},
	})
	require.NoError(t, err)

	// What is stored, to check the answers against: a sha256 and not a token.
	row, err := b.Ungated.Delegation().Get(ctx, app.DelegationGetRequest_builder{
		Ref:    app.DelegationRef_builder{Id: v.GetId()}.Build(),
		Select: app.DelegationSelect_builder{Secret: z.Ptr(true)}.Build(),
	}.Build())
	require.NoError(t, err)
	require.Len(t, row.GetSecret(), 32, "a sha256 and not a token")

	t.Run("the service answers, and the column is not in the answer", func(t *testing.T) {
		x := require.New(t)

		got, err := app.NewDelegationServiceClient(b.Conn).Get(bearing(ctx, b.Token),
			app.DelegationGetRequest_builder{
				Ref:    app.DelegationRef_builder{Id: v.GetId()}.Build(),
				Select: app.DelegationSelect_builder{All: z.Ptr(true)}.Build(),
			}.Build())
		x.NoError(err, "the delegation service is not on the wire")
		x.Empty(got.GetSecret(), "the verifier left the store")
		x.Equal([]string{verify}, got.GetMethods(), "the rest of the row did not come")
	})

	t.Run("and a batch strips it the same way", func(t *testing.T) {
		x := require.New(t)

		req, err := anypb.New(app.DelegationGetRequest_builder{
			Ref:    app.DelegationRef_builder{Id: v.GetId()}.Build(),
			Select: app.DelegationSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)

		res, err := pdpb.NewBatchServiceClient(b.Conn).Do(bearing(ctx, b.Token),
			pdpb.BatchRequest_builder{
				Ops: []*pdpb.Op{pdpb.Op_builder{
					Method:  app.DelegationService_Get_FullMethodName,
					Request: req,
				}.Build()},
			}.Build())
		x.NoError(err, "a batch could not read what the wire serves")
		x.NotContains(res.String(), string(row.GetSecret()), "the verifier left the store through a batch")
	})

	t.Run("and a caller-chosen token is still refused", func(t *testing.T) {
		x := require.New(t)

		// `Add` stays closed: it would take a verifier the caller chose, which
		// is a token nobody minted. `Unimplemented` because it is shut by
		// method, on a service that is otherwise there.
		_, err := app.NewDelegationServiceClient(b.Conn).Add(bearing(ctx, b.Token),
			app.DelegationAddRequest_builder{
				Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
				Secret: row.GetSecret(),
			}.Build())
		x.Equal(codes.Unimplemented, status.Code(err), "a delegation was written with a caller's own verifier")
	})
}

// TestADelegationAloneIsWorthNothing is the condition D21 and D23 both put on
// it -- *bound to the caller it was issued to* -- on the path that actually
// uses one.
//
// The first version of this could not hold it. A credential in `authorization`
// arrives alone: `auth.TokenStore.Lookup` is handed the token and nothing else,
// so there was nothing to compare an issuer against, and the binding was
// checked only in `Introspect` -- where an app asks *about* a token rather than
// spends one. Anything that came by the string could spend it, for its whole
// life, as the person.
//
// So a delegation is not a bearer credential. The app goes on authenticating as
// itself and says who it is acting for in a header beside it, which is what
// gives the comparison something to compare.
func TestADelegationAloneIsWorthNothing(t *testing.T) {
	b := keyFor(t, verify)
	ctx := t.Context()

	const listHolders = "/roster.HolderService/List"
	mayList(t, ctx, b, b.Who, listHolders)

	mine := delegates(t, ctx, b, b.Who, []string{listHolders}, 0)

	c := app.NewHolderServiceClient(b.Conn)
	list := func(c2 context.Context) error {
		_, err := c.List(c2, app.HolderListRequest_builder{}.Build())

		return err
	}

	t.Run("with the key it was minted for, it is the person", func(t *testing.T) {
		x := require.New(t)

		x.NoError(list(acting(ctx, b.Token, mine)))
	})

	t.Run("presented on its own, it is nobody", func(t *testing.T) {
		x := require.New(t)

		x.Equal(codes.Unauthenticated, status.Code(list(bearing(ctx, mine))),
			"a delegation authenticated a caller that never said who it was")
	})

	t.Run("and with no key beside it, it is nobody", func(t *testing.T) {
		x := require.New(t)

		bare := metadata.AppendToOutgoingContext(ctx, keys.HeaderActing, mine)
		x.Equal(codes.Unauthenticated, status.Code(list(bare)))
	})

	// The one the whole header exists for: a second app on the same deployment,
	// holding its own perfectly good key, presenting a delegation it was not
	// given.
	t.Run("and another app's key does not spend it", func(t *testing.T) {
		x := require.New(t)

		theirs := keyed(t, ctx, b, "other-app", []string{listHolders})

		x.Equal(codes.Unauthenticated, status.Code(list(acting(ctx, theirs, mine))),
			"one app spent what another was issued")
	})

	// And the app's key on its own still works, so the three refusals above are
	// about the pairing rather than about the chain being broken.
	t.Run("and the key on its own is still a caller", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewVouchServiceClient(b.Conn).Verify(bearing(ctx, b.Token),
			app.VouchVerifyRequest_builder{
				Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
				Secret: []byte("whatever"),
			}.Build())
		x.NoError(err, "the app was refused for its own call")
	})
}

// keyed mints a second deployment key, for the app that is not meant to have
// what it is holding.
func keyed(t *testing.T, ctx context.Context, b *keyedBuilt, alias string, methods []string) string {
	t.Helper()
	x := require.New(t)

	who := addHolder(t, ctx, b.Control, controlTenantOf(t, ctx, b), alias)

	token, sum, err := keys.Mint(keys.PrefixDeployment)
	x.NoError(err)

	_, err = b.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:   alias,
		Secret:  sum,
		Methods: methods,
	}.Build())
	x.NoError(err)

	return token
}

// signsIn is what a product app does: one call that proves the person and
// answers with the credential to act for them.
func signsIn(t *testing.T, ctx context.Context, b *keyedBuilt, who pdid.Id, secret string, methods []string) *app.VouchDelegateResponse {
	t.Helper()

	res, err := app.NewVouchServiceClient(b.Conn).Delegate(bearing(ctx, b.Token),
		app.VouchDelegateRequest_builder{
			Who:     app.VouchWho_builder{Id: who.Bytes()}.Build(),
			Secret:  []byte(secret),
			Methods: methods,
		}.Build())
	require.NoError(t, err)

	return res
}

// TestDelegateIsVerifyAndOneMoreThing is D23's "the answer rides back with the
// yes", as one call.
//
// It is a method of its own rather than a field on `Verify` for the reason D26
// gave one entry earlier: a role is a list of methods, so what a deployment can
// grant is exactly what it can name -- and a Login App that checks passwords
// and must never mint is a different grant from a product app that needs the
// token.
func TestDelegateIsVerifyAndOneMoreThing(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, delegate, listHolders)
	ctx := t.Context()

	mayList(t, ctx, b, b.Who, listHolders)

	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	t.Run("a yes carries a credential for the person it proved", func(t *testing.T) {
		x := require.New(t)

		res := signsIn(t, ctx, b, b.Who, "correct horse battery staple", []string{listHolders})
		x.True(res.GetVerified().GetOk())
		x.Equal(b.Who.Bytes(), res.GetVerified().GetHolder())
		x.NotEmpty(res.GetToken())
		x.NotNil(res.GetExpires(), "a delegation that never expires is not one")

		_, err := app.NewHolderServiceClient(b.Conn).List(
			acting(ctx, b.Token, res.GetToken()), app.HolderListRequest_builder{}.Build())
		x.NoError(err, "the token the sign-in answered with did not work")
	})

	t.Run("and a no carries nothing at all", func(t *testing.T) {
		x := require.New(t)

		res := signsIn(t, ctx, b, b.Who, "wrong", []string{listHolders})
		x.False(res.GetVerified().GetOk())
		x.Empty(res.GetToken(), "a refusal handed out a credential")
	})

	t.Run("and a delegation that allows nothing is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewVouchServiceClient(b.Conn).Delegate(bearing(ctx, b.Token),
			app.VouchDelegateRequest_builder{
				Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
				Secret: []byte("correct horse battery staple"),
			}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
	})
}

// TestOverAskingIsRefusedBeforeThePasswordIsCompared is the check's position
// being the design rather than an implementation detail.
//
// D14 made every refusal cost the same, so that a response cannot answer *does
// this account exist*. A caller that asks for more than it holds gets refused
// either way -- but if that refusal ran **after** the comparison it would come
// back as `PermissionDenied` for a right password and `ok:false` for a wrong
// one, which is the same oracle as a timing difference and exact rather than
// statistical.
func TestOverAskingIsRefusedBeforeThePasswordIsCompared(t *testing.T) {
	x := require.New(t)

	// A key that may sign people in and may not erase anybody.
	b := keyFor(t, delegate)
	ctx := t.Context()

	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	over := func(secret string) error {
		_, err := app.NewVouchServiceClient(b.Conn).Delegate(bearing(ctx, b.Token),
			app.VouchDelegateRequest_builder{
				Who:     app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
				Secret:  []byte(secret),
				Methods: []string{"/roster.HolderService/Erase"},
			}.Build())

		return err
	}

	right, wrong := over("correct horse battery staple"), over("wrong")

	x.Equal(codes.PermissionDenied, status.Code(right),
		"an app minted a method its own key does not carry")
	x.Equal(status.Code(right), status.Code(wrong),
		"over-asking answered differently for a right password and a wrong one")

	// And a stranger is the same answer again, so the two above are about the
	// request rather than about the person.
	_, err = app.NewVouchServiceClient(b.Conn).Delegate(bearing(ctx, b.Token),
		app.VouchDelegateRequest_builder{
			Who:     app.VouchWho_builder{Tenant: "contoso", Alias: "nobody-at-all"}.Build(),
			Secret:  []byte("whatever"),
			Methods: []string{"/roster.HolderService/Erase"},
		}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
}

// TestRevokeIsTheDeleteD23PromisedAndDidNotHave.
//
// Without it, signing out of an app left that app holding a credential that
// went on working: the generated service is unregistered and closed, and
// `HolderService/Invalidate` is the wrong instrument -- it voids every
// delegation the person has and touches nobody's session.
func TestRevokeIsTheDeleteD23PromisedAndDidNotHave(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, delegate, listHolders, revoke)
	ctx := t.Context()

	mayList(t, ctx, b, b.Who, listHolders)

	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	res := signsIn(t, ctx, b, b.Who, "correct horse battery staple", []string{listHolders})
	token := res.GetToken()

	list := func() error {
		_, err := app.NewHolderServiceClient(b.Conn).List(
			acting(ctx, b.Token, token), app.HolderListRequest_builder{}.Build())

		return err
	}
	x.NoError(list(), "the control")

	c := app.NewDelegationServiceClient(b.Conn)

	// A second app revoking it changes nothing, and is not told so -- every
	// answer here is the same answer, for the reason `Erase` gives.
	t.Run("another app's revoke does nothing and says nothing", func(t *testing.T) {
		x := require.New(t)

		theirs := keyed(t, ctx, b, "someone-else", []string{revoke})

		_, err := c.Revoke(bearing(ctx, theirs), app.DelegationRevokeRequest_builder{Token: token}.Build())
		x.NoError(err, "and the answer says nothing about whose it was")
		x.NoError(list(), "one app revoked what another was issued")
	})

	t.Run("and the app it was issued to ends it", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Revoke(bearing(ctx, b.Token), app.DelegationRevokeRequest_builder{Token: token}.Build())
		x.NoError(err)

		x.Equal(codes.Unauthenticated, status.Code(list()), "a revoked delegation went on working")
	})

	t.Run("and revoking what is gone succeeds", func(t *testing.T) {
		x := require.New(t)

		_, err := c.Revoke(bearing(ctx, b.Token), app.DelegationRevokeRequest_builder{Token: token}.Build())
		x.NoError(err)

		_, err = c.Revoke(bearing(ctx, b.Token),
			app.DelegationRevokeRequest_builder{Token: "rd_nothing-at-all"}.Build())
		x.NoError(err, "a token that was never here told the caller it was never here")
	})
}

// TestExpiredDelegationsAreCollected is the other half of the sentence the
// design wrote down: *expiry is enforced on read, never by a sweep -- a sweep
// that is the mechanism is a sweep whose outage is a security incident.*
//
// The read was built and the collector was not, so the table grew by one row
// per sign-in and nothing ever removed one. Nothing waits on this and no
// refusal depends on it; what it stops is a credential table that is a log.
func TestExpiredDelegationsAreCollected(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	live := wrote(t, ctx, b, b.Who, []string{listHolders},
		timestamppb.New(time.Now().Add(time.Hour)))
	dead := wrote(t, ctx, b, b.Who, []string{listHolders},
		timestamppb.New(time.Now().Add(-time.Minute)))

	n, err := b.Ent.Delegation.Query().Count(ctx)
	x.NoError(err)
	x.Equal(2, n)

	gone, err := keys.Collect(ctx, b.Ent)
	x.NoError(err)
	x.Equal(1, gone)

	// Hard, not erased: a soft erase would leave the row, which is the whole
	// thing this exists to stop. What it costs is that a trail naming an
	// expired delegation resolves to nothing, which `Tenant` already pays.
	n, err = b.Ent.Delegation.Query().Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "the live one was collected too")

	// And it is the live one that is left, rather than one of two.
	left, err := b.Ent.Delegation.Query().Only(ctx)
	x.NoError(err)
	x.True(left.DateExpires.After(time.Now()))

	_, _ = dead, live
}
