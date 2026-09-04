package cmd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	x.Nil(first.GetLockedUntil(), "locked one answer early")
	res := wrong()
	x.NotNil(res.GetLockedUntil(), "two wrong answers did not lock, as the deployment said they should")
	x.WithinDuration(time.Now().Add(time.Hour), res.GetLockedUntil().AsTime(), time.Minute, "locked for the default rather than for what was said")

	t.Run("and unset is the default", func(t *testing.T) {
		x := require.New(t)
		s, _ := personOn(t, nil)
		x.Equal(vouch.DefaultLockout, s.Lockout)
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
