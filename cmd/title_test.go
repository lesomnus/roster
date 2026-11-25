package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
)

// TestAProfileSaysSomebodysTitle: a job title is a field of the profile,
// beside the department, written with it and read back by whoever reads the
// profile.
//
// An app that keeps who approved something, and in what capacity, took the
// department from roster and had nowhere to take the title from: a profile
// said `display_name` and `department` and no title (#415). Right is
// `Profile.title`, free text as the company writes it, written by the verb
// that writes the rest of a profile and answered wherever the profile is: a
// `Get`, and a page of people.
func TestAProfileSaysSomebodysTitle(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	holders := app.NewHolderServiceClient(served(t, b.Server))

	b.mayAnything(b.ContosoUser, b.Contoso)
	admin := asOverTheWire(ctx, b.ContosoUser)
	who := b.holder(t, ctx, b.Contoso, "title-holder")
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()

	got, err := holders.Get(admin, app.HolderGetRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	v, err := holders.Update(admin, app.HolderUpdateRequest_builder{
		Ref: ref, DateUpdated: got.GetDateUpdated(),
		Profile: app.Profile_builder{DisplayName: "Jun Park", Department: "Payroll", Title: "Senior Accountant"}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("Senior Accountant", v.GetProfile().GetTitle())

	// Read back rather than trusting the answer: an `Update` echoes what it
	// was given, so a value that was never stored still looks right.
	got, err = holders.Get(admin, app.HolderGetRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	x.Equal("Senior Accountant", got.GetProfile().GetTitle())
	x.Equal("Payroll", got.GetProfile().GetDepartment())

	found, err := holders.Search(admin, app.HolderSearchRequest_builder{Department: z.Ptr("Payroll")}.Build())
	x.NoError(err)
	x.Len(found.GetItems(), 1)
	x.Equal("Senior Accountant", found.GetItems()[0].GetProfile().GetTitle(), "a page of people says no title")
}
