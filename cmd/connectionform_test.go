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

// TestAConnectionsNameIsHeldToAnAliasesGrammar: what a connection is called is
// in every identity's `provider` and in the path its answer comes back to
// (`/callback/<tenant>/<connection>`), and it was held to nothing. `Entra` was
// accepted beside `entra`, a second door to the same directory under which one
// person is two; so were `a/b c?` and a name with a space before it. A name is
// an alias -- lower-case letters, digits and single hyphens, beginning with a
// letter -- and one that is not is refused as it stands rather than folded
// into one that is, to the deployment as to a tenant: what was typed would
// otherwise not be what the row is called.
func TestAConnectionsNameIsHeldToAnAliasesGrammar(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	add := func(as context.Context, s app.Server, name string) error {
		t.Helper()
		_, err := s.Connection().Add(as, app.ConnectionAddRequest_builder{
			Tenant: at, Name: name, Issuer: "https://login.microsoftonline.com/contoso/v2.0", ClientId: "the-app",
		}.Build())

		return err
	}

	t.Run("an alias is a name", func(t *testing.T) {
		x := require.New(t)
		x.NoError(add(admin, b.Walled, "entra"))
		x.NoError(add(admin, b.Walled, "entra-staff-2"))
	})

	t.Run("and nothing else is", func(t *testing.T) {
		for _, name := range []string{"Entra", "ENTRA", " entra", "entra ", "a/b c?", "entra_staff", "entra--staff", "-entra", "2fa", ""} {
			err := add(admin, b.Walled, name)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%q was taken for a name: %v", name, err)
		}
	})

	t.Run("for the deployment either", func(t *testing.T) {
		err := add(ctx, b.Ungated, "Entra")
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	})

	t.Run("and a row is not renamed to one that is not", func(t *testing.T) {
		x := require.New(t)
		v := b.connectionOf(t, ctx, b.Contoso, "entra")
		_, err := b.Ungated.Connection().Patch(ctx, app.ConnectionPatchRequest_builder{
			Ref: app.ConnectionRef_builder{Id: v.GetId()}.Build(), DateUpdated: v.GetDateUpdated(),
			Name: z.Ptr("Entra"),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.Equal("entra", b.connectionOf(t, ctx, b.Contoso, "entra").GetName())
	})
}

// TestAConnectionsIssuerIsAnAddress: a connection's `issuer` is where a front
// door goes to ask who signed in, and its `client_id` is who the front door
// says it is there. `not a url` with no `client_id` was accepted, and was a
// sign-in that fails for everybody at the moment they try. An issuer is an
// absolute `http` or `https` address with a host, and no query or fragment --
// what OpenID Connect says one is -- and a client has a name.
//
// Plain `http` is not refused here. Whether a provider may be asked in the
// clear depends on where it is, which only the front door's deployment knows
// (`private_hosts`), so that refusal is the dialer's (`arrives/dial.go`).
func TestAConnectionsIssuerIsAnAddress(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	add := func(as context.Context, s app.Server, name, issuer, client string) error {
		t.Helper()
		_, err := s.Connection().Add(as, app.ConnectionAddRequest_builder{
			Tenant: at, Name: name, Issuer: issuer, ClientId: client,
		}.Build())

		return err
	}
	update := func(as context.Context, s app.Server, name string, with func(*app.ConnectionUpdateRequest_builder)) error {
		t.Helper()
		v := b.connectionOf(t, ctx, b.Contoso, name)
		req := app.ConnectionUpdateRequest_builder{
			Ref:         app.ConnectionRef_builder{Id: v.GetId()}.Build(),
			DateUpdated: v.GetDateUpdated(),
		}
		with(&req)
		_, err := s.Connection().Update(as, req.Build())

		return err
	}

	t.Run("an address is an issuer", func(t *testing.T) {
		x := require.New(t)
		x.NoError(add(admin, b.Walled, "entra", "https://login.microsoftonline.com/contoso/v2.0", "the-app"))
		x.NoError(add(admin, b.Walled, "github", "https://github.com", "the-app"), "GitHub's is spelled so")
		x.NoError(add(admin, b.Walled, "google", "https://accounts.google.com", "the-app"))
		x.NoError(add(admin, b.Walled, "premises", "http://idp.corp.example:8080/realms/contoso", "the-app"),
			"plain http is the front door's to refuse, which knows where the provider is")
	})

	t.Run("and nothing else is", func(t *testing.T) {
		for _, issuer := range []string{
			"",
			"not a url",
			"idp.example.test",
			"//idp.example.test",
			"https://",
			"https:idp.example.test",
			"ftp://idp.example.test",
			"file:///etc/passwd",
			"HTTPS://idp.example.test",
			" https://idp.example.test",
			"https://idp.example.test ",
			"https://idp.example.test/v2.0 ",
			"https://idp.example.test?tenant=contoso",
			"https://idp.example.test/v2.0?",
			"https://idp.example.test/v2.0#keys",
			"https://someone:secret@idp.example.test",
		} {
			err := add(admin, b.Walled, "another", issuer, "the-app")
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%q was taken for an issuer: %v", issuer, err)
		}
	})

	t.Run("nor a client with no name", func(t *testing.T) {
		err := add(admin, b.Walled, "another", "https://idp.example.test", "")
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	})

	t.Run("for the deployment either", func(t *testing.T) {
		x := require.New(t)
		err := add(ctx, b.Ungated, "another", "not a url", "the-app")
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		err = add(ctx, b.Ungated, "another", "https://idp.example.test", "")
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	})

	t.Run("and a row is not moved to one that is not", func(t *testing.T) {
		x := require.New(t)
		err := update(admin, b.Walled, "entra", func(r *app.ConnectionUpdateRequest_builder) {
			r.Issuer = z.Ptr("login.microsoftonline.com/contoso/v2.0")
		})
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		err = update(admin, b.Walled, "entra", func(r *app.ConnectionUpdateRequest_builder) {
			r.ClientId = z.Ptr("")
		})
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

		got := b.connectionOf(t, ctx, b.Contoso, "entra")
		x.Equal("https://login.microsoftonline.com/contoso/v2.0", got.GetIssuer())
		x.Equal("the-app", got.GetClientId())

		// What says nothing of either leaves both as they were.
		x.NoError(update(admin, b.Walled, "entra", func(r *app.ConnectionUpdateRequest_builder) {
			r.Desc = z.Ptr("staff")
		}))
	})
}

// TestAConnectionNamesNoClaimItsProviderIssuesNone: `subject_claim: oid` was
// read without the issuer beside it, so it was accepted at GitHub, which issues
// no ID token at all, and at Google, whose tokens carry no `oid`. From then on
// nobody signed in through the connection, and going back is the deployment's
// (`TestAConnectionNamesItsPeopleByOneClaim`): a tenant's administrator could
// close a door of their tenant and not open it again. So `oid` at an issuer
// known to issue none is refused, whichever half the write moves -- the claim,
// or the door under a claim already named.
func TestAConnectionNamesNoClaimItsProviderIssuesNone(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	add := func(as context.Context, s app.Server, name, issuer, claim string) error {
		t.Helper()
		_, err := s.Connection().Add(as, app.ConnectionAddRequest_builder{
			Tenant: at, Name: name, Issuer: issuer, ClientId: "the-app", SubjectClaim: claim,
		}.Build())

		return err
	}
	update := func(as context.Context, s app.Server, name string, with func(*app.ConnectionUpdateRequest_builder)) error {
		t.Helper()
		v := b.connectionOf(t, ctx, b.Contoso, name)
		req := app.ConnectionUpdateRequest_builder{
			Ref:         app.ConnectionRef_builder{Id: v.GetId()}.Build(),
			DateUpdated: v.GetDateUpdated(),
		}
		with(&req)
		_, err := s.Connection().Update(as, req.Build())

		return err
	}

	for name, issuer := range map[string]string{
		"github": "https://github.com",
		"google": "https://accounts.google.com",
	} {
		t.Run(name+" is not added naming oid", func(t *testing.T) {
			x := require.New(t)
			err := add(admin, b.Walled, name, issuer, "oid")
			x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
			// However the issuer is spelled, as far as `provisions` reads one.
			err = add(admin, b.Walled, name, issuer+"/", "oid")
			x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
			err = add(ctx, b.Ungated, name, issuer, "oid")
			x.Equal(codes.InvalidArgument, status.Code(err), "the deployment is told the same, since it is as true of it: %v", err)
		})

		t.Run(name+" is not moved to it", func(t *testing.T) {
			x := require.New(t)
			x.NoError(add(admin, b.Walled, name, issuer, ""))
			err := update(admin, b.Walled, name, func(r *app.ConnectionUpdateRequest_builder) { r.SubjectClaim = z.Ptr("oid") })
			x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
			x.Empty(b.connectionOf(t, ctx, b.Contoso, name).GetSubjectClaim())

			// And so is left a claim its tenant can still say.
			x.NoError(update(admin, b.Walled, name, func(r *app.ConnectionUpdateRequest_builder) { r.SubjectClaim = z.Ptr("sub") }))
		})
	}

	t.Run("nor is a connection that names oid moved to such a door", func(t *testing.T) {
		x := require.New(t)
		x.NoError(add(admin, b.Walled, "entra", "https://login.microsoftonline.com/contoso/v2.0", "oid"))
		err := update(admin, b.Walled, "entra", func(r *app.ConnectionUpdateRequest_builder) { r.Issuer = z.Ptr("https://github.com") })
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.Equal("https://login.microsoftonline.com/contoso/v2.0", b.connectionOf(t, ctx, b.Contoso, "entra").GetIssuer())

		// To another door that may issue one, it moves.
		x.NoError(update(admin, b.Walled, "entra", func(r *app.ConnectionUpdateRequest_builder) {
			r.Issuer = z.Ptr("https://login.microsoftonline.com/9f3e/v2.0")
		}))
	})
}
