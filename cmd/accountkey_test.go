package cmd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestAnAccountAppHoldsOneTenantsKeyAndReachesOnlyThatTenant is the spike
// `ts/plan.md` § E asked for, and the fact P4 is built on.
//
// An account app that fronts several operators has two credentials to choose
// from. A deployment key (`rk_`) resolves to a frame with **no tenant** and the
// policy hands it `frame.Everything` -- so the only thing keeping contoso's
// request out of fabrikam's rows would be the app's own code, which is the
// wiring-as-control roster refuses elsewhere. A tenant key (`rt_`) resolves to a
// holder inside a tenant, the tenant travels with the actor, and the wall does
// the narrowing with no discipline asked of the app. So the app holds one `rt_`
// per tenant it fronts, picked by host, and this is what that buys, through
// the served stack: the five calls a front door makes before it has a person
// -- look an identity up, enrol a stranger, accept a claim, read the row, and
// check a password -- each answer for the key's own tenant and refuse for
// another's.
//
// The last of those was added after the other four: `Accept` and `Vouch.Link`
// each resolve who they are about through the walled stack and `Verify` did
// not, so the same request that this test refuses as a claim was answered as a
// password. `server/vouch/vouch.go` carries what that cost and why the read
// moved.
//
// No new API: the key is `ApiKey.Issue` with a holder, which `roster key add
// --tenant contoso --holder account` already mints. This test mints it the way
// the test bench does and asks what it reaches.
func TestAnAccountAppHoldsOneTenantsKeyAndReachesOnlyThatTenant(t *testing.T) {
	const (
		identityGet = "/roster.IdentityService/Get"
		identityAdd = "/roster.IdentityService/Add"
		holderAdd   = "/roster.HolderService/Add"
		holderGet   = "/roster.HolderService/Get"
		tenantGet   = "/roster.TenantService/Get"
	)
	// And what it hands out: `Accept`'s `methods` are bounded by what the
	// caller may call, so a front door that mints delegations allowing
	// `listPeople` holds `listPeople`.
	frontDoor := []string{identityGet, identityAdd, holderAdd, holderGet, tenantGet, accept, verify, delegate, listPeople}

	x := require.New(t)
	b := keyFor(t, accept)
	ctx := t.Context()

	// The app, as a holder in contoso, with the role a front door needs and a
	// tenant key that acts as it.
	account := addHolder(t, ctx, b.Server, b.Contoso, "account-app")
	permits(t, ctx, b, b.Contoso, account, "front-door", frontDoor...)
	token := mintFor(t, ctx, b, account, "front-door", frontDoor, time.Time{})
	x.True(strings.HasPrefix(token, keys.PrefixTenant), "the bench minted the wrong kind of key")
	as := bearing(ctx, token)

	// Somebody in contoso who arrives through a provider, and has a password
	// too -- a front door offers both arms and this test asks about both.
	erin := addHolder(t, ctx, b.Server, b.Contoso, "erin")
	mustIdentity(t, ctx, b.Server, erin, "entra", "entra-erin")
	mayList(t, ctx, b, erin, listPeople)
	mustPassword(t, ctx, b, erin, secret)

	// And a second operator on the same roster, with somebody of their own --
	// the same provider, because one human may well sign up to both, and the
	// key must not be what relates them.
	//
	// Given the same password on purpose: two operators' people reuse one, and
	// the question this test asks is what contoso's key may do about it.
	fabrikam := add(t, ctx, b.Server, "fabrikam")
	fab := addHolder(t, ctx, b.Server, fabrikam, "fab")
	mustIdentity(t, ctx, b.Server, fab, "entra", "entra-fab")
	mustPassword(t, ctx, b, fab, secret)
	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: fab.Bytes()}.Build(),
		Address: fabAddress,
	}.Build())
	x.NoError(err)

	// And a role of fabrikam's own, so that a delegation for fab would have
	// something to allow. What refuses below has to be the wall, not an empty
	// intersection standing in for it.
	fabRole, err2 := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: fabrikam.Bytes()}.Build(),
		Alias:   "reader",
		Methods: []string{listPeople},
	}.Build())
	x.NoError(err2)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: fabRole.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: fab.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	identities := app.NewIdentityServiceClient(b.Conn)
	holders := app.NewHolderServiceClient(b.Conn)
	vouch := app.NewVouchServiceClient(b.Conn)

	bySubject := func(tenant []byte, subject string) *app.IdentityGetRequest {
		return app.IdentityGetRequest_builder{
			Ref: app.IdentityRef_builder{
				Subject: app.IdentityRefBySubject_builder{
					TenantId: tenant,
					Provider: proto.String("entra"),
					Subject:  proto.String(subject),
				}.Build(),
			}.Build(),
			Select: app.IdentitySelect_builder{
				Holder: app.HolderSelect_builder{}.Build(),
			}.Build(),
		}.Build()
	}

	t.Run("it finds its own tenant's person by claim", func(t *testing.T) {
		x := require.New(t)

		v, err := identities.Get(as, bySubject(b.Contoso.Bytes(), "entra-erin"))
		x.NoError(err)
		x.Equal(erin.Bytes(), v.GetHolder().GetId())
	})

	t.Run("and not another tenant's, even naming the tenant", func(t *testing.T) {
		x := require.New(t)

		// The reference is complete and correct; the row exists. What answers
		// is the wall, which never saw fabrikam's rows for this key at all.
		_, err := identities.Get(as, bySubject(fabrikam.Bytes(), "entra-fab"))
		x.Equal(codes.NotFound, status.Code(err),
			"a contoso key read a fabrikam row: %v", err)
	})

	t.Run("it enrols a stranger into its own tenant and into no other", func(t *testing.T) {
		x := require.New(t)

		_, err := holders.Add(as, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:  "newcomer",
		}.Build())
		x.NoError(err, "the front door could not enrol somebody into its own tenant")

		_, err = holders.Add(as, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: fabrikam.Bytes()}.Build(),
			Alias:  "intruder",
		}.Build())
		x.NotEqual(codes.OK, status.Code(err), "a contoso key created a person in fabrikam")

		// And nothing landed there: the refusal was the write not happening,
		// not a status code over a row that exists.
		vs, err := b.Ungated.Holder().List(ctx, app.HolderListRequest_builder{
			Filters: []*app.HolderFilter{app.HolderFilter_builder{
				Tenant: app.TenantRef_builder{Id: fabrikam.Bytes()}.Build(),
			}.Build()},
		}.Build())
		x.NoError(err)
		for _, h := range vs.GetItems() {
			x.NotEqual("intruder", h.GetAlias(), "the intruder is in fabrikam")
		}
	})

	t.Run("it accepts a claim about its own tenant's person", func(t *testing.T) {
		x := require.New(t)

		res, err := vouch.Accept(as, app.VouchAcceptRequest_builder{
			Claim: app.VouchClaim_builder{
				Tenant:   b.Contoso.Bytes(),
				Provider: "entra",
				Subject:  "entra-erin",
			}.Build(),
			Methods: []string{listPeople},
		}.Build())
		x.NoError(err)
		x.True(strings.HasPrefix(res.GetToken(), keys.PrefixDelegation), "no delegation came back")
		x.Equal(erin.Bytes(), res.GetVerified().GetHolder())
	})

	t.Run("and mints nothing for a claim about another tenant's", func(t *testing.T) {
		x := require.New(t)

		// The one call that would matter most: `Accept` is *sign in as whoever
		// the caller names*, and on a deployment key that is anyone anywhere. On
		// a tenant key the claim about fabrikam names nobody this key can see.
		res, err := vouch.Accept(as, app.VouchAcceptRequest_builder{
			Claim: app.VouchClaim_builder{
				Tenant:   fabrikam.Bytes(),
				Provider: "entra",
				Subject:  "entra-fab",
			}.Build(),
			Methods: []string{listPeople},
		}.Build())
		x.NotEqual(codes.OK, status.Code(err), "a contoso key minted a delegation for somebody in fabrikam")
		x.Empty(res.GetToken())
	})

	t.Run("it checks a password of its own tenant's person", func(t *testing.T) {
		x := require.New(t)

		v, err := vouch.Verify(as, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Tenant: "contoso", Alias: "erin"}.Build(),
			Secret: []byte(secret),
		}.Build())
		x.NoError(err)
		x.True(v.GetOk(), "the front door could not check its own tenant's password")
		x.Equal(erin.Bytes(), v.GetHolder())
	})

	t.Run("and checks nothing for another tenant's, by any of the three forms", func(t *testing.T) {
		// The same sentence as `Accept` above, with a password instead of a
		// claim. It was the arm that was open: `Accept` resolves the identity
		// through the walled stack and `Vouch.Link` the holder, while this one
		// read the person through the server the wall was never installed on --
		// so a key that cannot see a fabrikam row could check a fabrikam
		// password and be handed a delegation that acts as one.
		//
		// Three forms because `VouchWho` has three, and a rule written for the
		// one a form collects is a rule the other two walk past.
		for _, tt := range []struct {
			name string
			who  *app.VouchWho
		}{
			{"by id", app.VouchWho_builder{Id: fab.Bytes()}.Build()},
			{"by @tenant/alias", app.VouchWho_builder{Tenant: "fabrikam", Alias: "fab"}.Build()},
			{"by address", app.VouchWho_builder{Tenant: "fabrikam", Address: fabAddress}.Build()},
		} {
			t.Run(tt.name, func(t *testing.T) {
				x := require.New(t)

				v, err := vouch.Verify(as, app.VouchVerifyRequest_builder{
					Who:    tt.who,
					Secret: []byte(secret),
				}.Build())
				x.NoError(err, "a refusal here is an ordinary no, not an error")
				x.False(v.GetOk(), "a contoso key checked a fabrikam password")

				res, err := vouch.Delegate(as, app.VouchDelegateRequest_builder{
					Who:     tt.who,
					Secret:  []byte(secret),
					Methods: []string{listPeople},
				}.Build())
				x.NoError(err)
				x.False(res.GetVerified().GetOk())
				x.Empty(res.GetToken(), "a contoso key minted a delegation for somebody in fabrikam")
			})
		}
	})
}

// secret is the password both people in the test above hold, and fabAddress is
// the one an address-form sign-in would collect for fabrikam's.
const (
	secret     = "correct horse battery staple"
	fabAddress = "fab@fabrikam.example"
)

// mustPassword gives somebody one, through the server the deployment itself
// reaches -- which is where a first password comes from.
func mustPassword(t *testing.T, ctx context.Context, b *keyedBuilt, who pdid.Id, password string) {
	t.Helper()

	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Secret: []byte(password),
	}.Build())
	require.NoError(t, err)
}
