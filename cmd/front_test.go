package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// TestAFrontDoorLearnsWhoseNameItIs is item 1 of PLAN.md's list, and it closes
// the hole every multi-tenant app was filling with a map of its own.
//
// A tenant is the same service under a different operator's own domain, so the
// name a browser arrived at *is* the operator whose service they are signing in
// to. Before this, `examples/sso` took that as configuration -- a copy of a fact
// roster owns, in as many places as there are apps.
func TestAFrontDoorLearnsWhoseNameItIs(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Name:   "acme.example.com",
	}.Build())
	x.NoError(err)

	f := front.New(b.Ungated)

	t.Run("a name it serves answers with the tenant", func(t *testing.T) {
		x := require.New(t)

		v, err := f.WhoseHost(ctx, app.FrontWhoseHostRequest_builder{
			Host: "acme.example.com",
		}.Build())
		x.NoError(err)
		x.Equal(b.Acme.Bytes(), v.GetTenant())
	})

	// A caller hands over `r.Host`, which carries a port whenever the app is
	// not on 443 -- every development deployment, and plenty of real ones.
	t.Run("and it takes a host as a request carries one", func(t *testing.T) {
		x := require.New(t)

		for _, h := range []string{"ACME.Example.com", "acme.example.com:8443", "  acme.example.com  "} {
			v, err := f.WhoseHost(ctx, app.FrontWhoseHostRequest_builder{Host: h}.Build())
			x.NoError(err, h)
			x.Equal(b.Acme.Bytes(), v.GetTenant(), h)
		}
	})

	// The refusal that matters. A front door told nothing would look somebody
	// up in whichever tenant it happened to reach, which is the mistake having
	// the tenant in `Identity`'s key exists to make impossible.
	t.Run("and a name it does not serve is not found", func(t *testing.T) {
		x := require.New(t)

		_, err := f.WhoseHost(ctx, app.FrontWhoseHostRequest_builder{
			Host: "hooli.example.com",
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})

	// Two operators cannot both own one name. It is one of the few constraints
	// here that crosses the wall, and a hostname being a public fact is why
	// that trade is the cheap one.
	t.Run("and two tenants cannot claim one name", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Hooli.Bytes()}.Build(),
			Name:   "acme.example.com",
		}.Build())
		x.Equal(codes.AlreadyExists, status.Code(err))
	})
}

// TestAHostIsStoredAsItIsCompared is the rule no schema can state.
//
// Nothing fails when it is left out -- the row is written, a console lists it,
// and the only thing that never happens is a match, because the lookup
// normalises what a browser arrived at. The symptom is a sign-in page saying
// nobody is there on a tenant that is plainly configured, which is a long way
// from the cause.
func TestAHostIsStoredAsItIsCompared(t *testing.T) {
	b, ctx := build(t)

	for _, tc := range []struct{ desc, name string }{
		{"upper case", "ACME.example.com"},
		{"a port", "acme.example.com:8443"},
		{"whitespace", " acme.example.com"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
				Name:   tc.name,
			}.Build())
			x.Equal(codes.InvalidArgument, status.Code(err))

			// And the message says what it should have been, because the
			// alternative -- fixing it quietly -- hands back a row that differs
			// from what the caller wrote and then disagrees with itself.
			x.Contains(status.Convert(err).Message(), "acme.example.com")
		})
	}
}

// TestWhereSomebodyAuthenticatesHangsOffTheDomain is item 2, and the condition
// on it is the whole of its shape.
//
// Answered per person it is the account-enumeration oracle D21 spent a
// condition avoiding: type an address, be told a provider, and learn whether
// that account is here. Answered per domain the answer for a stranger is the
// answer for everybody.
func TestWhereSomebodyAuthenticatesHangsOffTheDomain(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.MailDomain().Add(ctx, app.MailDomainAddRequest_builder{
		Tenant:   app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Name:     "acme.com",
		Provider: "entra",
	}.Build())
	x.NoError(err)

	f := front.New(b.Ungated)

	where := func(tenant, address string) string {
		t.Helper()

		var k []byte
		switch tenant {
		case "acme":
			k = b.Acme.Bytes()
		default:
			k = b.Hooli.Bytes()
		}

		v, err := f.WhereFrom(ctx, app.FrontWhereFromRequest_builder{
			Tenant: k, Address: address,
		}.Build())
		require.NoError(t, err)

		return v.GetProvider()
	}

	x.Equal("entra", where("acme", "somebody@acme.com"))
	x.Equal("entra", where("acme", "acme.com"), "the domain alone is the same question")
	x.Equal("entra", where("acme", "SOMEBODY@ACME.COM"))

	// Somebody nobody has heard of, at a domain that is routed. The answer is
	// the domain's, which is the property that makes this safe to answer at
	// all.
	x.Equal("entra", where("acme", "nobody-at-all@acme.com"))

	// A domain nothing says anything about is an empty answer rather than a
	// refusal: there is nothing a caller could carry on wrongly with, and a
	// front door that learns nothing offers whatever it offers everybody.
	x.Empty(where("acme", "somebody@gmail.com"))

	// And it is one operator's fact. The same domain asked for by another
	// tenant is not their answer.
	x.Empty(where("hooli", "somebody@acme.com"),
		"one operator read another's routing")
}

// TestSigningInByAddressIsF7Closed.
//
// F7 said an address could not name anybody: `Email` is unique **per holder**
// so that a consultant can be one person in two tenants under one address, and
// one address could therefore name two people. `VouchWho` field 4 was left
// empty with a comment saying why.
//
// The way out was the second half of F7's own sentence -- *a tenant that
// arrives from somewhere the form did not type*. A front door has one now, and
// `Email` has a unique `(tenant, address)` to go with it.
func TestSigningInByAddressIsF7Closed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: b.AcmeUser.Bytes()}.Build(),
		Address: "someone@acme.example",
	}.Build())
	x.NoError(err)

	b.sets(t, ctx, b.AcmeUser, "correct horse battery staple")

	v := b.vouched()
	verify := func(tenant, address, secret string) *app.VouchVerifyResponse {
		t.Helper()

		res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Tenant: tenant, Address: address}.Build(),
			Secret: []byte(secret),
		}.Build())
		require.NoError(t, err)

		return res
	}

	t.Run("an address names one person now", func(t *testing.T) {
		x := require.New(t)

		res := verify("acme", "someone@acme.example", "correct horse battery staple")
		x.True(res.GetOk())
		x.Equal(b.AcmeUser.Bytes(), res.GetHolder())
	})

	// Every way of not being there is one answer, which is what D14 asks of
	// every path that ends in a refusal -- and this path has two extra reads to
	// leak through.
	t.Run("and every way of not being there is one answer", func(t *testing.T) {
		x := require.New(t)

		for _, tc := range []struct{ desc, tenant, address, secret string }{
			{"a wrong password", "acme", "someone@acme.example", "hunter2"},
			{"an address nobody has", "acme", "nobody@acme.example", "hunter2"},
			{"a tenant nobody serves", "nowhere", "someone@acme.example", "hunter2"},
			{"the right password at the wrong tenant", "hooli", "someone@acme.example", "correct horse battery staple"},
		} {
			res := verify(tc.tenant, tc.address, tc.secret)
			x.False(res.GetOk(), tc.desc)
			x.Nil(res.GetHolder(), tc.desc)
			x.Nil(res.GetLockedUntil(), tc.desc)
		}
	})

	// D3's consultant, which is what F7 refused to give up and this does not
	// take: one address, two tenants, two people.
	t.Run("and one address in two tenants is still two people", func(t *testing.T) {
		x := require.New(t)

		them := b.holder(t, ctx, b.Hooli, "consultant")
		_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: them.Bytes()}.Build(),
			Address: "someone@acme.example",
		}.Build())
		x.NoError(err, "a consultant was refused the second account")

		b.sets(t, ctx, them, "a different password")

		x.Equal(b.AcmeUser.Bytes(),
			verify("acme", "someone@acme.example", "correct horse battery staple").GetHolder())
		x.Equal(them.Bytes(),
			verify("hooli", "someone@acme.example", "a different password").GetHolder())
	})

	// And within one tenant it is now refused, which is the constraint that
	// makes the lookup above answer with one row rather than picking.
	t.Run("and two people in one tenant cannot share one", func(t *testing.T) {
		x := require.New(t)

		other := b.holder(t, ctx, b.Acme, "second")
		_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: other.Bytes()}.Build(),
			Address: "someone@acme.example",
		}.Build())
		x.Equal(codes.AlreadyExists, status.Code(err))
	})

	// Naming two ways is a caller that has not decided, refused rather than
	// resolved in some order this would then have to define.
	t.Run("and naming two ways is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who: app.VouchWho_builder{
				Tenant: "acme", Alias: "someone", Address: "someone@acme.example",
			}.Build(),
			Secret: []byte("whatever"),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err))
	})
}

// TestAConnectionIsRostersAndItsSecretIsNot is item 9's decision, and it is the
// decision rather than the schema that was open.
//
// A connection carries a client secret, and handing one back would make it the
// first secret roster returns rather than checks — which is what D13 refuses.
// The way out is not to hold it: everything about a connection that varies per
// tenant is **public**, and the secret is one string that has to reach the
// front door anyway, because using it means being the relying party and that is
// what D19 says roster is not.
func TestAConnectionIsRostersAndItsSecretIsNot(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant:    app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
		Name:      "entra",
		Issuer:    "https://login.microsoftonline.com/acme/v2.0",
		ClientId:  "a-client-id",
		Scopes:    []string{"email"},
		SecretRef: "env:ACME_ENTRA_SECRET",
	}.Build())
	x.NoError(err)

	// Read by the pair a front door has: the tenant from the hostname, and the
	// provider from the address.
	v, err := b.Ungated.Connection().Get(ctx, app.ConnectionGetRequest_builder{
		Ref: app.ConnectionRef_builder{
			At: app.ConnectionRefByAt_builder{
				Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
				Name:   z.Ptr("entra"),
			}.Build(),
		}.Build(),
		Select: app.ConnectionSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)

	x.Equal("https://login.microsoftonline.com/acme/v2.0", v.GetIssuer())
	x.Equal("a-client-id", v.GetClientId())
	x.Equal([]string{"email"}, v.GetScopes())

	// A reference and never the thing. roster does not read it, and what it
	// means is the front door's to know.
	x.Equal("env:ACME_ENTRA_SECRET", v.GetSecretRef())

	// And the name is one per tenant, because two operators may both call one
	// "entra" and mean two different directories.
	t.Run("and two operators may both have one", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
			Tenant:   app.TenantRef_builder{Id: b.Hooli.Bytes()}.Build(),
			Name:     "entra",
			Issuer:   "https://login.microsoftonline.com/hooli/v2.0",
			ClientId: "another-client-id",
		}.Build())
		x.NoError(err, "one operator's provider name took another's")

		_, err = b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
			Tenant:   app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
			Name:     "entra",
			Issuer:   "https://example.test",
			ClientId: "a-third",
		}.Build())
		x.Equal(codes.AlreadyExists, status.Code(err))
	})

	// The whole path a front door walks: a name, a tenant, an address, a
	// provider, a connection.
	t.Run("and it is the end of the path a front door walks", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
			Name:   "acme.example.com",
		}.Build())
		x.NoError(err)

		_, err = b.Ungated.MailDomain().Add(ctx, app.MailDomainAddRequest_builder{
			Tenant:   app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
			Name:     "acme.com",
			Provider: "entra",
		}.Build())
		x.NoError(err)

		f := front.New(b.Ungated)

		whose, err := f.WhoseHost(ctx, app.FrontWhoseHostRequest_builder{
			Host: "acme.example.com",
		}.Build())
		x.NoError(err)

		where, err := f.WhereFrom(ctx, app.FrontWhereFromRequest_builder{
			Tenant: whose.GetTenant(), Address: "somebody@acme.com",
		}.Build())
		x.NoError(err)
		x.Equal("entra", where.GetProvider())

		got, err := b.Ungated.Connection().Get(ctx, app.ConnectionGetRequest_builder{
			Ref: app.ConnectionRef_builder{
				At: app.ConnectionRefByAt_builder{
					Tenant: app.TenantRef_builder{Id: whose.GetTenant()}.Build(),
					Name:   z.Ptr(where.GetProvider()),
				}.Build(),
			}.Build(),
			Select: app.ConnectionSelect_builder{Issuer: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("https://login.microsoftonline.com/acme/v2.0", got.GetIssuer())
	})
}
