package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestAnOperatorStandsUpACustomerThatCanBeUsed is the whole of what `init` used
// to do for the first customer, done as an operator does it for the hundredth.
//
// `TestAnOperatorAdministersCustomers` already asserts the four writes, and
// that is the half this needed: `mayGrant` compares **methods and site** rather
// than tenants, and the operator's binding is tenant-wide `/roster.*/*` in the
// control plane, so it reaches a tenant that did not exist a moment ago.
//
// What was never asserted is the last mile, and it is the one that matters now:
// a customer's first person gets no password and no key from anywhere -- `init`
// does not write one and neither does creating them -- so a tenant stood up
// this way is somebody nobody can be until an operator writes a way in. Both
// routes are on this port, and both are here:
//
//	IssueService  mints an `rt_`, which resolves to its holder
//	VouchService  sets a password, which a front door checks
//
// It is the sequence a console walks and the sequence `docs/operating.md`
// gives, so a change that quietly breaks either leaves the deployment with no
// way to make its first customer.
func TestAnOperatorStandsUpACustomerThatCanBeUsed(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)
	c := signIn(t, s, "ops", passwordFrom(t, out))
	x.NotNil(c)

	g, err := s.GrpcAdmin(ctx, cmd.Config{})
	x.NoError(err)

	admin := pdtest.Serve(t, g)
	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value))

	// The four writes, which are `init`'s `allow()` with a frame in front of
	// them instead of `Ungated`.
	tn, err := app.NewTenantServiceClient(admin).Add(as,
		app.TenantAddRequest_builder{Alias: "newco", Name: "Newco Ltd"}.Build())
	x.NoError(err)

	h, err := app.NewHolderServiceClient(admin).Add(as, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:  "admin",
	}.Build())
	x.NoError(err)

	// The pattern and not an enumeration, for `allow()`'s reason: a list
	// written the day a customer is created is what existed that day, and the
	// first administrator is the one person who must not have to notice an
	// upgrade.
	r, err := app.NewRoleServiceClient(admin).Add(as, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:   "everything",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)

	_, err = app.NewBindingServiceClient(admin).Add(as, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	// Nothing so far is a way in. Asserted rather than assumed, because it is
	// the sentence the whole test exists to make true afterwards.
	n, err := s.Ent.Credential.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n, "creating a customer wrote them a credential")

	wire := served(t, s)

	t.Run("a key an operator mints, on the data plane's own port", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewIssueServiceClient(admin).IssueKey(as, app.IssueKeyRequest_builder{
			Holder:  app.HolderRef_builder{Id: h.GetId()}.Build(),
			Alias:   "bootstrap",
			Methods: []string{"/roster.*/*"},
		}.Build())
		x.NoError(err, "the operator could not write the new customer a way in")
		x.NotEmpty(v.GetToken())

		theirs := bearing(ctx, v.GetToken())

		// It resolves to the person rather than to the key, which is what makes
		// the binding above worth anything.
		me, err := app.NewMeServiceClient(wire).Get(theirs, app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal("admin", me.GetAlias())
		x.Equal(tn.GetId(), me.GetTenant())
		x.Equal([]string{"/roster.*/*"}, me.GetMethods())

		t.Run("and they can administer their own tenant with it", func(t *testing.T) {
			x := require.New(t)

			_, err := app.NewRoleServiceClient(wire).Add(theirs, app.RoleAddRequest_builder{
				Tenant:  app.TenantRef_builder{Id: tn.GetId()}.Build(),
				Alias:   "reader",
				Methods: []string{"/roster.HolderService/Get"},
			}.Build())
			x.NoError(err, "the first binding did not take")
		})

		t.Run("and nobody else's", func(t *testing.T) {
			x := require.New(t)

			// The wall, which is the other half of what makes this safe to hand
			// a customer: an `rt_` is narrowed to the tenant of the person it
			// resolves to, whoever minted it.
			vs, err := app.NewTenantServiceClient(wire).List(theirs,
				app.TenantListRequest_builder{}.Build())
			x.NoError(err)
			x.Len(vs.GetItems(), 1, "a customer's key answered with another tenant")
			x.Equal("newco", vs.GetItems()[0].GetAlias())
		})
	})

	t.Run("or a password, which is the same act for a person at a browser", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewVouchServiceClient(admin).Reset(as, app.VouchResetRequest_builder{
			Who: app.VouchWho_builder{Id: h.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		x.NotEmpty(v.GetSecret())

		// Checked by the service that stored it, on the port a front door
		// reaches. Unauthenticated on purpose is not what this is -- `Verify`
		// is a call some app makes, and here the deployment is `auth.Plain`'s
		// successor, so it goes over the admin port's own vouch.
		w, err := app.NewVouchServiceClient(admin).Verify(as, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: h.GetId()}.Build(),
			Secret: []byte(v.GetSecret()),
		}.Build())
		x.NoError(err)
		x.True(w.GetOk(), "the password an operator wrote does not check")

		t.Run("and a wrong one is refused", func(t *testing.T) {
			x := require.New(t)

			w, err := app.NewVouchServiceClient(admin).Verify(as, app.VouchVerifyRequest_builder{
				Who:    app.VouchWho_builder{Id: h.GetId()}.Build(),
				Secret: []byte("not it"),
			}.Build())
			x.NoError(err)
			x.False(w.GetOk())
		})
	})

	t.Run("and none of it needs a shell on the box", func(t *testing.T) {
		x := require.New(t)

		// The claim the console rests on: every write above went over a port,
		// as a session, through the rules -- not through `Ungated`. A caller
		// with no session gets none of it.
		_, err := app.NewTenantServiceClient(admin).Add(ctx,
			app.TenantAddRequest_builder{Alias: "nope"}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err))
	})
}
