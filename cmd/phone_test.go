package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestANumberIsSomebodysAsAnAddressIs: a `Phone` is a number kept as it was
// said, one row a person, a number and a kind, and held to being something
// somebody could dial -- and written onto somebody, and taken from them, by
// whoever may write a way into their account.
//
// It was asked nothing about whose account, on the grounds that nothing is
// sent to a number yet (#274). A number says who somebody is to whoever reads
// it, as an address does, and is where a code is sent the day one is; the rule
// is there before the first thing that depends on it.
func TestANumberIsSomebodysAsAnAddressIs(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	erin, fred := b.holder(t, ctx, b.Contoso, "erin"), b.holder(t, ctx, b.Contoso, "fred")
	ref := func(k interface{ Bytes() []byte }) *app.HolderRef {
		return app.HolderRef_builder{Id: k.Bytes()}.Build()
	}

	add := func(who *app.HolderRef, number, kind string) error {
		_, err := b.Walled.Phone().Add(admin, app.PhoneAddRequest_builder{Holder: who, Number: number, Kind: kind}.Build())

		return err
	}

	t.Run("kept as it was said, of a kind or of none", func(t *testing.T) {
		x := require.New(t)
		x.NoError(add(ref(erin), "+82 2-123-4567", "work"))
		x.NoError(add(ref(erin), "(555) 010-0199 x12", ""))

		vs, err := b.Walled.Phone().List(admin, app.PhoneListRequest_builder{
			Filters: []*app.PhoneFilter{app.PhoneFilter_builder{Holder: ref(erin)}.Build()},
		}.Build())
		x.NoError(err)
		got := map[string]string{}
		for _, v := range vs.GetItems() {
			got[v.GetNumber()] = v.GetKind()
		}
		x.Equal(map[string]string{"+82 2-123-4567": "work", "(555) 010-0199 x12": ""}, got)
	})

	t.Run("held to being a number", func(t *testing.T) {
		for _, tc := range []struct{ number, kind string }{
			{"", "work"},
			{" 555 0100", "work"},
			{"ask at reception", "work"},
			{"555 0100", "desk"},
			{"555 0100\n555 0101", ""},
		} {
			err := add(ref(erin), tc.number, tc.kind)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%q, %q: %v", tc.number, tc.kind, err)
		}
	})

	t.Run("listed once as what it is, as each thing it is, and by as many people as answer it", func(t *testing.T) {
		x := require.New(t)
		err := add(ref(erin), "+82 2-123-4567", "work")
		x.Equal(codes.AlreadyExists, status.Code(err), "%v", err)
		x.NoError(add(ref(erin), "+82 2-123-4567", "mobile"), "the mobile that is where their desk rings is two of theirs")
		x.NoError(add(ref(fred), "+82 2-123-4567", "work"), "a switchboard is two people's number")
	})

	t.Run("and held to the rule a way in is", func(t *testing.T) {
		x := require.New(t)
		// Somebody who may write and erase numbers and addresses, and holds
		// nothing else.
		clerk := b.holder(t, ctx, b.Contoso, "clerk")
		b.binds(t, clerk, b.role(t, ctx, "contacts",
			app.PhoneService_Add_FullMethodName, app.PhoneService_Erase_FullMethodName,
			app.EmailService_Add_FullMethodName), nil)
		as := b.asNobody(ctx, clerk, b.Contoso)

		_, err := b.Walled.Email().Add(as, app.EmailAddRequest_builder{
			Holder: ref(b.ContosoUser), Address: "clerk@contoso.example",
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "an address on an administrator is a way into their account: %v", err)

		_, err = b.Walled.Phone().Add(as, app.PhoneAddRequest_builder{
			Holder: ref(b.ContosoUser), Number: "555 0100", Kind: "work",
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "and so is a number there: %v", err)

		// Nor is the administrator's taken away.
		theirs, err := b.Ungated.Phone().Add(ctx, app.PhoneAddRequest_builder{
			Holder: ref(b.ContosoUser), Number: "555 0100", Kind: "work",
		}.Build())
		x.NoError(err)
		_, err = b.Walled.Phone().Erase(as, app.PhoneRef_builder{Id: theirs.GetId()}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		_, err = b.Ungated.Phone().Get(ctx, app.PhoneGetRequest_builder{Ref: app.PhoneRef_builder{Id: theirs.GetId()}.Build()}.Build())
		x.NoError(err, "a number refused its erase is gone")

		// Somebody who holds nothing the clerk does not: written, and taken
		// away.
		v, err := b.Walled.Phone().Add(as, app.PhoneAddRequest_builder{
			Holder: ref(fred), Number: "555 0101", Kind: "home",
		}.Build())
		x.NoError(err)
		gone, err := b.Walled.Phone().Erase(as, app.PhoneRef_builder{Id: v.GetId()}.Build())
		x.NoError(err)
		x.True(gone.GetErased())

		// And the clerk's own, as anybody's own is.
		v, err = b.Walled.Phone().Add(as, app.PhoneAddRequest_builder{
			Holder: ref(clerk), Number: "555 0102", Kind: "mobile",
		}.Build())
		x.NoError(err)
		gone, err = b.Walled.Phone().Erase(as, app.PhoneRef_builder{Id: v.GetId()}.Build())
		x.NoError(err)
		x.True(gone.GetErased())

		// What is not there is erased by nobody, as it always was.
		gone, err = b.Walled.Phone().Erase(as, app.PhoneRef_builder{Id: clerk.Bytes()}.Build())
		x.NoError(err)
		x.False(gone.GetErased())
	})
}
