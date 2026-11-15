package cmd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// A person in a customer, on a deployment configured as the test says.
func personOn(t *testing.T, with func(c *cmd.Config)) (*cmd.Server, []byte) {
	t.Helper()
	x := require.New(t)
	s, _, _ := adminDeployment(t, with)
	ctx := context.Background()

	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)
	who, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(), Alias: "erin",
	}.Build())
	x.NoError(err)

	return s, who.GetId()
}

func setFor(ctx context.Context, s *cmd.Server, who []byte, secret string) error {
	_, err := s.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref: app.HolderRef_builder{Id: who}.Build(), Secret: []byte(secret),
	}.Build())

	return err
}

// TestTheLockoutIsTheDeploymentsNumbers: `vouch.lockout` is read, on both
// paths that count -- a sign-in, and the re-authentication a person's own
// password change asks for -- and unset is ten per fifteen minutes.
func TestTheLockoutIsTheDeploymentsNumbers(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	s, who := personOn(t, func(c *cmd.Config) {
		c.Vouch.Lockout = cmd.LockoutConfig{Failures: 2, For: time.Hour}
	})
	x.Equal(vouch.Lockout{Failures: 2, For: time.Hour}, s.Lockout)
	x.NoError(setFor(ctx, s, who, "correct horse battery staple"))

	v := vouch.New(s.Ungated, s.Ungated, vouch.WithLockout(s.Lockout))
	wrong := func() *app.VouchVerifyResponse {
		res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{
			Who: app.VouchWho_builder{Id: who}.Build(), Secret: []byte("not it, not it"),
		}.Build())
		x.NoError(err)
		return res
	}
	first := wrong()
	x.False(first.GetOk())
	x.Nil(first.GetDateLocked(), "locked one answer early")
	res := wrong()
	x.NotNil(res.GetDateLocked(), "two wrong answers did not lock, as the deployment said they should")
	x.WithinDuration(time.Now().Add(time.Hour), res.GetDateLocked().AsTime(), time.Minute, "locked for the default rather than for what was said")

	t.Run("and unset is the default", func(t *testing.T) {
		x := require.New(t)
		s, _ := personOn(t, nil)
		x.Equal(vouch.DefaultLockout, s.Lockout)
	})
}

// TestTheConsolesSignInCountsByTheDeploymentsLockout: `AuthService/SignIn`
// closes an account at the number the deployment said, on the data plane and
// at the admin listener.
//
// # What it was
//
// The test above builds the `vouch.Server` it asks, with the deployment's
// numbers handed to it, and so held nothing about the one a served deployment
// asks at its own door. `server/console` built that one itself and was handed
// nothing: `AuthService/SignIn` counted by the default, ten and fifteen
// minutes, whatever `vouch.lockout` said. A deployment that asked for an
// account to close at the third wrong password had it close there for every
// app that asks roster, and at the tenth at the one sign-in anybody with a
// browser reaches (#275).
func TestTheConsolesSignInCountsByTheDeploymentsLockout(t *testing.T) {
	two := func(c *cmd.Config) {
		c.SignIn.Enabled = true
		c.Vouch.Lockout = cmd.LockoutConfig{Failures: 2, For: time.Hour}
	}

	// wrongTwiceThenRight is the whole of it: two wrong passwords, and then
	// the right one, which a closed account refuses as it refuses a wrong one.
	wrongTwiceThenRight := func(t *testing.T, auth app.AuthServiceClient, at context.Context, alias, secret string) {
		t.Helper()
		x := require.New(t)

		for range 2 {
			_, err := auth.SignIn(at, app.AuthSignInRequest_builder{Alias: alias, Password: "not it, not it"}.Build())
			x.Equal(codes.Unauthenticated, status.Code(err), "%v", err)
		}

		_, err := auth.SignIn(at, app.AuthSignInRequest_builder{Alias: alias, Password: secret}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err),
			"two wrong passwords did not close the account at this door, as the deployment said they should: %v", err)
	}

	t.Run("a tenant's person, on the data plane", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t, two)

		_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Name:   "contoso.example",
		}.Build())
		x.NoError(err)

		const secret = "correct horse battery staple"
		x.NoError(setFor(ctx, b.Server, b.ContosoUser.Bytes(), secret))

		auth := app.NewAuthServiceClient(pdtest.Serve(t, b.grpc(t)))
		at := metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example"))
		wrongTwiceThenRight(t, auth, at, "someone", secret)

		// And it is the row that says so, for an hour and not for a quarter.
		v, err := b.Ungated.Holder().SignsIn(ctx, app.HolderSignsInRequest_builder{
			Ref: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		x.Len(v.GetCredentials(), 1)
		x.NotNil(v.GetCredentials()[0].GetDateLocked())
		x.WithinDuration(time.Now().Add(time.Hour), v.GetCredentials()[0].GetDateLocked().AsTime(), time.Minute)
	})

	t.Run("an operator, at the admin listener", func(t *testing.T) {
		x := require.New(t)
		s, c, out := adminDeployment(t, two)

		g, err := s.GrpcAdmin(t.Context(), c)
		x.NoError(err)

		auth := app.NewAuthServiceClient(pdtest.Serve(t, g))
		wrongTwiceThenRight(t, auth, t.Context(), "admin", passwordFrom(t, out))
	})
}

// TestAPasswordHasToBeLongEnough: eight unless the deployment says, checked
// wherever a password arrives, and a ceiling that keeps the hash honest.
func TestAPasswordHasToBeLongEnough(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	s, who := personOn(t, nil)
	x.Equal(codes.InvalidArgument, status.Code(setFor(ctx, s, who, "short12")), "seven characters were taken")
	x.NoError(setFor(ctx, s, who, "eight888"))
	x.Equal(codes.InvalidArgument, status.Code(setFor(ctx, s, who, strings.Repeat("a", 1025))), "a kilobyte and one was hashed")
	x.NoError(setFor(ctx, s, who, strings.Repeat("a", 1024)))

	t.Run("and the deployment may ask for more", func(t *testing.T) {
		x := require.New(t)
		s, who := personOn(t, func(c *cmd.Config) { c.Vouch.Password.MinLength = 12 })
		x.Equal(codes.InvalidArgument, status.Code(setFor(ctx, s, who, "eleven11111")))
		x.NoError(setFor(ctx, s, who, "twelve121212"))
	})
}

// TestAPasswordUsedBeforeIsRefusedWhenAsked: off unless `no_reuse` says how
// many, then the one held and that many before it are refused, and one
// further back is not -- the memory is a window and not a life.
func TestAPasswordUsedBeforeIsRefusedWhenAsked(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	t.Run("off, a password comes back", func(t *testing.T) {
		x := require.New(t)
		s, who := personOn(t, nil)
		x.NoError(setFor(ctx, s, who, "first-one-1"))
		x.NoError(setFor(ctx, s, who, "second-one-2"))
		x.NoError(setFor(ctx, s, who, "first-one-1"))
	})

	s, who := personOn(t, func(c *cmd.Config) { c.Vouch.Password.NoReuse = 2 })
	x.NoError(setFor(ctx, s, who, "first-one-1"))
	x.Equal(codes.FailedPrecondition, status.Code(setFor(ctx, s, who, "first-one-1")), "the one held was taken again")
	x.NoError(setFor(ctx, s, who, "second-one-2"))
	x.NoError(setFor(ctx, s, who, "third-one-3"))
	x.Equal(codes.FailedPrecondition, status.Code(setFor(ctx, s, who, "first-one-1")), "two back was taken")
	x.Equal(codes.FailedPrecondition, status.Code(setFor(ctx, s, who, "second-one-2")), "one back was taken")
	x.NoError(setFor(ctx, s, who, "fourth-one-4"))
	x.NoError(setFor(ctx, s, who, "first-one-1"), "three back was refused; the window is two")

	t.Run("and a sign-in still works with what was just set", func(t *testing.T) {
		x := require.New(t)
		res, err := vouch.New(s.Ungated, s.Ungated).Verify(ctx, app.VouchVerifyRequest_builder{
			Who: app.VouchWho_builder{Id: who}.Build(), Secret: []byte("first-one-1"),
		}.Build())
		x.NoError(err)
		x.True(res.GetOk())
	})

	t.Run("and the memory is off the wire and out of the trail", func(t *testing.T) {
		x := require.New(t)
		v, err := s.Ungated.Credential().Get(ctx, app.CredentialGetRequest_builder{
			Ref: app.CredentialRef_builder{Kind: app.CredentialRefByKind_builder{
				Holder: app.HolderRef_builder{Id: who}.Build(), Kind: ptr(vouch.KindPassword),
			}.Build()}.Build(),
			Select: app.CredentialSelect_builder{Previous: ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Len(v.GetPrevious(), 2, "the window holds what it said")
	})
}
