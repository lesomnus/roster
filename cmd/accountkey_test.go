package cmd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

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
// the served stack: the four calls a front door makes before it has a person
// -- look an identity up, enrol a stranger, accept a claim, and read the row
// -- each answer for the key's own tenant and refuse for another's.
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
	frontDoor := []string{identityGet, identityAdd, holderAdd, holderGet, tenantGet, accept, listPeople}

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

	// Somebody in contoso who arrives through a provider.
	erin := addHolder(t, ctx, b.Server, b.Contoso, "erin")
	mustIdentity(t, ctx, b.Server, erin, "entra", "entra-erin")
	mayList(t, ctx, b, erin, listPeople)

	// And a second operator on the same roster, with somebody of their own --
	// the same provider, because one human may well sign up to both, and the
	// key must not be what relates them.
	fabrikam := add(t, ctx, b.Server, "fabrikam")
	fab := addHolder(t, ctx, b.Server, fabrikam, "fab")
	mustIdentity(t, ctx, b.Server, fab, "entra", "entra-fab")

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
}
