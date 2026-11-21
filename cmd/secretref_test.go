package cmd_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestASecretRefIsAReferenceOnEveryRoad: a connection's `secret_ref` is a
// reference, `<scheme>:<rest>`, from a file, at the box and over the wire
// alike; a value with no scheme is refused without being said back, and
// nothing of it reaches a row or the trail. Which schemes a front door reads
// is the front door's: `env:NAME`, `file:/run/secrets/x` and a word of a
// deployment's own all pass.
//
// # What it was
//
// `secret_ref: hunter2-the-secret-itself` was stored in the row and in the
// trail's patch. #276 stopped the front doors repeating it in a log when they
// could not read it; the row and the trail still took it, from a file, from
// the box and over the wire. What is written where a reference belongs and is
// not one is, more often than anything else, the secret itself.
func TestASecretRefIsAReferenceOnEveryRoad(t *testing.T) {
	const secret = "hunter2-the-secret-itself"

	trailHolds := func(t *testing.T, s *cmd.Server) {
		t.Helper()
		vs, err := s.Ent.Audit.Query().All(t.Context())
		require.NoError(t, err)
		for _, v := range vs {
			require.False(t, bytes.Contains(v.Patch, []byte(secret)), "the trail holds the secret: %s", v.Action)
			require.False(t, bytes.Contains(v.Value, []byte(secret)), "the trail holds the secret: %s", v.Action)
		}
	}

	t.Run("a reference in any front door's words passes", func(t *testing.T) {
		x := require.New(t)
		for _, ref := range []string{"env:NAME", "file:/run/secrets/x", "vault:kv/idp"} {
			paths := declare(t, "resources:\n  - kind: Tenant\n    alias: newco\n  - kind: Connection\n    tenant: newco\n    name: entra\n"+
				"    issuer: https://idp.example.test\n    client_id: abc\n    secret_ref: "+ref+"\n")
			_, err := cmd.ReadResources(paths)
			x.NoError(err, ref)
		}

		s, c, out := adminDeployment(t, nil)
		conn, as := adminPort(t, s, c, out)
		tn, err := app.NewTenantServiceClient(conn).Add(as, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
		x.NoError(err)
		for i, ref := range []string{"env:NAME", "file:/run/secrets/x", "vault:kv/idp"} {
			_, err := app.NewConnectionServiceClient(conn).Add(as, app.ConnectionAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(), Name: []string{"one", "two", "three"}[i],
				Issuer: "https://idp.example.test", ClientId: "abc", SecretRef: ref,
			}.Build())
			x.NoError(err, ref)
		}
	})

	t.Run("from a file", func(t *testing.T) {
		x := require.New(t)
		paths := declare(t, "resources:\n  - kind: Tenant\n    alias: newco\n  - kind: Connection\n    tenant: newco\n    name: entra\n"+
			"    issuer: https://idp.example.test\n    client_id: abc\n    secret_ref: "+secret+"\n")
		_, err := cmd.ReadResources(paths)
		x.Error(err)
		x.ErrorContains(err, paths[0]+":9:5: ")
		x.ErrorContains(err, "secret_ref")
		x.ErrorContains(err, "a scheme, a colon and the rest")
		x.ErrorContains(err, "env:NAME")
		x.NotContains(err.Error(), secret, "the refusal said the secret back")
	})

	t.Run("at the box", func(t *testing.T) {
		x := require.New(t)
		b := atTheBox(t)
		out, err := entities(t, at(b.c, "admin"), "connection", "add",
			`{"tenant":{"alias":"newco"},"name":"entra","issuer":"https://idp.example.test","client_id":"abc","secret_ref":"`+secret+`"}`)
		x.Error(err, out)
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.NotContains(err.Error(), secret, "the refusal said the secret back")

		s, err := cmd.Build(t.Context(), b.c)
		x.NoError(err)
		defer s.Close()
		n, err := s.Ent.Connection.Query().Count(t.Context())
		x.NoError(err)
		x.Zero(n)
		trailHolds(t, s)
	})

	t.Run("over the wire", func(t *testing.T) {
		x := require.New(t)
		s, c, out := adminDeployment(t, nil)
		conn, as := adminPort(t, s, c, out)

		tn, err := app.NewTenantServiceClient(conn).Add(as, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
		x.NoError(err)
		at := app.TenantRef_builder{Id: tn.GetId()}.Build()
		connections := app.NewConnectionServiceClient(conn)

		_, err = connections.Add(as, app.ConnectionAddRequest_builder{
			Tenant: at, Name: "entra", Issuer: "https://idp.example.test", ClientId: "abc", SecretRef: secret,
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.NotContains(err.Error(), secret, "the refusal said the secret back")

		v, err := connections.Add(as, app.ConnectionAddRequest_builder{
			Tenant: at, Name: "entra", Issuer: "https://idp.example.test", ClientId: "abc", SecretRef: "env:ENTRA",
		}.Build())
		x.NoError(err)

		ref := secret
		_, err = connections.Update(as, app.ConnectionUpdateRequest_builder{
			Ref: app.ConnectionRef_builder{Id: v.GetId()}.Build(), SecretRef: &ref, DateUpdated: v.GetDateUpdated(),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		x.NotContains(err.Error(), secret, "the refusal said the secret back")

		trailHolds(t, s)
	})
}
