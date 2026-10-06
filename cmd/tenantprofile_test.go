package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestASlackReferenceIsTheDeploymentsToWrite: a tenant says whether its
// profiles are filled, and the deployment says which of its secrets a Slack
// fill sends to Slack. A tenant's administrator who could write the reference
// could point the front door at any secret it holds -- another tenant's, its
// own key -- so a new one is refused to them, and keeping or taking away the
// one there is not.
func TestASlackReferenceIsTheDeploymentsToWrite(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	// The tenant's administrator writes through the wall, and the deployment
	// -- the file, the CLI on the database -- through the server that has none.
	write := func(as context.Context, p *app.TenantProfile) error {
		t.Helper()
		v, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: at}.Build())
		require.NoError(t, err)
		s := b.Walled
		if as == ctx {
			s = b.Ungated
		}
		_, err = s.Tenant().Update(as, app.TenantUpdateRequest_builder{
			Ref:         at,
			DateUpdated: v.GetDateUpdated(),
			Config:      app.TenantConfig_builder{Profile: p}.Build(),
		}.Build())

		return err
	}
	profile := func() *app.TenantProfile {
		t.Helper()
		v, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
			Ref: at, Select: app.TenantSelect_builder{Config: z.Ptr(true)}.Build(),
		}.Build())
		require.NoError(t, err)

		return v.GetConfig().GetProfile()
	}

	t.Run("the tenant says whether", func(t *testing.T) {
		x := require.New(t)
		x.NoError(write(admin, app.TenantProfile_builder{Fill: true}.Build()))
		x.True(profile().GetFill())
	})

	t.Run("and not which secret", func(t *testing.T) {
		x := require.New(t)
		err := write(admin, app.TenantProfile_builder{Fill: true, SlackSecretRef: "env:ROSTER_LOGIN_KEY"}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		x.Empty(profile().GetSlackSecretRef())
	})

	t.Run("which the deployment does", func(t *testing.T) {
		x := require.New(t)
		x.NoError(write(ctx, app.TenantProfile_builder{Fill: true, SlackSecretRef: "env:CONTOSO_SLACK"}.Build()))
		x.Equal("env:CONTOSO_SLACK", profile().GetSlackSecretRef())
	})

	// A form saving the settings sends the reference back as it read it, and
	// that is not a new one.
	t.Run("and the tenant may keep it, or take it away, and not change it", func(t *testing.T) {
		x := require.New(t)
		x.NoError(write(admin, app.TenantProfile_builder{Fill: false, SlackSecretRef: "env:CONTOSO_SLACK"}.Build()))
		x.Equal("env:CONTOSO_SLACK", profile().GetSlackSecretRef())

		err := write(admin, app.TenantProfile_builder{SlackSecretRef: "env:SOMEBODY_ELSES"}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		x.NoError(write(admin, app.TenantProfile_builder{}.Build()))
		x.Empty(profile().GetSlackSecretRef())
	})

	// A reference, and not the token: a token written here would be a secret
	// in a row everybody who may read the tenant can read.
	t.Run("and it is a reference", func(t *testing.T) {
		err := write(ctx, app.TenantProfile_builder{Fill: true, SlackSecretRef: "xoxb-1234-the-token"}.Build())
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	})
}
