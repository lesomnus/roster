package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestTheAdminListenerAnswersNoVerifier: what is kept of a key is the hash it
// is checked against, and no port answers it -- the admin listener neither,
// though everybody at it is an operator.
//
// `cmd/admin.go` has said so since the port was made: *a port being private is
// not a reason to serve a password hash over it*. What it shut was the
// generated reads of a `Credential`. A key's `Get` and `List` are served, and
// what clears `secret` from their answers on the data plane is a layer the
// admin listener's stack was built without: so `ApiKey.Get` with
// `select.secret` answered the hash there, and `""` at the port a customer
// reaches (#274).
func TestTheAdminListenerAnswersNoVerifier(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)

	c := signIn(t, s, "admin", passwordFrom(t, out))
	x.NotNil(c)

	g, err := s.GrpcAdmin(ctx, cmd.Config{})
	x.NoError(err)
	admin := pdtest.Serve(t, g)
	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value))

	tn, err := app.NewTenantServiceClient(admin).Add(as, app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)

	keys := app.NewApiKeyServiceClient(admin)
	minted, err := keys.Issue(as, app.ApiKeyIssueRequest_builder{
		Holder: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Alias:  z.Ptr("admin"),
				Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
			}.Build(),
		}.Build(),
		Alias:   "laptop",
		Methods: []string{"/roster.MeService/Get"},
	}.Build())
	x.NoError(err)
	x.NotEmpty(minted.GetToken())

	// The row is there with its hash, which is what there is to answer.
	kept, err := s.Ungated.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
		Ref:    app.ApiKeyRef_builder{Id: minted.GetKey().GetId()}.Build(),
		Select: app.ApiKeySelect_builder{Secret: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.NotEmpty(kept.GetSecret(), "the row keeps no verifier, so this would prove nothing")

	t.Run("not when it is asked for by name", func(t *testing.T) {
		x := require.New(t)

		got, err := keys.Get(as, app.ApiKeyGetRequest_builder{
			Ref:    app.ApiKeyRef_builder{Id: minted.GetKey().GetId()}.Build(),
			Select: app.ApiKeySelect_builder{Secret: z.Ptr(true), Alias: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("laptop", got.GetAlias())
		x.Empty(got.GetSecret(), "the admin listener answered a key's stored verifier")
	})

	t.Run("nor in a list of everything", func(t *testing.T) {
		x := require.New(t)

		vs, err := keys.List(as, app.ApiKeyListRequest_builder{}.Build())
		x.NoError(err)
		x.NotEmpty(vs.GetItems())
		for _, v := range vs.GetItems() {
			x.Empty(v.GetSecret(), "the admin listener listed a key's stored verifier")
		}
	})

	// What it is for still works: the key signs its holder in, which is the
	// hash being read where it is checked and nowhere else.
	t.Run("and the key is still one", func(t *testing.T) {
		x := require.New(t)

		_, err := s.Ungated.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
			Ref: app.ApiKeyRef_builder{Id: minted.GetKey().GetId()}.Build(),
		}.Build())
		x.Equal(codes.OK, status.Code(err))
	})
}
