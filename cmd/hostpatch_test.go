package cmd_test

import (
	"strconv"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// TestAHostStaysAsItIsComparedWhenItIsRenamed is the half of the rule that
// nothing was holding.
//
// `Host.Add` has been refusing an unnormalised name since the day it was
// written, and `TestAHostIsStoredAsItIsCompared` says why. `Patch` carries the
// identical guard and had nothing looking at it at all -- so the rule was
// enforced exactly once, at the moment a row was created, and the very next
// write could put the row back into the state the guard exists to prevent.
//
// That is the worse of the two failures, not the milder one. A name refused at
// `Add` is refused in front of whoever is typing it; a name accepted at `Patch`
// belongs to a tenant that was **working a minute ago** and has now gone dark,
// with a console still listing the name its operator just entered. The row is
// there, it is spelled the way they meant, and `FrontService.WhoseHost`
// normalises the name a browser arrived at and never reaches it.
func TestAHostStaysAsItIsComparedWhenItIsRenamed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:   "contoso.example.com",
	}.Build())
	x.NoError(err)

	// Every refusal below declines the version check rather than omitting it,
	// and that is deliberate. A patch carrying no version at all is refused as
	// `InvalidArgument` for exactly that -- the same code, from a check with
	// nothing to do with this one -- so a test written the lazy way would go on
	// passing with the guard deleted. Forcing leaves the name as the only thing
	// the server can still object to, and it leaves each case below
	// independent of whether the one before it wrote anything.
	rename := func(to string) error {
		t.Helper()

		_, err := b.Ungated.Host().Patch(ctx, app.HostPatchRequest_builder{
			Ref:              v.Ref(),
			Name:             z.Ptr(to),
			DateUpdatedForce: z.Ptr(true),
		}.Build())

		return err
	}

	for _, tc := range []struct{ desc, name, want string }{
		{"upper case and a port at once", "Contoso.Example.com:8443", "contoso.example.com"},
		{"upper case", "CONTOSO.example.com", "contoso.example.com"},
		{"a port", "contoso.example.com:8443", "contoso.example.com"},
		{"whitespace", " contoso.example.com ", "contoso.example.com"},

		// The brackets are not part of the name. They exist only to tell a
		// colon inside an address from the colon before a port, so
		// `[::1]` and `::1` are one name spelled two ways -- and the one a
		// request can arrive as is the bare one, because that is what
		// [front.Hostname] answers with.
		{"an address literal in brackets", "[::1]", "::1"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			err := rename(tc.name)
			x.Equal(codes.InvalidArgument, status.Code(err))

			// The message names what it should have been. Fixing it quietly is
			// the alternative and it is worse: the caller gets back a row that
			// differs from what it wrote, and the console that wrote it cannot
			// find the name the person just typed.
			// Quoted, which is what makes this an assertion. `normalised` says
			// *stored as it is compared, so %q rather than %q*, so the message
			// holds both forms -- and for half of these the normal form is a
			// substring of what was typed, so an unquoted `Contains` is
			// satisfied by a message that only echoes the input back.
			x.Contains(status.Convert(err).Message(), strconv.Quote(tc.want),
				"the refusal did not name the form it should have been stored as")
		})
	}

	// A name is not something a patch may take away. `HasName` is what decides
	// whether the guard looks at all, and an empty string is present -- so this
	// is the case that separates "the caller said nothing about the name" from
	// "the caller said the name is nothing", which a nameless host row would
	// otherwise be the result of.
	//
	// The second spelling is the same row by a different route: a name that is
	// only whitespace normalises to nothing, which is not a value that can be
	// stored and compared either, and it is what a form submitted with a
	// stray space in an otherwise empty box sends.
	t.Run("and it cannot be emptied, in either spelling", func(t *testing.T) {
		x := require.New(t)

		for _, name := range []string{"", "   "} {
			err := rename(name)
			x.Equal(codes.InvalidArgument, status.Code(err), "%q", name)
		}
	})

	// The other side of `HasName`. A patch that touches only the description
	// must not be measured against a name it never mentioned -- if it were,
	// every edit to any other field on a host would be refused for having an
	// empty one, and the guard would have made the entity unpatchable.
	t.Run("and a patch that says nothing about it is untouched", func(t *testing.T) {
		x := require.New(t)

		u, err := b.Ungated.Host().Patch(ctx, app.HostPatchRequest_builder{
			Ref:         v.Ref(),
			Desc:        z.Ptr("the marketing site"),
			DateUpdated: v.GetDateUpdated(),
		}.Build())
		x.NoError(err)
		x.Equal("contoso.example.com", u.GetName())

		v = u
	})

	// And a name that is already what it will be compared as goes through, and
	// is stored exactly as written. This is what makes the refusals above a
	// guard rather than a wall: renaming a host is a thing an operator does.
	t.Run("and one already normal is stored as written", func(t *testing.T) {
		x := require.New(t)

		u, err := b.Ungated.Host().Patch(ctx, app.HostPatchRequest_builder{
			Ref:         v.Ref(),
			Name:        z.Ptr("shop.contoso.example.com"),
			DateUpdated: v.GetDateUpdated(),
		}.Build())
		x.NoError(err)
		x.Equal("shop.contoso.example.com", u.GetName())

		got, err := b.Ungated.Host().Get(ctx, app.HostGetRequest_builder{
			Ref:    app.HostRef_builder{Name: z.Ptr("shop.contoso.example.com")}.Build(),
			Select: app.HostSelect_builder{All: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("shop.contoso.example.com", got.GetName())
	})
}

// TestAMailDomainStaysAsItIsComparedWhenItIsRenamed is the same rule on the
// other name a front door looks up, and it was unpinned for the same reason.
//
// `FrontService.WhereFrom` takes an address, cuts it at the last `@` and
// lowercases what is left, so a routing row stored as `CONTOSO.example` is a row
// no address ever matches. The failure is quieter than the host one:
// `WhereFrom` answers an unknown domain with an **empty provider** rather than
// `NotFound`, on purpose, so that it cannot be asked whether a domain exists
// here. Which means a tenant whose routing was renamed into a form nothing
// matches does not get an error anywhere -- everybody at that domain is simply
// offered the default sign-in instead of their own directory.
func TestAMailDomainStaysAsItIsComparedWhenItIsRenamed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.MailDomain().Add(ctx, app.MailDomainAddRequest_builder{
		Tenant:   app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:     "contoso.example",
		Provider: "entra",
	}.Build())
	x.NoError(err)

	// As above: the version check is declined rather than omitted, so that an
	// `InvalidArgument` here can only have come from the name.
	rename := func(to string) error {
		t.Helper()

		_, err := b.Ungated.MailDomain().Patch(ctx, app.MailDomainPatchRequest_builder{
			Ref:              v.Ref(),
			Name:             z.Ptr(to),
			DateUpdatedForce: z.Ptr(true),
		}.Build())

		return err
	}

	for _, tc := range []struct{ desc, name, want string }{
		{"upper case", "CONTOSO.example", "contoso.example"},
		{"whitespace", " contoso.example ", "contoso.example"},

		// A whole address is the commonest thing to paste into this field, and
		// what is looked up is the part after the `@`. Storing the address
		// would be storing a row keyed by one person.
		{"a whole address", "somebody@contoso.example", "contoso.example"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			err := rename(tc.name)
			x.Equal(codes.InvalidArgument, status.Code(err))
			// Quoted, which is what makes this an assertion. `normalised` says
			// *stored as it is compared, so %q rather than %q*, so the message
			// holds both forms -- and for half of these the normal form is a
			// substring of what was typed, so an unquoted `Contains` is
			// satisfied by a message that only echoes the input back.
			x.Contains(status.Convert(err).Message(), strconv.Quote(tc.want),
				"the refusal did not name the form it should have been stored as")
		})
	}

	// As above, and the whitespace spelling reaches it the same way: `Domain`
	// trims before it looks for the `@`, so a box with a space in it is a
	// domain of nothing.
	t.Run("and it cannot be emptied, in either spelling", func(t *testing.T) {
		x := require.New(t)

		for _, name := range []string{"", "   "} {
			err := rename(name)
			x.Equal(codes.InvalidArgument, status.Code(err), "%q", name)
		}
	})

	// A patch that only moves the routing must not be refused for a name it
	// never mentioned -- and this is the patch an operator actually makes,
	// because changing directory is why a mail domain is edited at all.
	t.Run("and repointing the provider says nothing about it", func(t *testing.T) {
		x := require.New(t)

		u, err := b.Ungated.MailDomain().Patch(ctx, app.MailDomainPatchRequest_builder{
			Ref:         v.Ref(),
			Provider:    z.Ptr("okta"),
			DateUpdated: v.GetDateUpdated(),
		}.Build())
		x.NoError(err)
		x.Equal("contoso.example", u.GetName())
		x.Equal("okta", u.GetProvider())

		v = u
	})

	// And the rename an operator meant goes through, and the front door finds
	// it afterwards -- which is the property all of the above is protecting,
	// stated once rather than assumed.
	t.Run("and one already normal is what a front door then finds", func(t *testing.T) {
		x := require.New(t)

		// The version is declined here as it is in `rename`, and the provider
		// is read off the answer rather than written down. Both are so that
		// this stands on its own: it ran green in the file and red under
		// `-run` of its own name, because it was asserting the provider a
		// sibling subtest had moved and the version that sibling had left.
		// A test that only passes with its neighbours is one that says nothing
		// about the line it names.
		u, err := b.Ungated.MailDomain().Patch(ctx, app.MailDomainPatchRequest_builder{
			Ref:              v.Ref(),
			Name:             z.Ptr("contoso.com"),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		x.NoError(err)
		x.Equal("contoso.com", u.GetName())

		f := front.New(b.Ungated)

		res, err := f.WhereFrom(ctx, app.FrontWhereFromRequest_builder{
			Tenant: b.Contoso.Bytes(), Address: "somebody@contoso.com",
		}.Build())
		x.NoError(err)
		x.Equal(u.GetProvider(), res.GetProvider(),
			"the row was renamed and the front door found something else")
		x.NotEmpty(res.GetProvider())
	})
}
