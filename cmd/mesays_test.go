package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/roster/rstr"
)

// TestMeSaysAddressesInTheOrderTheyWereProved: somebody's addresses are
// answered proved ones first, in the order they were proved, and then the
// rest.
//
// The read asked for no order, so the answer was in whatever order the store
// kept -- and the first proved address of it is what a front door puts in a
// token as `email` (`login/claims.go`), which a relying party keys accounts on.
// Here the answer led with `abe@`, which nobody had proved, and its first
// proved address was `bob@`, the one proved last; right is `eve@`, proved
// first. Two proved at one instant are in the order of their rows, so that
// there is one answer, and an address nobody proved comes after every one
// somebody did.
func TestMeSaysAddressesInTheOrderTheyWereProved(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	began := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	add := func(address string) *app.Email {
		t.Helper()
		v, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(), Address: address + "@contoso.example",
		}.Build())
		require.NoError(t, err)

		return v
	}
	proved := func(v *app.Email, after time.Duration) {
		t.Helper()
		_, err := b.Ungated.Email().Patch(ctx, app.EmailPatchRequest_builder{
			Ref:          app.EmailRef_builder{Id: v.GetId()}.Build(),
			DateVerified: timestamppb.New(began.Add(after)),
			DateUpdated:  v.GetDateUpdated(),
		}.Build())
		require.NoError(t, err)
	}

	// Written in one order, spelled in another and proved in a third; with two
	// nobody proved, two proved at one instant, and one proved before any of
	// them that is erased.
	gone := add("aaa")
	last := add("bob")
	add("zoe")
	tied := add("cat")
	add("abe")
	first := add("eve")
	tiedToo := add("fay")
	proved(gone, time.Minute)
	proved(first, time.Hour)
	proved(tiedToo, 2*time.Hour)
	proved(tied, 2*time.Hour)
	proved(last, 3*time.Hour)
	_, err := b.Ungated.Email().Erase(ctx, app.EmailRef_builder{Id: gone.GetId()}.Build())
	x.NoError(err)

	v, err := app.NewMeServiceClient(served(t, b.Server)).Get(asOverTheWire(ctx, b.ContosoUser), app.MeGetRequest_builder{}.Build())
	x.NoError(err)

	got := []string{}
	for _, m := range v.GetEmails() {
		got = append(got, m.GetAddress())
	}
	x.Equal([]string{
		"eve@contoso.example", // proved first
		"cat@contoso.example", // proved with `fay@`, and the earlier row
		"fay@contoso.example",
		"bob@contoso.example", // proved last
		"zoe@contoso.example", // nobody proved these two: as they were written
		"abe@contoso.example",
	}, got)

	x.NotNil(v.GetEmails()[3].GetDateVerified())
	x.Nil(v.GetEmails()[4].GetDateVerified(), "an address nobody proved is answered as one")
}
