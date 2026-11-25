package cmd_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/portrait"
)

// wide is a character of four bytes, the most one is: a name is counted in
// characters, so the longest name there is has this many bytes to each.
const wide = "\U00010348"

// dataOf is something an app might keep in `Holder.data`, of `n` bytes: its
// type's name and its value together, which is how it is counted.
func dataOf(t *testing.T, n int) *anypb.Any {
	t.Helper()

	// The value is a tag, a length and the bytes, so how many bytes to ask for
	// is found by asking.
	for payload := n; payload >= 0; payload-- {
		v, err := anypb.New(wrapperspb.Bytes(bytes.Repeat([]byte{'x'}, payload)))
		require.NoError(t, err)
		if len(v.GetTypeUrl())+len(v.GetValue()) == n {
			return v
		}
	}
	t.Fatalf("no value of %d bytes", n)

	return nil
}

// refusedAt is an InvalidArgument that names the field it is about.
func refusedAt(t *testing.T, err error, field string) {
	t.Helper()
	require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), field)
	require.Less(t, len(status.Convert(err).Message()), 1024, "a refusal does not say back what it refused")
}

// TestWhatSomebodyWritesOfThemselvesHasALength: each field of a profile, and
// `data`, is written up to its bound and refused past it.
//
// `HolderService/Update` is everybody's own to call (`self-service`), and what
// it wrote was held to no length: somebody who holds nothing wrote a display
// name and a department of three megabytes each onto their own row, answered
// OK. Right is InvalidArgument, naming the field and the bound, at the write.
// What is refused is one character past the bound, and what is at the bound
// is written -- in characters of four bytes, since a name is counted in
// characters and not in bytes.
func TestWhatSomebodyWritesOfThemselvesHasALength(t *testing.T) {
	b, ctx := build(t)
	who := b.holder(t, ctx, b.Contoso, "plain")
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()

	holders := app.NewHolderServiceClient(served(t, b.Server))
	wire := asOverTheWire(ctx, who)

	// What their organisation says of them is not theirs to write
	// (`TestSomebodyChangingTheirOwnProfileKeepsWhatTheirOrganisationSaysOfThem`),
	// so a profile that changes one of those three is written by whoever
	// administers the tenant's people, and held to the same bounds.
	b.mayAnything(b.ContosoUser, b.Contoso)
	admin := asOverTheWire(ctx, b.ContosoUser)
	update := func(t *testing.T, p *app.Profile, data *anypb.Any) error {
		t.Helper()
		got, err := holders.Get(admin, app.HolderGetRequest_builder{Ref: ref}.Build())
		require.NoError(t, err)
		wire := wire
		for _, o := range core.Organisational {
			if p != nil && o.Of(p) != o.Of(got.GetProfile()) {
				wire = admin
			}
		}
		_, err = holders.Update(wire, app.HolderUpdateRequest_builder{
			Ref: ref, DateUpdated: got.GetDateUpdated(), Profile: p, Data: data,
		}.Build())

		return err
	}

	for _, tc := range []struct {
		field string
		most  int
		of    func(v string) *app.Profile
	}{
		{"profile.display_name", core.MaxName, func(v string) *app.Profile { return app.Profile_builder{DisplayName: v}.Build() }},
		{"profile.department", core.MaxName, func(v string) *app.Profile { return app.Profile_builder{Department: v}.Build() }},
		{"profile.title", core.MaxName, func(v string) *app.Profile { return app.Profile_builder{Title: v}.Build() }},
		{"profile.employee_no", core.MaxEmployeeNo, func(v string) *app.Profile { return app.Profile_builder{EmployeeNo: v}.Build() }},
		{"profile.locale", core.MaxLocale, func(v string) *app.Profile { return app.Profile_builder{Locale: v}.Build() }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			refusedAt(t, update(t, tc.of(strings.Repeat(wide, tc.most+1)), nil), tc.field)
			refusedAt(t, update(t, tc.of(strings.Repeat("x", 3<<20)), nil), tc.field)
			require.NoError(t, update(t, tc.of(strings.Repeat(wide, tc.most)), nil))
		})
	}

	t.Run("data", func(t *testing.T) {
		refusedAt(t, update(t, nil, dataOf(t, core.MaxData+1)), "data")
		refusedAt(t, update(t, nil, dataOf(t, 3<<20)), "data")
		require.NoError(t, update(t, nil, dataOf(t, core.MaxData)))
	})
}

// TestARowsNameNoteAndLabelsHaveALength: what is written of a person, a team,
// a site, a group, a role and a tenant -- the name, the note, the labels -- is
// held to one table, by the write that makes the row and by the one that
// changes it.
//
// None had a length. A tenant's administrator added a team whose note was a
// megabyte and was answered OK, and so for each row here; right is
// InvalidArgument, naming the field. The bound is the same for every row,
// since it is the same field in each.
func TestARowsNameNoteAndLabelsHaveALength(t *testing.T) {
	b, ctx := build(t)
	b.mayAnything(b.ContosoUser, b.Contoso)
	wire := asOverTheWire(ctx, b.ContosoUser)
	conn := served(t, b.Server)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	long := func(most int) string { return strings.Repeat(wide, most+1) }
	many := map[string]string{}
	for i := range core.MaxLabels + 1 {
		many[fmt.Sprintf("k%d", i)] = "v"
	}
	keyed := map[string]string{strings.Repeat("k", core.MaxLabelKey+1): "v"}
	valued := map[string]string{"k": strings.Repeat("v", core.MaxLabelValue+1)}

	// One row of each kind, written with what `say` is handed.
	type said struct {
		name, desc string
		labels     map[string]string
	}
	rows := []struct {
		row      string
		labelled bool
		add      func(alias string, v said) error
	}{
		{"a person", true, func(alias string, v said) error {
			_, err := app.NewHolderServiceClient(conn).Add(wire, app.HolderAddRequest_builder{
				Tenant: at, Alias: alias, Name: v.name, Desc: v.desc, Labels: v.labels,
			}.Build())

			return err
		}},
		{"a team", false, func(alias string, v said) error {
			_, err := app.NewTeamServiceClient(conn).Add(wire, app.TeamAddRequest_builder{
				Tenant: at, Alias: alias, Name: v.name, Desc: v.desc,
			}.Build())

			return err
		}},
		{"a site", true, func(alias string, v said) error {
			_, err := app.NewSiteServiceClient(conn).Add(wire, app.SiteAddRequest_builder{
				Tenant: at, Alias: alias, Name: v.name, Desc: v.desc, Labels: v.labels,
			}.Build())

			return err
		}},
		{"a group", true, func(alias string, v said) error {
			_, err := app.NewGroupServiceClient(conn).Add(wire, app.GroupAddRequest_builder{
				Tenant: at, Alias: alias, Name: v.name, Desc: v.desc, Labels: v.labels,
			}.Build())

			return err
		}},
		{"a role", true, func(alias string, v said) error {
			_, err := app.NewRoleServiceClient(conn).Add(wire, app.RoleAddRequest_builder{
				Tenant: at, Alias: alias, Name: v.name, Desc: v.desc, Labels: v.labels,
				Methods: []string{app.HolderService_Get_FullMethodName},
			}.Build())

			return err
		}},
		{"a tenant, changed", true, func(_ string, v said) error {
			got, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: at}.Build())
			if err != nil {
				return err
			}
			req := app.TenantUpdateRequest_builder{Ref: at, DateUpdated: got.GetDateUpdated(), Labels: v.labels}
			if v.name != "" {
				req.Name = z.Ptr(v.name)
			}
			if v.desc != "" {
				req.Desc = z.Ptr(v.desc)
			}
			_, err = app.NewTenantServiceClient(conn).Update(wire, req.Build())

			return err
		}},
	}

	for i, r := range rows {
		t.Run(r.row, func(t *testing.T) {
			alias := func(what string) string { return fmt.Sprintf("row%d-%s", i, what) }

			refusedAt(t, r.add(alias("name"), said{name: long(core.MaxName)}), "name")
			refusedAt(t, r.add(alias("desc"), said{desc: long(core.MaxDesc)}), "desc")
			refusedAt(t, r.add(alias("megabyte"), said{desc: strings.Repeat("x", 1<<20)}), "desc")
			if r.labelled {
				refusedAt(t, r.add(alias("many"), said{labels: many}), "labels")
				refusedAt(t, r.add(alias("key"), said{labels: keyed}), "labels")
				refusedAt(t, r.add(alias("value"), said{labels: valued}), "labels")
			}

			at := said{name: strings.Repeat(wide, core.MaxName), desc: strings.Repeat(wide, core.MaxDesc)}
			if r.labelled {
				at.labels = map[string]string{strings.Repeat("k", core.MaxLabelKey): strings.Repeat("v", core.MaxLabelValue)}
			}
			require.NoError(t, r.add(alias("longest"), at), "what is at the bound is written")
		})
	}
}

// TestANumberAndAnAddressHaveALength: an address is 254 bytes at most, as the
// protocol that carries mail says, and a number is 64 characters.
//
// The number has had its bound since it had a rule. The address had none: six
// of a megabyte each on one person's own row, each answered OK, made
// `EmailService/List` answer `ResourceExhausted`.
func TestANumberAndAnAddressHaveALength(t *testing.T) {
	b, ctx := build(t)
	who := b.holder(t, ctx, b.Contoso, "plain")
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()
	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, who)
	emails, phones := app.NewEmailServiceClient(conn), app.NewPhoneServiceClient(conn)

	// 64 before the `@`, and a name of three labels after it.
	name := strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	longest := strings.Repeat("a", front.MaxLocal) + "@" + name
	require.Len(t, longest, front.MaxAddress)

	t.Run("an address", func(t *testing.T) {
		for i := range 6 {
			_, err := emails.Add(wire, app.EmailAddRequest_builder{
				Holder: ref, Address: fmt.Sprintf("%s-%d@contoso.example", strings.Repeat("a", 1<<20), i),
			}.Build())
			refusedAt(t, err, "address")
		}
		_, err := emails.Add(wire, app.EmailAddRequest_builder{Holder: ref, Address: "a" + longest}.Build())
		refusedAt(t, err, "address")
		_, err = emails.Add(wire, app.EmailAddRequest_builder{Holder: ref, Address: "a@d" + name}.Build())
		refusedAt(t, err, "address")

		_, err = emails.Add(wire, app.EmailAddRequest_builder{Holder: ref, Address: longest}.Build())
		require.NoError(t, err)

		vs, err := emails.List(wire, app.EmailListRequest_builder{}.Build())
		require.NoError(t, err)
		require.Len(t, vs.GetItems(), 1)
	})

	t.Run("a number", func(t *testing.T) {
		_, err := phones.Add(wire, app.PhoneAddRequest_builder{Holder: ref, Number: "+" + strings.Repeat("5", 64)}.Build())
		refusedAt(t, err, "number")
		v, err := phones.Add(wire, app.PhoneAddRequest_builder{Holder: ref, Number: "+" + strings.Repeat("5", 63)}.Build())
		require.NoError(t, err)

		// And by the general write, which no port serves and the deployment
		// makes: the rule is about the number.
		_, err = b.Ungated.Phone().Patch(ctx, app.PhonePatchRequest_builder{
			Ref: app.PhoneRef_builder{Id: v.GetId()}.Build(), DateUpdated: v.GetDateUpdated(),
			Number: z.Ptr("+" + strings.Repeat("5", 1<<20)),
		}.Build())
		refusedAt(t, err, "number")
	})
}

// noise is an image that compresses as badly as one can: every pixel its own
// colour, so the portrait made of it is as large as a portrait is.
func noise(t *testing.T, side int) []byte {
	t.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	m := image.NewNRGBA(image.Rect(0, 0, side, side))
	for y := range side {
		for x := range side {
			m.Set(x, y, color.NRGBA{uint8(r.Uint32()), uint8(r.Uint32()), uint8(r.Uint32()), 0xff})
		}
	}
	var out bytes.Buffer
	require.NoError(t, png.Encode(&out, m))

	return out.Bytes()
}

// longest is somebody with everything written of them at its bound: every
// character four bytes, every label a full one, `data` full, and a portrait
// that does not compress.
func (b *built) longest(t *testing.T, ctx context.Context, alias string, picture []byte, data *anypb.Any) {
	t.Helper()

	labels := map[string]string{}
	for i := range core.MaxLabels {
		labels[fmt.Sprintf("%0*d", core.MaxLabelKey, i)] = strings.Repeat("v", core.MaxLabelValue)
	}
	v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  alias,
		Name:   strings.Repeat(wide, core.MaxName),
		Desc:   strings.Repeat(wide, core.MaxDesc),
		Labels: labels,
		Profile: app.Profile_builder{
			DisplayName: strings.Repeat(wide, core.MaxName),
			Department:  strings.Repeat(wide, core.MaxName),
			Title:       strings.Repeat(wide, core.MaxName),
			EmployeeNo:  strings.Repeat(wide, core.MaxEmployeeNo),
			Locale:      strings.Repeat(wide, core.MaxLocale),
		}.Build(),
		Data: data,
	}.Build())
	require.NoError(t, err)

	_, err = b.Ungated.Holder().Portray(ctx, app.HolderPortrayRequest_builder{
		Ref: app.HolderRef_builder{Id: v.GetId()}.Build(), DateUpdated: v.GetDateUpdated(), Image: picture,
	}.Build())
	require.NoError(t, err)
}

// TestAListOfPeopleAtTheirLongestIsAnswered: nobody makes their tenant's list
// of people unanswerable by what they write of themselves, and a full page of
// people each written at every bound is answered.
//
// Five people who held nothing each wrote a display name of 900 KiB onto
// their own row (OK, each), and their administrator's `HolderService/List`
// answered `ResourceExhausted: received message larger than max`: nobody
// could page past them, since the page that fails is the one that says where
// the next begins. Right is that each write is refused and the list is
// answered.
//
// And the bounds are small enough together for that to stay so: a hundred
// people, which is the most a page holds, each with their name, their note,
// their labels, their profile and `data` at the bound and a portrait that
// does not compress, are one answer.
func TestAListOfPeopleAtTheirLongestIsAnswered(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := served(t, b.Server)
	holders := app.NewHolderServiceClient(conn)

	for i := range 5 {
		who := b.holder(t, ctx, b.Contoso, fmt.Sprintf("plain-%d", i))
		ref := app.HolderRef_builder{Id: who.Bytes()}.Build()
		wire := asOverTheWire(ctx, who)

		got, err := holders.Get(wire, app.HolderGetRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		_, err = holders.Update(wire, app.HolderUpdateRequest_builder{
			Ref: ref, DateUpdated: got.GetDateUpdated(),
			Profile: app.Profile_builder{DisplayName: strings.Repeat("x", 900<<10)}.Build(),
		}.Build())
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "a display name of 900 KiB, written by somebody of themselves: %v", err)
	}

	picture, data := noise(t, portrait.Sizes[len(portrait.Sizes)-1]), dataOf(t, core.MaxData)
	for i := range 100 {
		b.longest(t, ctx, fmt.Sprintf("longest-%d", i), picture, data)
	}

	b.mayAnything(b.ContosoUser, b.Contoso)
	admin := asOverTheWire(ctx, b.ContosoUser)

	// Every page of the list, the most rows a page holds at a time.
	listed, after := 0, ""
	for {
		page, err := holders.List(admin, app.HolderListRequest_builder{Size: 100, After: after}.Build())
		x.NoError(err, "the administrator's list of people, after %d of them", listed)
		listed += len(page.GetItems())
		if after = page.GetNext(); after == "" {
			break
		}
	}
	x.GreaterOrEqual(listed, 105)

	// And the hundred at their longest, in one answer.
	found, err := holders.Search(admin, app.HolderSearchRequest_builder{Q: z.Ptr("longest"), Size: 100}.Build())
	x.NoError(err, "and their search")
	x.Len(found.GetItems(), 100)
	t.Logf("a hundred people at their longest are %d bytes, of the %d one answer may be", proto.Size(found), 4<<20)
}
