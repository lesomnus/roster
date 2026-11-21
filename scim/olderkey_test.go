package scim_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// TestAKeyThatLacksAMethodIsRefusedAndNobodyIsSaidGone: a key minted before
// the endpoint called a method it now calls -- or one an administrator
// narrowed -- is answered `403` with roster's sentence naming the method for
// the request that needs it. Never `404`, and never a list of nobody: a
// directory reads either as *this user is gone*, and acts on it.
//
// The manual said the endpoint does what the key allows and says in its log
// what it could not, which holds for a number, who somebody reports to and a
// group's erase, and for nothing else. What the rest do is this, and the
// manual says it now. Held here for two reads a lookup is made of.
func TestAKeyThatLacksAMethodIsRefusedAndNobodyIsSaidGone(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	gina := d.someone(t, "gina", ginaOid)

	self, err := d.s.Ungated.Holder().Get(t.Context(), rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{
			Alias: proto.String("scim"), Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	minted := 0
	without := func(m string) string {
		minted++

		return d.mint(t, self.GetId(), fmt.Sprintf("without-%d", minted), slices.DeleteFunc(slices.Clone(scim.Methods), func(v string) bool { return v == m }))
	}

	for m, path := range map[string]string{
		// Reading somebody back by the id it was answered with.
		rstr.HolderService_Get_FullMethodName: scim.Base + "/Users/" + gina,
		// Looking somebody up by what the directory calls them.
		rstr.IdentityService_Get_FullMethodName: filter("externalId", ginaOid),
	} {
		code, got, _ := d.call(t, without(m), http.MethodGet, path, nil)
		x.Equal(http.StatusForbidden, code, "%s: %v", m, got)
		x.Contains(got["detail"], m, "the sentence names what the key lacks")
		x.NotContains(got, "totalResults", "%s: no list of nobody", m)
	}
}
