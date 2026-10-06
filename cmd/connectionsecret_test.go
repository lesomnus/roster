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

// TestAConnectionsSecretGoesWhereTheDeploymentSays: a connection's `secret_ref`
// names one of the deployment's secrets, and the front door sends it to the
// connection's issuer. Were both a tenant's to write, a tenant's administrator
// could name any secret the front door holds beside an issuer of their own.
// So a new reference, and a new issuer for a connection with one, are the
// deployment's; keeping the reference or taking it away is the tenant's, and
// so is a connection with no secret at all.
func TestAConnectionsSecretGoesWhereTheDeploymentSays(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	// The tenant's administrator writes through the wall, and the deployment
	// -- the file, the CLI on the database -- through the server with none.
	server := func(as context.Context) app.Server {
		if as == ctx {
			return b.Ungated
		}

		return b.Walled
	}
	add := func(as context.Context, name, issuer, ref string) error {
		t.Helper()
		_, err := server(as).Connection().Add(as, app.ConnectionAddRequest_builder{
			Tenant: at, Name: name, Issuer: issuer, ClientId: "the-app", SecretRef: ref,
		}.Build())

		return err
	}
	read := func(name string) *app.Connection {
		t.Helper()
		v, err := b.Ungated.Connection().Get(ctx, app.ConnectionGetRequest_builder{
			Ref:    app.ConnectionRef_builder{At: app.ConnectionRefByAt_builder{Tenant: at, Name: z.Ptr(name)}.Build()}.Build(),
			Select: app.ConnectionSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		require.NoError(t, err)

		return v
	}
	update := func(as context.Context, name string, with func(*app.ConnectionUpdateRequest_builder)) error {
		t.Helper()
		v := read(name)
		req := app.ConnectionUpdateRequest_builder{
			Ref:         app.ConnectionRef_builder{Id: v.GetId()}.Build(),
			DateUpdated: v.GetDateUpdated(),
		}
		with(&req)
		_, err := server(as).Connection().Update(as, req.Build())

		return err
	}

	t.Run("a connection with no secret is the tenant's", func(t *testing.T) {
		require.NoError(t, add(admin, "public", "https://accounts.google.com", ""))
	})

	t.Run("and one naming a secret is not", func(t *testing.T) {
		x := require.New(t)
		err := add(admin, "mine", "https://idp.attacker.example", "env:ROSTER_LOGIN_KEY")
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("which the deployment writes", func(t *testing.T) {
		require.NoError(t, add(ctx, "entra", "https://login.microsoftonline.com/contoso/v2.0", "env:CONTOSO_ENTRA_SECRET"))
	})

	t.Run("and the tenant may change all but where its secret goes", func(t *testing.T) {
		x := require.New(t)
		x.NoError(update(admin, "entra", func(r *app.ConnectionUpdateRequest_builder) {
			r.Scopes = []string{"email", "profile", "User.Read"}
			r.ClientId = z.Ptr("another-app")
			r.SecretRef = z.Ptr("env:CONTOSO_ENTRA_SECRET")
		}), "sending the reference back as it read it is not a new one")

		err := update(admin, "entra", func(r *app.ConnectionUpdateRequest_builder) {
			r.SecretRef = z.Ptr("env:FABRIKAM_ENTRA_SECRET")
		})
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		err = update(admin, "entra", func(r *app.ConnectionUpdateRequest_builder) {
			r.Issuer = z.Ptr("https://idp.attacker.example")
		})
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		got := read("entra")
		x.Equal("env:CONTOSO_ENTRA_SECRET", got.GetSecretRef())
		x.Equal("https://login.microsoftonline.com/contoso/v2.0", got.GetIssuer())
	})

	// Taken away, nothing is sent anywhere, so the issuer may move with it.
	t.Run("and may take the secret away, and then move it", func(t *testing.T) {
		x := require.New(t)
		x.NoError(update(admin, "entra", func(r *app.ConnectionUpdateRequest_builder) {
			r.SecretRef = z.Ptr("")
			r.Issuer = z.Ptr("https://login.example/contoso")
		}))
		got := read("entra")
		x.Empty(got.GetSecretRef())
		x.Equal("https://login.example/contoso", got.GetIssuer())
	})

	t.Run("and the deployment may do all of it", func(t *testing.T) {
		require.NoError(t, update(ctx, "entra", func(r *app.ConnectionUpdateRequest_builder) {
			r.Issuer = z.Ptr("https://login.microsoftonline.com/contoso/v2.0")
			r.SecretRef = z.Ptr("env:CONTOSO_ENTRA_SECRET")
		}))
	})
}
