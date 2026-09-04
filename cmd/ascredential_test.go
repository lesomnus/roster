package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// Every credential roster takes resolves to somebody, and this file is the one
// question asked of each of them: **is it the holder's to use?**
//
// A key, a delegation, a link, a continuation and a console cookie are five
// different rows with five different lifetimes, and what they have in common is
// that presenting one makes roster answer as a person. So the failure they
// share is one failure -- a caller spends a credential that resolves to
// somebody who is not them, and from that moment every read is walled, gated
// and audited *as the wrong person*. Nothing downstream can notice: the wall
// narrows correctly, the policy answers correctly, the trail names the person
// the credential named. There is no second opinion after this point, which is
// why the refusals live here and why they are worth a file of their own.
//
// What is already pinned elsewhere and deliberately not repeated: a delegation
// presented with no key at all, and one presented by a wholly different app
// (`TestADelegationAloneIsWorthNothing`); an attempt continued by another app
// (`TestAnAttemptBelongsToWhoeverOpenedIt`); a link redeemed by another app
// (`TestALinkIsAWayInThatSomebodyElseDelivers`); a key of one kind wearing the
// other's prefix (`TestThePrefixDecidesWhichDatabase`); a revoked or expired key
// (`TestRevokingAKeyStopsItAtOnce`, `TestAnExpiredKeyIsRefused`).

// permits binds a role of the named tenant to somebody.
//
// `mayList` is the same thing for one method in contoso; this takes the tenant and
// the list, because half of what is being measured below is a caller in one
// tenant reaching into another, and a helper that can only write contoso's roles
// cannot set that up.
func permits(t *testing.T, ctx context.Context, b *keyedBuilt, in, who pdid.Id, alias string, methods ...string) {
	t.Helper()
	x := require.New(t)

	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:   alias,
		Methods: methods,
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
}

// serviceBehind is the control plane holder a deployment key hangs off.
//
// `issuerOf` answers the other identifier on the same row -- the **key**, which
// is what a delegation is stamped with. Both are needed below, and the whole of
// that test is that they are not the same thing.
func serviceBehind(t *testing.T, ctx context.Context, b *keyedBuilt, token string) pdid.Id {
	t.Helper()
	x := require.New(t)

	v, err := b.Control.Ungated.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
		Ref: app.ApiKeyRef_builder{Secret: keys.Sum(token)}.Build(),
		Select: app.ApiKeySelect_builder{
			Holder: app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	x.NoError(err)

	return mustId(t, v.GetHolder().GetId())
}

// rekeyed is what rotating an app's credential is: a second key on the **same**
// service, allowing the same methods, replacing nothing.
func rekeyed(t *testing.T, ctx context.Context, b *keyedBuilt, svc pdid.Id, alias string, methods ...string) string {
	t.Helper()
	x := require.New(t)

	token, sum, err := keys.Mint(keys.PrefixDeployment)
	x.NoError(err)

	_, err = b.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: svc.Bytes()}.Build(),
		Alias:   alias,
		Secret:  sum,
		Methods: methods,
	}.Build())
	x.NoError(err)

	return token
}

// TestNobodyMintsAWayIntoAnotherTenant was **red about the app and not about
// the test**, and holds the fix: `Link` now resolves through `s.walled`
// (link.go says so beside the call). What follows is the hole as it stood,
// kept because the shape of it -- a credential write that resolves its subject
// through the open server -- is the shape to check any new one against.
//
// `VouchService.Link` mints a single-use way into an account and hands the
// token straight back to whoever asked. What it did not do is ask whether the
// caller had any business naming that account: it resolved the person through
// `s.open`, the server the wall was never installed on, and wrote the row.
// Every other credential write in this service goes through `s.walled` -- `Set`,
// `Reset`, `Unlock`, `Enrol` are all on the other side of that line, and the
// package comment says why in as many words: *an administrator of one tenant
// cannot reach into another, and that narrowing is the generated one.* `Link` is
// the one that is not, and it is the one that needs no secret to spend.
//
// So the whole of it is two calls. Somebody in contoso who holds
// `VouchService/Link` and `VouchService/Redeem` -- two entries in one role, not
// an administrator, nothing that mentions fabrikam -- names a fabrikam person by
// tenant alias and user alias, is handed a link for them, redeems it, and is
// holding a delegation that roster answers as that person. From there
// `roster-as` reads fabrikam's rows, with fabrikam's bindings, under fabrikam's tenant,
// from a credential that belongs to contoso.
//
// The third method she holds is `HolderService/List`, which is an ordinary
// thing to be able to do inside your own tenant and is here only so that the
// last step has something to read. It grants nothing across the wall; the
// delegation is what carries her over it.
//
// What it costs: the wall between two customers who have never heard of each
// other, and it costs it without a password, without mail, without touching the
// person's credential and without anything in the trail saying the caller was
// anybody but themselves until the delegated call lands as somebody else.
//
// The `Set` half is here to say that the refusal exists and `Link` is outside
// it -- so this is a gap in one call rather than a rule nobody wrote.
func TestNobodyMintsAWayIntoAnotherTenant(t *testing.T) {
	x := require.New(t)

	// `assert` and not `require` for the chain below, which is the one place in
	// this file it is right: `require` stops at the first failure, and the first
	// failure here is the row being written -- so a reader would see that and
	// never see what the row is then worth. Every step is reported, because the
	// last one is the claim and the first one is only where it starts.
	a := assert.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	// A second customer with somebody in it, who may read their own tenant.
	fabrikam := add(t, ctx, b.Server, "fabrikam")
	erlich := addHolder(t, ctx, b.Server, fabrikam, "erlich")
	permits(t, ctx, b, fabrikam, erlich, "fabrikam-reader", listHolders)

	// And somebody in contoso with those two methods, plus the ordinary right to
	// read her own tenant. Deliberately not the tenant's administrator: what is
	// being measured is the smallest standing that reaches, and it turns out to
	// be two entries in one role.
	permits(t, ctx, b, b.Contoso, b.Who, "front-door", link, redeem, listHolders)
	hers := mintFor(t, ctx, b, b.Who, "her-login-app",
		[]string{link, redeem, listHolders}, time.Time{})

	c := app.NewVouchServiceClient(b.Conn)
	as := bearing(ctx, hers)

	// The refusal that does exist, on the write that goes through the wall. It
	// is here so that this reads as a gap in one call rather than as a rule
	// nobody wrote.
	t.Run("her tenant's wall stops her writing his password", func(t *testing.T) {
		x := require.New(t)

		permits(t, ctx, b, b.Contoso, b.Who, "setter", "/roster.CredentialService/Set")

		_, err := app.NewCredentialServiceClient(b.Conn).Set(
			bearing(ctx, mintFor(t, ctx, b, b.Who, "setter-key",
				[]string{"/roster.CredentialService/Set"}, time.Time{})),
			app.CredentialSetRequest_builder{
				Ref: app.HolderRef_builder{
					Slug: app.HolderRefBySlug_builder{
						Tenant: app.TenantRef_builder{Alias: z.Ptr("fabrikam")}.Build(),
						Alias:  z.Ptr("erlich"),
					}.Build(),
				}.Build(),
				Secret: []byte("correct horse battery staple"),
			}.Build())
		x.Error(err, "an contoso caller wrote a fabrikam password")
	})

	made, err := c.Link(as, app.VouchLinkRequest_builder{
		Who: app.VouchWho_builder{Tenant: "fabrikam", Alias: "erlich"}.Build(),
	}.Build())
	x.NoError(err, "asking is allowed to succeed -- TestAskingForALinkSaysNothingAboutWhoIsHere")

	// Asking answers a token for a stranger too, so the token proves nothing on
	// its own. The row does: a link for somebody outside the caller's tenant
	// must never have been written.
	n, err := b.Ent.Link.Query().Count(ctx)
	x.NoError(err)
	a.Zero(n, "a caller in contoso was written a way into a fabrikam account")

	res, err := c.Redeem(as, app.VouchRedeemRequest_builder{
		Token:   made.GetToken(),
		Methods: []string{listHolders},
	}.Build())
	x.NoError(err)
	a.False(res.GetVerified().GetOk(),
		"a caller in contoso signed in as somebody in fabrikam, holding nothing of theirs")
	a.Empty(res.GetToken(), "and was handed a credential for them")

	// And the end of it, which is what the two calls above are worth: roster
	// answering fabrikam's rows to an contoso credential, as a fabrikam person.
	if res.GetToken() == "" {
		return
	}

	v, err := app.NewHolderServiceClient(b.Conn).List(
		acting(ctx, hers, res.GetToken()), app.HolderListRequest_builder{}.Build())
	x.NoError(err)

	for _, h := range v.GetItems() {
		a.NotEqual("erlich", h.GetAlias(),
			"an contoso credential read fabrikam, as a fabrikam person")
	}
}

// TestADelegationThatDoesNotCheckOutIsNotTheAppInstead is the ordering rule in
// `auth.Seq` made into a refusal, and it is the difference between failing
// closed and failing **wider than the request asked for**.
//
// `keys.Acting` runs ahead of `auth.Bearer`, and it has two ways to answer no.
// `auth.ErrNoCredential` means *not my request* and `Seq` moves on to the next
// handler; a status error stops the search. A request carrying `roster-as` has
// said what it wants to be, so every way of that being wrong -- a delegation
// nobody minted, an expired one, one another app was issued -- has to be the
// second kind.
//
// Answered the first way, the request does not fail at all. It falls through to
// `Bearer`, which finds the app's own key in `authorization`, and the app is
// served **as itself**: a deployment key, which the policy hands `Everything`,
// on the very call the delegation was there to narrow down to one person. The
// app asked to be one customer and was quietly given all of them, and the only
// evidence is a trail naming the key rather than the person -- which is what
// the trail names for every un-delegated call the app makes anyway.
//
// So the assertion is not only the code. It is that the tenant the app was
// **not** acting for stays unread, because `Unauthenticated` is what a fall-
// through would not produce and reading fabrikam is what it would.
func TestADelegationThatDoesNotCheckOutIsNotTheAppInstead(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, listHolders, delegate)
	ctx := t.Context()

	fabrikam := add(t, ctx, b.Server, "fabrikam")
	addHolder(t, ctx, b.Server, fabrikam, "erlich")

	mayList(t, ctx, b, b.Who, listHolders)

	c := app.NewHolderServiceClient(b.Conn)
	list := func(c2 context.Context) (*app.HolderListResponse, error) {
		return c.List(c2, app.HolderListRequest_builder{}.Build())
	}

	// The control, and the whole reason the fall-through would matter: this key
	// is the deployment's, so on its own it reads every customer there is.
	t.Run("the key on its own is every tenant", func(t *testing.T) {
		x := require.New(t)

		v, err := list(bearing(ctx, b.Token))
		x.NoError(err)

		var aliases []string
		for _, h := range v.GetItems() {
			aliases = append(aliases, h.GetAlias())
		}
		x.Contains(aliases, "someone")
		x.Contains(aliases, "erlich", "the control: an rk_ crosses tenants")
	})

	// A real delegation, minted for a second app on this deployment. It is a
	// live row for a real person -- what is wrong with it is only whose it is.
	theirs := keyed(t, ctx, b, "another-app", []string{delegate, listHolders})

	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	signed, err := app.NewVouchServiceClient(b.Conn).Delegate(bearing(ctx, theirs),
		app.VouchDelegateRequest_builder{
			Who:     app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret:  []byte("correct horse battery staple"),
			Methods: []string{listHolders},
		}.Build())
	x.NoError(err)
	x.NotEmpty(signed.GetToken())

	for _, tc := range []struct{ desc, as string }{
		{"a delegation nobody minted", "rd_nothing-anybody-ever-wrote-down"},
		{"a string that is not one at all", "hunter2hunter2"},
		{"one another app was issued", signed.GetToken()},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			v, err := list(acting(ctx, b.Token, tc.as))

			// The consequence before the code, so that a build where this
			// refusal has gone says what it cost rather than only that a
			// number differs.
			for _, h := range v.GetItems() {
				x.NotEqual("erlich", h.GetAlias(),
					"the call a delegation was meant to narrow read every tenant")
			}

			x.Equal(codes.Unauthenticated, status.Code(err),
				"a delegation that did not check out was served as the app itself")
		})
	}
}

// TestADelegationIsBoundToTheKeyAndNotToTheAppBehindIt is the sentence
// `vouch.issuerOf` writes down and nothing held it to: *rotating an app's key
// invalidates the delegations it issued, and a caller whose credential has been
// replaced is not obviously the same caller.*
//
// It is the sharp form of the binding. `TestADelegationAloneIsWorthNothing`
// already refuses a **different app**, which leaves open the reading that what
// is compared is the service -- and under that reading a key is a password for
// the app rather than the caller's identity, and the row that got leaked goes
// on standing in for the row that replaced it.
//
// So both keys here hang off the same control plane holder, carry the same
// methods, and were minted by the same operator minutes apart. The only thing
// that differs is which row the caller arrived as, and that has to be enough:
// a key handed to a contractor, or one pasted into a build log and rotated out
// the same afternoon, is a caller that must not be able to spend the sign-ins
// its replacement performed -- nor the other way round, which is the half that
// makes the rotation worth anything.
//
// Both directions are asserted for a second reason: a build where the
// comparison is deleted outright serves them **both**, and a test that only
// checked the refusal would still be measuring one live credential against one
// dead one.
func TestADelegationIsBoundToTheKeyAndNotToTheAppBehindIt(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, delegate, listHolders)
	ctx := t.Context()

	mayList(t, ctx, b, b.Who, listHolders)

	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	svc := serviceBehind(t, ctx, b, b.Token)
	next := rekeyed(t, ctx, b, svc, "production-2", delegate, listHolders)

	// The two are the same app by every reading but the row.
	x.Equal(svc, serviceBehind(t, ctx, b, next), "the rotation made a second service")
	x.NotEqual(issuerOf(t, ctx, b), keys.Sum(next), "the two keys are one row")

	signIn := func(with string) string {
		t.Helper()

		res, err := app.NewVouchServiceClient(b.Conn).Delegate(bearing(ctx, with),
			app.VouchDelegateRequest_builder{
				Who:     app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
				Secret:  []byte("correct horse battery staple"),
				Methods: []string{listHolders},
			}.Build())
		require.NoError(t, err)
		require.NotEmpty(t, res.GetToken())

		return res.GetToken()
	}

	list := func(key, held string) error {
		_, err := app.NewHolderServiceClient(b.Conn).List(
			acting(ctx, key, held), app.HolderListRequest_builder{}.Build())

		return err
	}

	was, now := signIn(b.Token), signIn(next)

	t.Run("each key spends what it was given", func(t *testing.T) {
		x := require.New(t)

		x.NoError(list(b.Token, was))
		x.NoError(list(next, now), "the rotated key cannot spend its own sign-in")
	})

	t.Run("and neither spends the other's", func(t *testing.T) {
		x := require.New(t)

		x.Equal(codes.Unauthenticated, status.Code(list(next, was)),
			"a rotated key picked up the sign-ins the key it replaced performed")
		x.Equal(codes.Unauthenticated, status.Code(list(b.Token, now)),
			"a key that has been rotated away went on spending its replacement's sign-ins")
	})

	// And the end a rotation is actually run for: the old key is **revoked**,
	// not merely joined by a replacement. Its sign-ins must die with it -- the
	// binding above makes them unspendable by anybody else, and the erase makes
	// them unspendable by the key itself, and only the two together mean a key
	// pasted into a build log stops mattering. Fresh sign-ins through the
	// replacement have to keep working, or a rotation is an outage.
	t.Run("and revoking the key ends its sign-ins for good", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Control.Ungated.ApiKey().Erase(ctx,
			app.ApiKeyRef_builder{Secret: keys.Sum(b.Token)}.Build())
		x.NoError(err)

		x.Equal(codes.Unauthenticated, status.Code(list(b.Token, was)),
			"a revoked key went on spending its sign-ins")
		x.Equal(codes.Unauthenticated, status.Code(list(next, was)),
			"a revoked key's sign-ins moved to its replacement")

		x.NoError(list(next, signIn(next)),
			"the replacement could not sign anybody in, which makes rotation an outage")
	})
}

// TestATenantKeysDelegationIsBoundToThePersonAndNotToTheKey is the other
// resolution of the same comparison, and it comes out the opposite way round.
//
// `keys.Store` answers a deployment key **as the key row** and a tenant key
// **as its holder**, deliberately: one belongs to the deployment and the other
// is somebody calling, attenuated. `keys.Acting` makes that same switch again
// so that the two agree about who a delegation was stamped for -- and it is the
// half of `Acting` that nothing exercised, because every delegation test in the
// suite presents an `rk_`.
//
// Getting it wrong is not a refusal, it is an impersonation. Bound to the key
// row here, a person's second key would not spend their own sign-ins -- annoying
// and visible. Bound to the *holder* on the deployment side, every key of one
// service would be interchangeable, and rotation would stop meaning anything;
// that is the failure `TestADelegationIsBoundToTheKeyAndNotToTheAppBehindIt`
// covers. What this one covers is the direction nobody would look at: that
// Carol, an ordinary colleague in the same tenant, holding a key allowing the
// same two methods, is not Alice.
//
// The deployment key is here for the reason the whole file exists. It is
// strictly the most powerful credential this deployment issues -- it reads
// every customer, as `TestADelegationThatDoesNotCheckOutIsNotTheAppInstead`
// shows -- and it still cannot spend a delegation it was not given. Which is
// what says the refusal is the binding and not privilege: no amount of standing
// substitutes for being the caller it was issued to.
func TestATenantKeysDelegationIsBoundToThePersonAndNotToTheKey(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	bob := addHolder(t, ctx, b.Server, b.Contoso, "bob")
	carol := addHolder(t, ctx, b.Server, b.Contoso, "carol")

	// Bob is who gets signed in; Alice and Carol are two colleagues who both
	// run something that signs people in.
	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: bob.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	permits(t, ctx, b, b.Contoso, b.Who, "signer-alice", delegate, listHolders)
	permits(t, ctx, b, b.Contoso, carol, "signer-carol", delegate, listHolders)
	permits(t, ctx, b, b.Contoso, bob, "reader-bob", listHolders)

	allowed := []string{delegate, listHolders}
	alice := mintFor(t, ctx, b, b.Who, "alice-one", allowed, time.Time{})
	again := mintFor(t, ctx, b, b.Who, "alice-two", allowed, time.Time{})
	hers := mintFor(t, ctx, b, carol, "carol-one", allowed, time.Time{})

	res, err := app.NewVouchServiceClient(b.Conn).Delegate(bearing(ctx, alice),
		app.VouchDelegateRequest_builder{
			Who:     app.VouchWho_builder{Id: bob.Bytes()}.Build(),
			Secret:  []byte("correct horse battery staple"),
			Methods: []string{listHolders},
		}.Build())
	x.NoError(err)
	x.True(res.GetVerified().GetOk())

	held := res.GetToken()
	x.NotEmpty(held)

	list := func(key string) error {
		_, err := app.NewHolderServiceClient(b.Conn).List(
			acting(ctx, key, held), app.HolderListRequest_builder{}.Build())

		return err
	}

	t.Run("whoever it was issued to spends it, by any key of theirs", func(t *testing.T) {
		x := require.New(t)

		x.NoError(list(alice))
		x.NoError(list(again),
			"a tenant key is its holder, so a second key of theirs is the same caller")
	})

	t.Run("and a colleague with the same methods does not", func(t *testing.T) {
		x := require.New(t)

		x.Equal(codes.Unauthenticated, status.Code(list(hers)),
			"one person spent an authentication another one performed")
	})

	t.Run("and neither does the deployment's own key", func(t *testing.T) {
		x := require.New(t)

		x.Equal(codes.Unauthenticated, status.Code(list(b.Token)),
			"the widest credential this deployment issues spent one it was not given")
	})
}

// TestSigningOutReachesThePortWithNoWall is the one place where a cookie that
// should be dead is worth the most.
//
// A console session is the only credential in this deployment that is not
// bearer-shaped by design: a browser has nowhere safe to keep a secret, so what
// it holds is an opaque name for a row, and the row is the whole of what makes
// it real. `session.Store.Del` is a soft erase and `Get` narrows past it -- and
// that narrowing is *written* rather than inherited, because this store goes to
// ent directly, so nothing generated is holding it up.
//
// `TestAConsoleSessionSurvivesTheProcess` asks the store. This asks the admin
// port, which is where it matters: that port has **no wall on it** -- an
// operator has no tenant in the data plane, so the stack is built without one --
// and every customer in the deployment is reachable through it. A cookie that
// outlives its sign-out is therefore not "somebody stayed signed in"; it is a
// laptop closed in a cafe, a shared machine, or a session ended after a
// contractor left, still able to create tenants, read any customer's people and
// write their credentials, with every write recorded in the trail as the
// operator who signed out.
//
// The second half is what the port will and will not accept at all. Its chain
// is a session handler and nothing else, so a deployment key -- which outranks
// any operator on the data plane -- is not an operator here. That is not a rule
// anybody enforces per call: it is the wiring, and the wiring is the whole of
// the control.
func TestSigningOutReachesThePortWithNoWall(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)
	c := signIn(t, s, "ops", passwordFrom(t, out))
	x.NotNil(c, "the control: init printed a password that signs in")

	g, err := s.GrpcAdmin(ctx, cmd.Config{})
	x.NoError(err)
	admin := pdtest.Serve(t, g)

	gc, err := s.GrpcControl(ctx, cmd.Config{})
	x.NoError(err)
	control := pdtest.Serve(t, gc)

	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value))

	// Creating a customer, which is the port's reason to exist and needs no
	// tenant of the caller's -- there is no wall here to have one.
	_, err = app.NewTenantServiceClient(admin).Add(as,
		app.TenantAddRequest_builder{Alias: "before"}.Build())
	x.NoError(err, "the control: the cookie administers customers")

	_, err = app.NewAuthServiceClient(control).SignOut(as, &app.AuthSignOutRequest{})
	x.NoError(err)

	t.Run("the cookie is nobody the moment it is ended", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewTenantServiceClient(admin).Add(as,
			app.TenantAddRequest_builder{Alias: "after"}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err),
			"a signed-out console went on administering every customer")

		n, err := s.Ent.Tenant.Query().Count(ctx)
		x.NoError(err)
		x.Equal(1, n, "a signed-out console wrote a customer") // `before`, and nothing else
	})

	// And a key is not a way round it. `rk_` is the deployment's own credential
	// and the widest thing it issues; this port does not read `authorization`
	// at all.
	t.Run("and a deployment key is not an operator", func(t *testing.T) {
		x := require.New(t)

		svc := addHolder(t, ctx, s.Control, controlTenant(t, ctx, s), "custody")

		token, sum, err := keys.Mint(keys.PrefixDeployment)
		x.NoError(err)

		_, err = s.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: svc.Bytes()}.Build(),
			Alias:   "production",
			Secret:  sum,
			Methods: []string{"/roster.*/*"},
		}.Build())
		x.NoError(err)

		_, err = app.NewTenantServiceClient(admin).Add(bearing(ctx, token),
			app.TenantAddRequest_builder{Alias: "by-key"}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err),
			"a service key administered customers on the operators' port")
	})
}

// controlTenant is the control plane's one tenant, on a plain `cmd.Server`.
//
// `controlTenantOf` is the same read on the keyed harness; this one takes the
// server because `inited` answers with one of those and not with a
// `keyedBuilt`.
func controlTenant(t *testing.T, ctx context.Context, s *cmd.Server) pdid.Id {
	t.Helper()
	x := require.New(t)

	v, err := s.Control.Ungated.Tenant().List(ctx, app.TenantListRequest_builder{}.Build())
	x.NoError(err)
	x.NotEmpty(v.GetItems())

	return mustId(t, v.GetItems()[0].GetId())
}
