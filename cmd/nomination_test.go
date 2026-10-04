package cmd_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/pd"
)

// TestADeploymentKeyIsNarrowedToWhoATenantNominatedForIt is #36, as #73 left it.
//
// One Login App instance fronting many tenants holds one credential, and the
// roster-hosted shape is an `rk_`: it resolves to a frame with no tenant, the
// policy hands it `frame.Everything`, and what keeps contoso's request out of
// fabrikam's rows is the app's own code. `docs/login.md` refuses an `rk_` front
// door for exactly that.
//
// A request that says which name it arrived at is answered instead as the
// holder that name's tenant nominated **for this key** -- the name chooses the
// tenant, and the `Nomination` chooses who.
func TestADeploymentKeyIsNarrowedToWhoATenantNominatedForIt(t *testing.T) {
	x := require.New(t)

	b := keyFor(t, "/roster.*/*")
	ctx := t.Context()

	// The tenant's own: a name it answers at, and the holder it nominates for
	// this key's service.
	answersAt(t, b.Server, b.Contoso, "contoso.example")
	nominates(t, b.Server, b.Contoso, b.Service, b.Who)

	// What the nominated holder may do, which is what the frame becomes: their
	// bindings, and nothing the key brought with it.
	r, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "fronts",
		Methods: []string{"/roster.MeService/Get", "/roster.HolderService/List"},
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// A second tenant, so that "every tenant" and "one tenant" are different
	// answers rather than the same one.
	fabrikam := add(t, ctx, b.Server, "fabrikam")
	_ = addHolder(t, ctx, b.Server, fabrikam, "somebody")

	me := app.NewMeServiceClient(b.Conn)
	holders := app.NewHolderServiceClient(b.Conn)

	bearer := metadata.Pairs("authorization", "Bearer "+b.Token)
	wide := metadata.NewOutgoingContext(ctx, bearer)
	at := arrivedAt(ctx, b.Token, "contoso.example")

	// Without the header: the key, seeing every tenant -- which is the state
	// this exists to replace.
	vs, err := holders.List(wide, app.HolderListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 4, "a deployment key saw fewer tenants than every one")

	t.Run("and with it, the holder that tenant nominated", func(t *testing.T) {
		x := require.New(t)

		v, err := me.Get(at, app.MeGetRequest_builder{}.Build())
		x.NoError(err, "the nomination did not answer")
		x.Equal(b.Who.Bytes(), v.GetId())
		x.Equal(b.Contoso.Bytes(), v.GetTenant())
	})

	t.Run("and the wall narrows what it reads", func(t *testing.T) {
		x := require.New(t)

		vs, err := holders.List(at, app.HolderListRequest_builder{}.Build())
		x.NoError(err)

		for _, h := range vs.GetItems() {
			x.Equal(b.Contoso.Bytes(), h.GetTenant().GetId(),
				"a narrowed call read a row from another tenant")
		}
		x.NotEmpty(vs.GetItems())
	})

	// The key could read every tenant a moment ago, and what it holds here is
	// one holder's bindings.
	t.Run("and it is the holder's bindings rather than the key's methods", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewTenantServiceClient(b.Conn).Add(at,
			app.TenantAddRequest_builder{Alias: "newco"}.Build())
		x.Error(err, "a narrowed call did something the holder it borrowed cannot")
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	// Refused rather than answered as the key, which would hand back the wide
	// frame the caller was trying to narrow -- silently.
	t.Run("and a name nothing claims is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := me.Get(arrivedAt(ctx, b.Token, "nobody.example"), app.MeGetRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	t.Run("and a tenant that nominated nobody for this key is refused", func(t *testing.T) {
		x := require.New(t)

		answersAt(t, b.Server, fabrikam, "fabrikam.example")

		_, err := me.Get(arrivedAt(ctx, b.Token, "fabrikam.example"), app.MeGetRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})
}

// TestAKeyNobodyNominatedBorrowsNothing is #73.
//
// The nomination was a field on the `Host`, and `keys.At` checked that the
// token was a deployment key and that it existed -- not **which** one. So any
// `rk_` naming that host was answered as whoever it nominated, with that
// holder's bindings and not the key's own methods: a key minted for one read
// came out holding whatever the Login App's holder was granted.
//
// It is found by the key's holder now, so a second app's key reaches only what
// a tenant nominated for that app.
func TestAKeyNobodyNominatedBorrowsNothing(t *testing.T) {
	x := require.New(t)

	b := keyFor(t, app.TenantService_Get_FullMethodName)
	ctx := t.Context()

	answersAt(t, b.Server, b.Contoso, "contoso.example")
	nominates(t, b.Server, b.Contoso, b.Service, b.Who)

	// A second service of the operator's, with a key of its own and as narrow,
	// that nobody nominated for anything.
	_, token := serviceKey(t, b.Server, "other", app.TenantService_Get_FullMethodName)

	me := app.NewMeServiceClient(b.Conn)

	_, err := me.Get(arrivedAt(ctx, b.Token, "contoso.example"), app.MeGetRequest_builder{}.Build())
	x.NoError(err, "the key that was nominated was refused")

	_, err = me.Get(arrivedAt(ctx, token, "contoso.example"), app.MeGetRequest_builder{}.Build())
	x.Error(err, "a key nobody nominated borrowed the holder another key was nominated")
	x.Equal(codes.Unauthenticated, status.Code(err))
}

// TestTwoAppsArriveAtOneNameAsTwoHolders is why the nomination left the `Host`.
//
// The Login App narrows to the host of a product's redirect, which is the name
// that product is served at -- and a product run for many tenants narrows to
// that same name for its own calls. One field on the name could nominate one of
// them, and `roster login provision` rewrote it to its own holder on every
// start.
func TestTwoAppsArriveAtOneNameAsTwoHolders(t *testing.T) {
	x := require.New(t)

	b := keyFor(t, app.TenantService_Get_FullMethodName)
	ctx := t.Context()

	answersAt(t, b.Server, b.Contoso, "contoso.example")

	product, token := serviceKey(t, b.Server, "product", app.TenantService_Get_FullMethodName)
	itsHolder := addHolder(t, ctx, b.Server, b.Contoso, "product")

	nominates(t, b.Server, b.Contoso, b.Service, b.Who)
	nominates(t, b.Server, b.Contoso, product, itsHolder)

	me := app.NewMeServiceClient(b.Conn)

	v, err := me.Get(arrivedAt(ctx, b.Token, "contoso.example"), app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(b.Who.Bytes(), v.GetId(), "the first app was answered as somebody else")

	v, err = me.Get(arrivedAt(ctx, token, "contoso.example"), app.MeGetRequest_builder{}.Build())
	x.NoError(err)
	x.Equal(itsHolder.Bytes(), v.GetId(), "the second app was answered as somebody else")

	t.Run("and one app is nominated once per tenant", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Nomination().Add(ctx, app.NominationAddRequest_builder{
			Tenant:     app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			BorrowerId: product.Bytes(),
			ActsAs:     app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		}.Build())
		x.Error(err, "a second answer to who this app is here was written")
		x.Equal(codes.AlreadyExists, status.Code(err))
	})
}

// TestANominationNamesOnlyItsOwnTenantsHolder is the agreement the nomination
// needs, and the shape `agree.go` states for every other row that names two
// tenants: one that named somebody else's would hand a caller a frame in a
// tenant that never agreed to it.
func TestANominationNamesOnlyItsOwnTenantsHolder(t *testing.T) {
	x := require.New(t)

	b, ctx := build(t)
	app1 := pdid.New(pd.HolderDomain)

	_, err := b.Ungated.Nomination().Add(ctx, app.NominationAddRequest_builder{
		Tenant:     app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(),
		BorrowerId: app1.Bytes(),

		// Contoso's.
		ActsAs: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
	}.Build())
	x.Error(err, "a tenant nominated another tenant's holder")
	x.Equal(codes.InvalidArgument, status.Code(err))

	t.Run("and its own is written", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Fabrikam, "front")
		v, err := b.Ungated.Nomination().Add(ctx, app.NominationAddRequest_builder{
			Tenant:     app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(),
			BorrowerId: app1.Bytes(),
			ActsAs:     app.HolderRef_builder{Id: who.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		got, err := b.Ungated.Nomination().Get(ctx, app.NominationGetRequest_builder{
			Ref:    app.NominationRef_builder{Id: v.GetId()}.Build(),
			Select: app.NominationSelect_builder{ActsAs: app.HolderSelect_builder{}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal(who.Bytes(), got.GetActsAs().GetId())
	})

	t.Run("and a nomination for no app is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Nomination().Add(ctx, app.NominationAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(),
			ActsAs: app.HolderRef_builder{Id: b.holder(t, ctx, b.Fabrikam, "front").Bytes()}.Build(),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
	})
}

// TestNobodyNominatesAHolderWiderThanThemselves.
//
// A narrowed key is answered with the nominated holder's bindings, so a
// nomination is a way to act as that holder, held by whoever holds the key.
// `Host.acts_as` was argued to need no rule -- *borrowing is strictly less* --
// which is true of tenants and not of methods:
//
//	Alice may call Nomination.Add and Holder.Get, and nothing else.
//	Alice nominates the tenant's owner for the app's key.
//	Everything the app does in her tenant is done as the owner.
//
// Refused on the terms a key or a credential is: nobody writes a way into an
// account wider than their own.
func TestNobodyNominatesAHolderWiderThanThemselves(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const nominate = "/roster.NominationService/Add"
	borrower := pdid.New(pd.HolderDomain)

	// Alice administers nominations and can read people.
	b.binds(t, b.ContosoUser, b.role(t, ctx, "nominator", nominate, getHolder), nil)

	// The owner, who holds everything.
	owner := b.holder(t, ctx, b.Contoso, "owner")
	b.binds(t, owner, b.role(t, ctx, "owner", "/roster.*/*"), nil)

	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)
	nominations := app.NewNominationServiceClient(conn)

	_, err := nominations.Add(wire, app.NominationAddRequest_builder{
		Tenant:     app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		BorrowerId: borrower.Bytes(),
		ActsAs:     app.HolderRef_builder{Id: owner.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"she nominated somebody wider than herself for a key she does not hold")

	t.Run("and the app's own holder, no wider than she is, is hers to nominate", func(t *testing.T) {
		x := require.New(t)

		front := b.holder(t, ctx, b.Contoso, "front")
		_, err := nominations.Add(wire, app.NominationAddRequest_builder{
			Tenant:     app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			BorrowerId: borrower.Bytes(),
			ActsAs:     app.HolderRef_builder{Id: front.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
	})
}

// nominates says who `borrower`'s keys are answered as in `tenant`.
func nominates(t *testing.T, s *cmd.Server, tenant, borrower, who pdid.Id) {
	t.Helper()

	_, err := s.Ungated.Nomination().Add(t.Context(), app.NominationAddRequest_builder{
		Tenant:     app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
		BorrowerId: borrower.Bytes(),
		ActsAs:     app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)
}

// serviceKey is another service of the roster operator's, in the control plane,
// with a deployment key allowing `methods`.
func serviceKey(t *testing.T, s *cmd.Server, alias string, methods ...string) (pdid.Id, string) {
	t.Helper()
	x := require.New(t)

	who, err := cmd.HolderNamed(t.Context(), s.Control, alias)
	x.NoError(err)

	token, sum, err := keys.Mint(keys.PrefixDeployment)
	x.NoError(err)
	_, err = s.Control.Ungated.ApiKey().Add(t.Context(), app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:   alias,
		Secret:  sum,
		Methods: methods,
	}.Build())
	x.NoError(err)

	return who, token
}

// arrivedAt is a call carrying `token` that says it arrived at `name`.
func arrivedAt(ctx context.Context, token, name string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"authorization", "Bearer "+token,
		keys.HeaderAt, name,
	))
}

// TestANarrowedKeyDoesNothingUntilARequestNamesATenant is #74's second half.
//
// A product that always knows which tenant a request is about makes no call
// across every tenant, so the honest key for it allows nothing unnarrowed --
// and `--allow` refused that, so it had to name something, and every request
// that forgot `roster-at` ran that something across the whole deployment.
// `--narrowed` mints the key with nothing on it: refused as itself, answered as
// whoever each tenant nominated.
func TestANarrowedKeyDoesNothingUntilARequestNamesATenant(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	token := stdoutOf(t, cli.NewCmdControl(&c), "key", "add", "--narrowed", "product")
	x.True(strings.HasPrefix(token, keys.PrefixDeployment), "not a deployment key: %.3q", token)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	contoso := add(t, ctx, s, "contoso")
	who := addHolder(t, ctx, s, contoso, "product")
	borrower, err := cmd.HolderNamed(ctx, s.Control, "product")
	x.NoError(err)
	answersAt(t, s, contoso, "contoso.example")
	nominates(t, s, contoso, borrower, who)

	me := app.NewMeServiceClient(served(t, s))

	_, err = me.Get(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)),
		app.MeGetRequest_builder{}.Build())
	x.Error(err, "a narrowed key did something without naming a tenant")
	x.Equal(codes.PermissionDenied, status.Code(err))

	v, err := me.Get(arrivedAt(ctx, token, "contoso.example"), app.MeGetRequest_builder{}.Build())
	x.NoError(err, "a narrowed key was refused where a tenant nominated somebody for it")
	x.Equal(who.Bytes(), v.GetId())

	t.Run("and a forgotten --allow is still the refusal it was", func(t *testing.T) {
		err := cli.NewCmdControl(&c).Run(t.Context(), []string{"key", "add", "forgot"})
		require.ErrorContains(t, err, "--allow")
	})
}

// TestATenantNamedDirectlyIsNarrowedTheSameWay is #76's roster half.
//
// A name a tenant answers at only ever chose the tenant; the nomination is what
// consents. So a caller with no name to give -- a directory resolving its tenant
// from a DN -- says the tenant itself, `@contoso` or `@<identifier>`, and is
// answered exactly as a name would have had it answered.
func TestATenantNamedDirectlyIsNarrowedTheSameWay(t *testing.T) {
	x := require.New(t)

	b := keyFor(t, app.TenantService_Get_FullMethodName)
	ctx := t.Context()

	nominates(t, b.Server, b.Contoso, b.Service, b.Who)
	me := app.NewMeServiceClient(b.Conn)

	for _, at := range []string{front.AtTenant("contoso"), front.AtTenant("CONTOSO"), front.AtTenant(b.Contoso.String())} {
		v, err := me.Get(arrivedAt(ctx, b.Token, at), app.MeGetRequest_builder{}.Build())
		x.NoError(err, "%s was not answered as the nominated holder", at)
		x.Equal(b.Who.Bytes(), v.GetId(), at)
	}

	t.Run("and a tenant that nominated nobody for the key is refused", func(t *testing.T) {
		x := require.New(t)

		_ = add(t, ctx, b.Server, "fabrikam")
		_, err := me.Get(arrivedAt(ctx, b.Token, front.AtTenant("fabrikam")), app.MeGetRequest_builder{}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	t.Run("and so are a tenant that does not exist, and none at all", func(t *testing.T) {
		x := require.New(t)

		for _, at := range []string{front.AtTenant("nobody"), front.TenantMark} {
			_, err := me.Get(arrivedAt(ctx, b.Token, at), app.MeGetRequest_builder{}.Build())
			x.Equal(codes.Unauthenticated, status.Code(err), "%q", at)
		}
	})
}

// TestAKeyReadsItsOwnNominationsAndNobodyElses is how an app run for many
// tenants learns which ones it acts in.
//
// It has to ask before it can name a tenant, so it asks unnarrowed -- and an
// unnarrowed deployment key is every tenant, which would show it every other
// app's nominations too. What it is owed is its own, and the layer holds a key
// to them: the same `List`, `Get` and `Watch`, answering about the caller's own
// rows.
func TestAKeyReadsItsOwnNominationsAndNobodyElses(t *testing.T) {
	x := require.New(t)

	b := keyFor(t, "/roster.NominationService/*")
	ctx := t.Context()

	fabrikam := add(t, ctx, b.Server, "fabrikam")
	nominates(t, b.Server, b.Contoso, b.Service, b.Who)
	nominates(t, b.Server, fabrikam, b.Service, addHolder(t, ctx, b.Server, fabrikam, "front"))

	// Another app, in contoso.
	other, _ := serviceKey(t, b.Server, "other", app.TenantService_Get_FullMethodName)
	theirs, err := b.Ungated.Nomination().Add(ctx, app.NominationAddRequest_builder{
		Tenant:     app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		BorrowerId: other.Bytes(),
		ActsAs:     app.HolderRef_builder{Id: addHolder(t, ctx, b.Server, b.Contoso, "other").Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	nominations := app.NewNominationServiceClient(b.Conn)
	wide := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+b.Token))

	vs, err := nominations.List(wide, app.NominationListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 2, "a key saw other than its own two nominations")
	tenants := [][]byte{}
	for _, v := range vs.GetItems() {
		x.Equal(b.Service.Bytes(), v.GetBorrowerId())
		tenants = append(tenants, v.GetTenant().GetId())
	}
	x.ElementsMatch([][]byte{b.Contoso.Bytes(), fabrikam.Bytes()}, tenants)

	t.Run("and a filter naming another app's is refused", func(t *testing.T) {
		_, err := nominations.List(wide, app.NominationListRequest_builder{
			Filters: []*app.NominationFilter{app.NominationFilter_builder{BorrowerId: other.Bytes()}.Build()},
		}.Build())
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("and another app's is not there to get", func(t *testing.T) {
		_, err := nominations.Get(wide, app.NominationGetRequest_builder{
			Ref: app.NominationRef_builder{Id: theirs.GetId()}.Build(),
		}.Build())
		require.Equal(t, codes.NotFound, status.Code(err))
	})

	t.Run("and it polls rather than watches", func(t *testing.T) {
		st, err := nominations.Watch(wide, app.NominationWatchRequest_builder{
			Filters: []*app.NominationFilter{app.NominationFilter_builder{
				Ref: app.NominationRef_builder{Id: theirs.GetId()}.Build(),
			}.Build()},
		}.Build())
		require.NoError(t, err)

		_, err = st.Recv()
		require.Equal(t, codes.Unimplemented, status.Code(err), "a key watched a nomination, which a watch by reference could not hold to its own")
	})
}

// TestARosterAtThatNamesNothingIsRefused is a header that is there and names
// no one place.
//
// It was read as no header at all, and for an unnarrowed key no header is every
// tenant: `roster-at: :443` was answered wider than a request that sent nothing
// would have been, and wider than the caller meant to be. A narrowed key was
// never at risk -- it allows nothing as itself -- so the key here is one with a
// method of its own.
func TestARosterAtThatNamesNothingIsRefused(t *testing.T) {
	b := keyFor(t, app.HolderService_List_FullMethodName)
	ctx := t.Context()

	answersAt(t, b.Server, b.Contoso, "contoso.example")
	nominates(t, b.Server, b.Contoso, b.Service, b.Who)
	permits(t, ctx, b, b.Contoso, b.Who, "fronts", app.HolderService_List_FullMethodName)

	holders := app.NewHolderServiceClient(b.Conn)
	list := func(ats ...string) error {
		kv := []string{"authorization", "Bearer " + b.Token}
		for _, a := range ats {
			kv = append(kv, keys.HeaderAt, a)
		}
		_, err := holders.List(metadata.NewOutgoingContext(ctx, metadata.Pairs(kv...)), app.HolderListRequest_builder{}.Build())

		return err
	}

	for _, ats := range [][]string{
		{""},
		{" "},
		{":443"},
		{"[]"},
		{"contoso.example", " "},
		{"contoso.example", ":443"},
		{"contoso.example", "fabrikam.example"},
	} {
		require.Equal(t, codes.Unauthenticated, status.Code(list(ats...)), "roster-at %q was answered", ats)
	}

	require.NoError(t, list("contoso.example"), "one name, once")
	require.NoError(t, list("contoso.example", "CONTOSO.EXAMPLE:443"), "one name, said twice")
	require.NoError(t, list(), "no header is the key as itself")
}

// TestADeploymentKeyAsItselfWritesNoNomination is a key allowed `Add`, unnarrowed.
//
// It is every tenant held to its methods, and a nomination is a way to act as
// somebody: one such key wrote a nomination anywhere, for its own holder, as
// whoever there held nothing -- whom the reach rule lets anybody name.
func TestADeploymentKeyAsItselfWritesNoNomination(t *testing.T) {
	b := keyFor(t, app.NominationService_Add_FullMethodName, app.NominationService_Erase_FullMethodName)
	ctx := t.Context()

	nobody := addHolder(t, ctx, b.Server, b.Contoso, "nobody")
	_, err := app.NewNominationServiceClient(b.Conn).Add(bearing(ctx, b.Token), app.NominationAddRequest_builder{
		Tenant:     app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		BorrowerId: b.Service.Bytes(),
		ActsAs:     app.HolderRef_builder{Id: nobody.Bytes()}.Build(),
	}.Build())
	require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
}
