package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestAKeyThatIsRefusedMakesNobody: on the control plane a key is issued for a
// holder by alias, made if they are not there -- naming a caller is what makes
// one. So a key that is refused has to be refused before that.
//
// It was not. The holder was made first, and then the key's methods were held
// to what the caller holds: `403`, and somebody left on the control plane whom
// nobody had been allowed to make a key for (#274).
func TestAKeyThatIsRefusedMakesNobody(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, _ := inited(t)

	ts, err := s.Control.Ungated.Tenant().List(ctx, app.TenantListRequest_builder{Size: 1}.Build())
	x.NoError(err)
	owner := app.TenantRef_builder{Id: ts.GetItems()[0].GetId()}.Build()

	// A caller of the deployment's own that may issue keys and holds nothing
	// else, so that a key for anything is more than it has to hand out.
	svc, err := s.Control.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: owner, Alias: "issuer"}.Build())
	x.NoError(err)
	token, sum, err := keys.Mint(keys.PrefixDeployment)
	x.NoError(err)
	_, err = s.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: svc.GetId()}.Build(),
		Alias:   "issues",
		Secret:  sum,
		Methods: []string{app.ApiKeyService_Issue_FullMethodName},
	}.Build())
	x.NoError(err)

	holders := func() []string {
		vs, err := s.Control.Ungated.Holder().List(ctx, app.HolderListRequest_builder{}.Build())
		x.NoError(err)

		var out []string
		for _, v := range vs.GetItems() {
			out = append(out, v.GetAlias())
		}

		return out
	}
	before := holders()

	_, err = app.NewApiKeyServiceClient(servedControl(t, s)).Issue(bearing(ctx, token), app.ApiKeyIssueRequest_builder{
		HolderAlias: "left-behind",
		Alias:       "theirs",
		Methods:     []string{"/roster.HolderService/Get"},
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

	x.ElementsMatch(before, holders(), "a key that was refused left the holder it named")
}
