package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	entaccess "github.com/lesomnus/roster/internal/ent/access"
	app "github.com/lesomnus/roster/rstr"
)

// TestWhoReadOrWroteWhoseDataIsRecorded is payday#111 on roster's data plane,
// through the chain a caller reaches: a list of people is a row of the access
// record naming each of them, filed under their tenant and naming who asked;
// and a write of somebody's address is a row of the write, without waiting for
// the second the reads wait for.
func TestWhoReadOrWroteWhoseDataIsRecorded(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	alice := b.holder(t, ctx, b.Contoso, "alice")
	b.mayAnything(b.ContosoUser, b.Contoso)

	conn := served(t, b.Server)
	as := asOverTheWire(ctx, b.ContosoUser)

	vs, err := app.NewHolderServiceClient(conn).List(as, app.HolderListRequest_builder{}.Build())
	x.NoError(err)
	x.NotEmpty(vs.GetItems())

	x.NoError(b.Access.Flush(ctx))
	rs, err := b.Ent.Access.Query().All(ctx)
	x.NoError(err)
	x.Len(rs, 1)
	x.Equal("read", rs[0].Kind)
	x.Equal(app.HolderService_List_FullMethodName, rs[0].Action)
	x.Equal(b.Contoso.Uuid(), rs[0].TenantId)
	x.Equal(b.ContosoUser.Uuid(), rs[0].ActorId)
	x.Contains(rs[0].Subjects, alice.Bytes())
	x.EqualValues(len(vs.GetItems()), rs[0].SubjectCount)

	t.Run("and a write of somebody's address is a row of the write", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewEmailServiceClient(conn).Add(as, app.EmailAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
			Address: "alice@contoso.example",
		}.Build())
		x.NoError(err)

		// Not queued: written by the commit, or by the interceptor once the
		// call answered.
		x.Zero(b.Access.Pending())
		rs, err := b.Ent.Access.Query().Where(entaccess.KindEQ("write")).All(ctx)
		x.NoError(err)
		x.Len(rs, 1)
		x.Equal(app.EmailService_Add_FullMethodName, rs[0].Action)
		x.Equal([][]byte{v.GetId()}, rs[0].Subjects)
		x.Equal(b.Contoso.Uuid(), rs[0].TenantId)
	})
}
