package cmd_test

import (
	"strings"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// TestAnAddressIsOneSpelling: an address is written as the plain mailbox it
// is, and anything else is refused where it is written.
//
// An address was lowered and trimmed and held to nothing else. Somebody who
// holds no role wrote each of these onto their own row and was answered OK;
// right is InvalidArgument, naming `address`. Three of them are one mailbox --
// the plain one, the one in brackets, the one with a dot after its name --
// and were three rows; the composed and the decomposed `á` were two. A row is
// where a link is mailed and what a token says somebody's address is, so it
// is one spelling of one mailbox or it is not written.
//
// Refused and not mended, as a capital already was: a caller whose row comes
// back different from what it wrote cannot find what it just wrote.
func TestAnAddressIsOneSpelling(t *testing.T) {
	b, ctx := build(t)
	who := b.holder(t, ctx, b.Contoso, "plain")
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()

	// As themselves and over the wire, holding nothing: an address is
	// somebody's own to write (`self-service`).
	emails := app.NewEmailServiceClient(served(t, b.Server))
	wire := asOverTheWire(ctx, who)
	add := func(address string) error {
		_, err := emails.Add(wire, app.EmailAddRequest_builder{Holder: ref, Address: address}.Build())

		return err
	}

	for _, tc := range []struct {
		name    string
		address string
	}{
		{"two words and no @", "not an address"},
		{"a second @", "a@b@contoso.example"},
		{"a line break inside", "x\ny@contoso.example"},
		{"a name and an address", `"ceo@contoso.example" <plain@elsewhere.example>`},
		{"brackets round it", "<plain@contoso.example>"},
		{"a quoted mailbox", `"plain"@contoso.example`},
		{"a comment", "plain(me)@contoso.example"},
		{"a dot after the name", "plain@contoso.example."},
		{"a dot before the @", "plain.@contoso.example"},
		{"a composed letter", "pláin@contoso.example"},
		{"the same letter decomposed", "pláin@contoso.example"},
		{"a name in its own letters", "plain@bücher.example"},
		{"an address literal", "plain@[192.0.2.1]"},
		{"more than 64 bytes before the @", strings.Repeat("a", front.MaxLocal+1) + "@contoso.example"},
		{"more than 254 bytes", "plain@" + strings.Repeat("a.", 125) + "example"},
		{"a megabyte", strings.Repeat("a", 1<<20) + "@contoso.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := add(tc.address)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
			require.Contains(t, status.Convert(err).Message(), "address")
		})
	}

	t.Run("a capital is refused naming the spelling, as it was", func(t *testing.T) {
		err := add("Plain@Contoso.Example")
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		require.Contains(t, status.Convert(err).Message(), `"plain@contoso.example"`)
	})

	t.Run("the plain mailbox is written, and is the only row", func(t *testing.T) {
		x := require.New(t)
		x.NoError(add("plain@contoso.example"))
		x.NoError(add("o'brien+billing@xn--bcher-kva.example"))

		vs, err := emails.List(wire, app.EmailListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 2)
	})

	t.Run("and is found by what somebody types", func(t *testing.T) {
		x := require.New(t)
		typist := b.holder(t, ctx, b.Contoso, "typist")
		proves(t, ctx, b.Server, typist, "first.last+tag@contoso.example")
		b.sets(t, ctx, typist, "the password they chose")

		for _, typed := range []string{"first.last+tag@contoso.example", " First.Last+Tag@Contoso.Example "} {
			x.True(b.verifiesAt(t, ctx, "contoso", typed, "the password they chose").GetOk(),
				"%q did not reach the row it names", typed)
		}
	})
}

// TestAnAddressIsHeldToItsFormByEveryRoad: what a provider vouches for, what a
// directory provisions and what it says of somebody already here are each an
// address, held to what one somebody typed is.
//
// Each road asked the spelling and nothing else, as `EmailService/Add` did, so
// each wrote `<erin@contoso.example>`: answered OK; right is InvalidArgument.
// The rule is about the value and not about who sends it, so the deployment's
// own writes are held to it too.
func TestAnAddressIsHeldToItsFormByEveryRoad(t *testing.T) {
	const bracketed = "<erin@contoso.example>"

	b, ctx := build(t)
	as := b.directory(t, ctx)

	refused := func(t *testing.T, err error) {
		t.Helper()
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		require.Contains(t, status.Convert(err).Message(), "address")
	}

	t.Run("a directory's Provision", func(t *testing.T) {
		_, err := b.provision(as, "erin", erinOid, bracketed)
		refused(t, err)

		// One write or none: nobody was made for the address that was refused.
		_, err = b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
				Alias: z.Ptr("erin"), Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			}.Build()}.Build(),
		}.Build())
		require.Equal(t, codes.NotFound, status.Code(err), "%v", err)
	})

	h, err := b.provision(as, "erin", erinOid, "erin@contoso.example")
	require.NoError(t, err)
	erin := app.HolderRef_builder{Id: h.GetId()}.Build()

	t.Run("what a directory says of somebody", func(t *testing.T) {
		_, err := b.Walled.Holder().Tell(as, app.HolderTellRequest_builder{Ref: erin, Address: z.Ptr(bracketed)}.Build())
		refused(t, err)
	})

	t.Run("what a provider vouches for", func(t *testing.T) {
		ids, err := b.Ungated.Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{app.IdentityFilter_builder{Holder: erin}.Build()},
		}.Build())
		require.NoError(t, err)
		require.Len(t, ids.GetItems(), 1)

		_, err = b.Ungated.Email().Attest(ctx, app.EmailAttestRequest_builder{
			Holder: erin, Address: bracketed, Verified: true,
			VouchedBy: app.IdentityRef_builder{Id: ids.GetItems()[0].GetId()}.Build(),
		}.Build())
		refused(t, err)
	})

	t.Run("the deployment's own write", func(t *testing.T) {
		_, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: erin, Address: bracketed}.Build())
		refused(t, err)
	})

	t.Run("and its general one", func(t *testing.T) {
		vs, err := b.Ungated.Email().List(ctx, app.EmailListRequest_builder{
			Filters: []*app.EmailFilter{app.EmailFilter_builder{Holder: erin}.Build()},
		}.Build())
		require.NoError(t, err)
		require.Len(t, vs.GetItems(), 1)

		_, err = b.Ungated.Email().Patch(ctx, app.EmailPatchRequest_builder{
			Ref:         app.EmailRef_builder{Id: vs.GetItems()[0].GetId()}.Build(),
			Address:     z.Ptr(bracketed),
			DateUpdated: vs.GetItems()[0].GetDateUpdated(),
		}.Build())
		refused(t, err)
	})
}
