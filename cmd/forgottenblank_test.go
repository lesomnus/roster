package cmd_test

import (
	"context"
	"image/color"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/trail"

	"github.com/lesomnus/roster/internal/ent"
	entemail "github.com/lesomnus/roster/internal/ent/email"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	entlink "github.com/lesomnus/roster/internal/ent/link"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/keys"
)

// saysOf is what a holder's row says in each field its schema declares
// personal, by the field's name: true where it says anything. Read off the
// schema rather than written here, so a field declared personal tomorrow is
// asked about without anybody remembering to.
func saysOf(v *ent.Holder) map[string]bool {
	m := v.Proto().ProtoReflect()

	out := map[string]bool{}
	fs := m.Descriptor().Fields()
	for i := range fs.Len() {
		f := fs.Get(i)
		if o, _ := proto.GetExtension(f.Options(), pdpb.E_Field).(*pdpb.Field); !o.GetPersonal() {
			continue
		}

		name := string(f.Name())
		switch {
		case f.IsMap():
			out[name] = m.Get(f).Map().Len() > 0
		case f.IsList():
			out[name] = m.Get(f).List().Len() > 0
		case f.Kind() == protoreflect.MessageKind:
			out[name] = m.Has(f)
		default:
			out[name] = !m.Get(f).Equal(f.Default())
		}
	}

	return out
}

// known is somebody of `in` with every personal field of their row said: the
// names an operator gave them, a profile, what an app keeps of them, a
// picture, and what their directory calls them.
func (b *built) known(t *testing.T, ctx context.Context, in pdid.Id, alias string) pdid.Id {
	t.Helper()
	x := require.New(t)
	at := app.TenantRef_builder{Id: in.Bytes()}.Build()

	// What a directory says of somebody is taken where a tenant has one.
	_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/" + alias + "/v2.0", ClientId: "the-app",
		Provisions: true,
	}.Build())
	x.NoError(err)

	data, err := anypb.New(wrapperspb.String("badge 4417"))
	x.NoError(err)
	v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant:  at,
		Alias:   alias,
		Name:    "Hana Kim",
		Desc:    "night shift, third floor",
		Labels:  map[string]string{"badge": "4417"},
		Profile: app.Profile_builder{DisplayName: "Hana", Department: "Assembly", EmployeeNo: "4417"}.Build(),
		Data:    data,
	}.Build())
	x.NoError(err)
	who := mustId(t, v.GetId())
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()

	_, err = b.Ungated.Holder().Tell(ctx, app.HolderTellRequest_builder{
		Ref: ref,
		Entry: app.DirectoryEntry_builder{
			Subject: z.Ptr("subject-of-" + alias),
			Address: z.Ptr(alias + ".kim@corp.example"),
			Name:    z.Ptr(alias + ".kim@upn.example"),
		}.Build(),
	}.Build())
	x.NoError(err)

	v, err = b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	_, err = b.Ungated.Holder().Portray(ctx, app.HolderPortrayRequest_builder{
		Ref: ref, DateUpdated: v.GetDateUpdated(), Image: face(t, 64, color.NRGBA{20, 120, 60, 255}),
	}.Build())
	x.NoError(err)

	row, err := b.Ent.Holder.Get(ctx, who.Uuid())
	x.NoError(err)
	said := saysOf(row)
	x.NotEmpty(said, "the schema declares nothing of a holder personal, so this proves nothing")
	for name, says := range said {
		x.True(says, "%s is declared personal and this never set it, so nothing is proved about it", name)
	}

	return who
}

// TestSomebodyForgottenIsABlankRow: the manual says a forgotten person's row
// "stays, blank" -- a pseudonym reaching nothing -- and `forget.Forget`
// blanked the six columns a holder had when it was written: the alias, the
// name, the description, the labels, the profile and `data`. The four added
// since stayed, each declared personal: their picture, and what their
// directory called them -- its subject for them, the address it last said and
// the name it looks them up by. Read from the store, a forgotten row answered
// a portrait at every size and all three strings; right is none of them.
// Destroying a tenant forgets its people by the same function, so the same
// columns stayed there.
//
// Held to the schema, as the list of a tenant's tables is
// (`TestEveryTenantedTableGoesWithItsTenant`): every field of `Holder`
// declared `personal` says nothing on the row that is left, and [known]
// refuses a field it never set. One declared tomorrow and not cleared fails
// here rather than staying in a database and its backups.
func TestSomebodyForgottenIsABlankRow(t *testing.T) {
	b, ctx := build(t)

	blank := func(t *testing.T, who pdid.Id) {
		t.Helper()
		x := require.New(t)

		row, err := b.Ent.Holder.Get(ctx, who.Uuid())
		x.NoError(err)
		x.NotNil(row.DateErased, "they were not forgotten, so this proves nothing")

		x.Nil(row.Portrait, "their picture stayed")
		x.Nil(row.DirectorySubject, "what their directory knows them by stayed")
		x.Nil(row.DirectoryAddress, "the address their directory said stayed")
		x.Nil(row.DirectoryName, "the name their directory looks them up by stayed")

		for name, says := range saysOf(row) {
			x.False(says, "%s is declared personal and a forgotten row still says it", name)
		}

		// What is left is what the row is kept for: the identifier the trail
		// names them by, the tenant it is filed under, and when.
		x.Equal(who.Uuid(), row.Id)
		x.NotEqual(pdid.Nil.Uuid(), row.TenantId)
	}

	t.Run("forgotten by name", func(t *testing.T) {
		who := b.known(t, ctx, b.Contoso, "hana")

		_, err := forget.Forget(ctx, b.Ent, who, trail.Policy{}, audit.Contents{}, nil)
		require.NoError(t, err)
		blank(t, who)
	})

	t.Run("and with their tenant", func(t *testing.T) {
		who := b.known(t, ctx, b.Fabrikam, "ivan")

		closes(t, ctx, b.Ungated, b.Fabrikam)
		_, err := forget.DestroyTenant(ctx, b.Ent, b.Fabrikam, trail.Policy{}, audit.Contents{}, nil)
		require.NoError(t, err)
		blank(t, who)

		// And nobody of the tenant is left saying anything.
		rows, err := b.Ent.Holder.Query().Where(entholder.TenantId(b.Fabrikam.Uuid())).All(ctx)
		require.NoError(t, err)
		for _, row := range rows {
			for name, says := range saysOf(row) {
				require.False(t, says, "%s of somebody in a destroyed tenant still says something", name)
			}
		}
	})
}

// TestALinkThatProvesAnAddressGoesWithTheAddress: a link that proves an
// address is told from a link that signs somebody in by the address it names
// (`email_svc.ext.proto`), and that edge is `ON DELETE SET NULL`. Forgetting
// somebody destroys their addresses, so a claim of one of them that was in
// somebody's mailbox named no address from then on, and read as a sign-in
// link for whoever claimed it: `VouchService/Redeem` refused the token while
// the address was there, and answered `ok` with a delegation for the claimant
// once its holder was forgotten. Right is the refusal both times.
//
// A link that names an address has nothing to prove once the address is
// destroyed, so it is destroyed with it, in the transaction that takes the
// address: there is no moment the row is there and names nothing.
func TestALinkThatProvesAnAddressGoesWithTheAddress(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, claimEmail, link, redeem, listHolders)
	ctx := t.Context()
	as := bearing(ctx, b.Token)
	c := app.NewVouchServiceClient(b.Conn)

	// Somebody holds an address nobody proved, and somebody else claims it:
	// the link is the claimant's, and names the address's row.
	holds := addHolder(t, ctx, b.Server, b.Contoso, "holds")
	row, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder: app.HolderRef_builder{Id: holds.Bytes()}.Build(), Address: "wanted@contoso.example",
	}.Build())
	x.NoError(err)

	claim, err := app.NewEmailServiceClient(b.Conn).Claim(as, app.EmailClaimRequest_builder{
		Ref:    app.EmailRef_builder{Id: row.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	redeemed := func() *app.VouchDelegateResponse {
		t.Helper()
		res, err := c.Redeem(as, app.VouchRedeemRequest_builder{
			Token: claim.GetToken(), Methods: []string{listHolders},
		}.Build())
		x.NoError(err)

		return res
	}
	linked := func() int {
		t.Helper()
		n, err := b.Ent.Link.Query().Where(entlink.Secret(keys.Sum(claim.GetToken()))).Count(ctx)
		x.NoError(err)

		return n
	}

	res := redeemed()
	x.False(res.GetVerified().GetOk(), "a link that proves an address signed somebody in")
	x.Empty(res.GetToken())
	x.Equal(1, linked(), "the claim was spent by the door that refused it, so this proves nothing")

	// Whoever held the address is forgotten, and the address with them.
	_, err = forget.Forget(ctx, b.Ent, holds, trail.Policy{}, audit.Contents{}, nil)
	x.NoError(err)
	n, err := b.Ent.Email.Query().Where(entemail.IdEQ(mustId(t, row.GetId()).Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(n, "the address was not destroyed, so this proves nothing")

	res = redeemed()
	x.False(res.GetVerified().GetOk(), "a claim of an address that was destroyed signed its claimant in")
	x.Empty(res.GetToken(), "and was answered a delegation")
	x.Zero(linked(), "a link that names an address outlived the address")

	// Nobody else's link moved: one that signs the claimant in is still theirs.
	other, err := c.Link(as, app.VouchLinkRequest_builder{Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build()}.Build())
	x.NoError(err)
	_, err = forget.Forget(ctx, b.Ent, addHolder(t, ctx, b.Server, b.Contoso, "bystander"), trail.Policy{}, audit.Contents{}, nil)
	x.NoError(err)
	n, err = b.Ent.Link.Query().Where(entlink.Secret(keys.Sum(other.GetToken()))).Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "a link of somebody else's went with a forgotten person")
}
