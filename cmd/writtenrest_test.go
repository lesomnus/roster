package cmd_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/keys"
)

// everyPage walks a list to its end, a hundred rows at a time -- the most a
// page holds, so that a page too large to answer is met -- and says how many
// rows it was answered, and how many bytes the largest page was.
func everyPage[P interface {
	proto.Message
	GetNext() string
}](t *testing.T, what string, page func(after string) (P, int, error)) (rows, largest int) {
	t.Helper()

	after := ""
	for {
		v, n, err := page(after)
		require.NoError(t, err, "%s, after %d rows", what, rows)
		rows += n
		largest = max(largest, proto.Size(v))
		if after = v.GetNext(); after == "" {
			return rows, largest
		}
	}
}

// TestAListOfAccountsAtTheirLongestIsAnswered: nobody makes the list of their
// tenant's accounts at providers unanswerable by the subjects they write onto
// their own row.
//
// `IdentityService/Add` is everybody's own to call, about their own row, and
// a subject was held to no length. Somebody who held nothing wrote five
// accounts of 900 KiB each onto their own row (OK, each); their
// administrator's `IdentityService/List` then answered `ResourceExhausted`,
// and so did `HolderService/SignsIn` for them. It is what a name was before a
// name had a length (`TestAListOfPeopleAtTheirLongestIsAnswered`). Right is
// that each write is refused, naming `subject` and the bound.
//
// What everybody holds is `IdentityService/Attest` now, through a front door,
// and it writes through `Add`; so the writer here is somebody a role lets
// call `Add` about their own row, and the bound is the form's, whoever writes.
//
// 255 bytes, which is what OpenID Connect lets a subject be and what a
// directory's id for somebody already was. And a hundred accounts at that
// length, which is the most a page holds, are one answer.
func TestAListOfAccountsAtTheirLongestIsAnswered(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := served(t, b.Server)
	ids, holders := app.NewIdentityServiceClient(conn), app.NewHolderServiceClient(conn)

	who := b.holder(t, ctx, b.Contoso, "plain")
	b.binds(t, who, b.role(t, ctx, "links-themselves", app.IdentityService_Add_FullMethodName), nil)
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()
	wire := asOverTheWire(ctx, who)

	for i := range 5 {
		_, err := ids.Add(wire, app.IdentityAddRequest_builder{
			Holder: ref, Provider: fmt.Sprintf("big-%d", i), Subject: strings.Repeat("s", 900<<10),
		}.Build())
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "a subject of 900 KiB, written by somebody onto their own row: %v", err)
		if err != nil {
			assert.Contains(t, status.Convert(err).Message(), "subject")
			assert.Less(t, len(status.Convert(err).Message()), 1024, "a refusal does not say back what it refused")
		}
	}

	// As many accounts as a page holds, each as long as one is.
	for i := range 100 {
		_, err := ids.Add(wire, app.IdentityAddRequest_builder{
			Holder: ref, Provider: fmt.Sprintf("longest-%d", i), Subject: strings.Repeat("s", core.MaxSubject),
		}.Build())
		x.NoError(err)
	}

	b.mayAnything(b.ContosoUser, b.Contoso)
	admin := asOverTheWire(ctx, b.ContosoUser)

	listed, size := everyPage(t, "the administrator's list of accounts", func(after string) (*app.IdentityListResponse, int, error) {
		page, err := ids.List(admin, app.IdentityListRequest_builder{Size: 100, After: after}.Build())

		return page, len(page.GetItems()), err
	})
	x.GreaterOrEqual(listed, 100)
	t.Logf("a hundred accounts at their longest are %d bytes, of the %d one answer may be", size, 4<<20)

	how, err := holders.SignsIn(admin, app.HolderSignsInRequest_builder{Ref: ref}.Build())
	x.NoError(err, "and how they sign in")
	x.NotEmpty(how.GetIdentities())

	// And one byte past the bound is past it.
	_, err = ids.Add(wire, app.IdentityAddRequest_builder{
		Holder: ref, Provider: "past", Subject: strings.Repeat("s", core.MaxSubject+1),
	}.Build())
	refusedAt(t, err, "subject")
}

// methodsOf is a list of `n` methods of `each` bytes apiece -- thirty-two or
// more -- every one of them something a role naming `/roster.HolderService/*`
// covers.
func methodsOf(n, each int) []string {
	const of = "/roster.HolderService/"

	out := make([]string, n)
	for i := range out {
		tail := fmt.Sprintf("%d", i)
		out[i] = of + strings.Repeat("M", each-len(of)-len(tail)) + tail
	}

	return out
}

// TestAListOfKeysAtTheirLongestIsAnswered: nobody makes the list of their
// tenant's keys unanswerable by the methods they list on their own.
//
// `ApiKeyService/Issue` is everybody's own to call, for a key on their own
// account that names what they hold, and a list of methods was held to no
// count and no length. Somebody bound one role wrote five keys onto their own
// account, each naming four thousand methods of 225 bytes (OK, each); their
// administrator's `ApiKeyService/List` then answered `ResourceExhausted`, and
// so did `HolderService/SignsIn` for them. Right is that each is refused,
// naming `methods` and the bound.
//
// A list is 1,024 entries at most, an entry 255 bytes, and the whole of it
// sixteen kibibytes: every method roster serves, named one by one, is a fifth
// of the first and under half of the last
// (`TestAListOfMethodsHoldsEveryMethodRosterServes`). And a hundred keys,
// each with a list as long as one is, are one answer.
func TestAListOfKeysAtTheirLongestIsAnswered(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := served(t, b.Server)
	issued, holders := app.NewApiKeyServiceClient(conn), app.NewHolderServiceClient(conn)

	// Somebody who holds one role, and may list on a key what it covers.
	who := b.holder(t, ctx, b.Contoso, "plain")
	b.binds(t, who, b.role(t, ctx, "links-themselves", app.IdentityService_Add_FullMethodName), nil)
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()
	b.binds(t, who, b.role(t, ctx, "reads-people", "/roster.HolderService/*"), nil)
	wire := asOverTheWire(ctx, who)

	issue := func(alias string, methods []string) error {
		_, err := issued.Issue(wire, app.ApiKeyIssueRequest_builder{Holder: ref, Alias: alias, Methods: methods}.Build())

		return err
	}

	for i := range 5 {
		err := issue(fmt.Sprintf("big-%d", i), methodsOf(4000, 225))
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "a key naming 900 KiB of methods, issued by somebody on their own account: %v", err)
		if err != nil {
			assert.Contains(t, status.Convert(err).Message(), "methods")
			assert.Less(t, len(status.Convert(err).Message()), 1024, "a refusal does not say back what it refused")
		}
	}

	// As many keys as a page holds, each naming as much as a list holds.
	for i := range 100 {
		x.NoError(issue(fmt.Sprintf("longest-%d", i), methodsOf(keys.MaxMethodsSize/keys.MaxMethod, keys.MaxMethod)))
	}

	b.mayAnything(b.ContosoUser, b.Contoso)
	admin := asOverTheWire(ctx, b.ContosoUser)

	listed, size := everyPage(t, "the administrator's list of keys", func(after string) (*app.ApiKeyListResponse, int, error) {
		page, err := issued.List(admin, app.ApiKeyListRequest_builder{Size: 100, After: after}.Build())

		return page, len(page.GetItems()), err
	})
	x.GreaterOrEqual(listed, 100)
	t.Logf("a hundred keys at their longest are %d bytes, of the %d one answer may be", size, 4<<20)

	how, err := holders.SignsIn(admin, app.HolderSignsInRequest_builder{Ref: ref}.Build())
	x.NoError(err, "and how they sign in")
	x.NotEmpty(how.GetKeys())

	// And each of the three bounds, past by one.
	t.Run("one entry too many", func(t *testing.T) {
		refusedAt(t, issue("many", methodsOf(keys.MaxMethods+1, 32)), "methods")
	})
	t.Run("an entry too long", func(t *testing.T) {
		refusedAt(t, issue("long", methodsOf(1, keys.MaxMethod+1)), "methods")
	})
	t.Run("too much in all", func(t *testing.T) {
		refusedAt(t, issue("much", methodsOf(keys.MaxMethodsSize/keys.MaxMethod+1, keys.MaxMethod)), "methods")
	})
}

// TestAListOfMethodsHoldsEveryMethodRosterServes: the bounds on a list of
// methods are a few times what roster itself has to list, counted here off
// the services it registers. A role or a key that named every method one by
// one -- which is what a pattern is for -- would be a fifth of the count and
// under half of the bytes; were roster to grow past that, this says to raise
// them before anybody's list meets one.
func TestAListOfMethodsHoldsEveryMethodRosterServes(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	var all []string
	size := 0
	for svc, info := range b.grpc(t).GetServiceInfo() {
		for _, m := range info.Methods {
			name := "/" + svc + "/" + m.Name
			all = append(all, name)
			size += len(name)
		}
	}
	x.Greater(len(all), 100, "the walk found too few methods to prove anything")
	t.Logf("roster serves %d methods, which named one by one are %d bytes", len(all), size)

	x.NoError(keys.Listed("methods", all))
	x.LessOrEqual(3*len(all), keys.MaxMethods, "%d methods are served; a list holds three times that", len(all))
	x.LessOrEqual(2*size, keys.MaxMethodsSize, "every method named is %d bytes; a list holds twice that", size)
	for _, name := range all {
		x.LessOrEqual(4*len(name), keys.MaxMethod, name)
	}
}

// TestWhatARoleAndADelegationListHasALength: a role's list of methods and a
// delegation's are held to what a key's is, and a delegation's before
// anything is spent on it.
//
// A role is written by a tenant's administrator, and one naming a megabyte
// of methods was taken (OK) and widened (OK). A delegation's list is what a
// front door says at `VouchService/Delegate`, at `Accept` and where it
// redeems a link: one too long was asked of the gate an entry at a time, the
// link was spent, and the row was written. Right is InvalidArgument for each,
// and for a delegation where the list is first read: nobody's password is
// checked and no link is spent for a list that will not be written.
func TestWhatARoleAndADelegationListHasALength(t *testing.T) {
	const password = "the password they chose"

	b, ctx := build(t)
	b.mayAnything(b.ContosoUser, b.Contoso)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	tooMany := methodsOf(4000, 225)

	t.Run("a role made", func(t *testing.T) {
		_, err := b.Walled.Role().Add(admin, app.RoleAddRequest_builder{Tenant: at, Alias: "wide", Methods: tooMany}.Build())
		refusedAt(t, err, "methods")
	})

	t.Run("a role widened", func(t *testing.T) {
		role := b.role(t, ctx, "widened", "/roster.HolderService/Get")
		_, err := b.Walled.Role().Widen(admin, app.RoleWidenRequest_builder{
			Ref: app.RoleRef_builder{Id: role.Bytes()}.Build(), Methods: tooMany,
		}.Build())
		refusedAt(t, err, "methods")

		_, err = b.Walled.Role().Widen(admin, app.RoleWidenRequest_builder{
			Ref: app.RoleRef_builder{Id: role.Bytes()}.Build(), Methods: methodsOf(keys.MaxMethodsSize/keys.MaxMethod-1, keys.MaxMethod),
		}.Build())
		require.NoError(t, err, "a list as long as one is")
	})

	// The row with the most in it: a role's name, its note, its labels and its
	// list, each at its bound, a hundred to a page.
	t.Run("and a page of roles at their longest is answered", func(t *testing.T) {
		labels := map[string]string{}
		for i := range core.MaxLabels {
			labels[fmt.Sprintf("%0*d", core.MaxLabelKey, i)] = strings.Repeat("v", core.MaxLabelValue)
		}
		for i := range 100 {
			_, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
				Tenant: at, Alias: fmt.Sprintf("longest-%d", i),
				Name: strings.Repeat(wide, core.MaxName), Desc: strings.Repeat(wide, core.MaxDesc), Labels: labels,
				Methods: methodsOf(keys.MaxMethodsSize/keys.MaxMethod, keys.MaxMethod),
			}.Build())
			require.NoError(t, err)
		}

		roles := app.NewRoleServiceClient(served(t, b.Server))
		listed, size := everyPage(t, "the administrator's list of roles", func(after string) (*app.RoleListResponse, int, error) {
			page, err := roles.List(asOverTheWire(ctx, b.ContosoUser), app.RoleListRequest_builder{Size: 100, After: after}.Build())

			return page, len(page.GetItems()), err
		})
		require.GreaterOrEqual(t, listed, 100)
		t.Logf("a hundred roles at their longest are %d bytes, of the %d one answer may be", size, 4<<20)
	})

	alice := b.holder(t, ctx, b.Contoso, "alice")
	b.sets(t, ctx, alice, password)
	v := b.vouched()
	rows := func() int {
		n, err := b.Ent.Delegation.Query().Count(ctx)
		require.NoError(t, err)

		return n
	}
	was := rows()

	t.Run("a delegation at a sign-in", func(t *testing.T) {
		_, err := v.Delegate(admin, app.VouchDelegateRequest_builder{
			Who: app.VouchWho_builder{Id: alice.Bytes()}.Build(), Secret: []byte(password), Methods: tooMany,
		}.Build())
		refusedAt(t, err, "methods")
		require.Equal(t, was, rows())
	})

	t.Run("and at a link, which is not spent on it", func(t *testing.T) {
		x := require.New(t)
		made, err := v.Link(admin, app.VouchLinkRequest_builder{Who: app.VouchWho_builder{Id: alice.Bytes()}.Build()}.Build())
		x.NoError(err)

		_, err = v.Redeem(admin, app.VouchRedeemRequest_builder{Token: made.GetToken(), Methods: tooMany}.Build())
		refusedAt(t, err, "methods")
		x.Equal(was, rows())

		res, err := v.Redeem(admin, app.VouchRedeemRequest_builder{
			Token: made.GetToken(), Methods: []string{app.MeService_Get_FullMethodName},
		}.Build())
		x.NoError(err)
		x.True(res.GetVerified().GetOk(), "the link was spent on the list that was refused")
	})
}

// longerThan is text of one byte more than `most`.
func longerThan(most int) string { return strings.Repeat("x", most+1) }

// TestTheRestOfWhatAnAdministratorWritesHasALength: what is left of what a
// caller writes -- a connection's issuer, its client's id, its scopes and the
// name of its secret, the provider a mail domain names, the client a group is
// shown to -- is held to the table: 2,048 bytes for an address, as a
// picture's is, and 255 for an identifier.
//
// These are a tenant's administrator's to write, and not everybody's, so they
// are here for the table to be whole: each took a megabyte.
func TestTheRestOfWhatAnAdministratorWritesHasALength(t *testing.T) {
	b, ctx := build(t)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	connection := func(name string, with func(*app.ConnectionAddRequest_builder)) error {
		req := app.ConnectionAddRequest_builder{
			Tenant: at, Name: name, Issuer: "https://login.example.com/contoso", ClientId: "the-app",
		}
		with(&req)
		_, err := b.Ungated.Connection().Add(ctx, req.Build())

		return err
	}

	t.Run("a connection's issuer", func(t *testing.T) {
		refusedAt(t, connection("a", func(r *app.ConnectionAddRequest_builder) {
			r.Issuer = "https://login.example.com/" + longerThan(core.MaxLocation)
		}), "issuer")
	})
	t.Run("its client's id", func(t *testing.T) {
		refusedAt(t, connection("b", func(r *app.ConnectionAddRequest_builder) { r.ClientId = longerThan(core.MaxIdentifier) }), "client_id")
	})
	t.Run("the name of its secret", func(t *testing.T) {
		refusedAt(t, connection("c", func(r *app.ConnectionAddRequest_builder) {
			r.SecretRef = "env:" + longerThan(core.MaxIdentifier)
		}), "secret_ref")
	})
	t.Run("its scopes", func(t *testing.T) {
		many := make([]string, core.MaxScopes+1)
		for i := range many {
			many[i] = fmt.Sprintf("scope-%d", i)
		}
		refusedAt(t, connection("d", func(r *app.ConnectionAddRequest_builder) { r.Scopes = many }), "scopes")
		refusedAt(t, connection("e", func(r *app.ConnectionAddRequest_builder) {
			r.Scopes = []string{longerThan(core.MaxIdentifier)}
		}), "scopes")
	})
	t.Run("and one at every bound is written", func(t *testing.T) {
		full := make([]string, core.MaxScopes)
		for i := range full {
			full[i] = strings.Repeat("s", core.MaxIdentifier-2) + fmt.Sprintf("%02d", i)
		}
		require.NoError(t, connection("f", func(r *app.ConnectionAddRequest_builder) {
			r.Issuer = "https://login.example.com/" + strings.Repeat("p", core.MaxLocation-len("https://login.example.com/"))
			r.ClientId = strings.Repeat("c", core.MaxIdentifier)
			r.Scopes = full
		}))
	})

	t.Run("the provider a mail domain names", func(t *testing.T) {
		_, err := b.Ungated.MailDomain().Add(ctx, app.MailDomainAddRequest_builder{
			Tenant: at, Name: "contoso.example", Provider: longerThan(core.MaxIdentifier),
		}.Build())
		refusedAt(t, err, "provider")
	})

	t.Run("the client a group is shown to", func(t *testing.T) {
		g, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{Tenant: at, Alias: "shown"}.Build())
		require.NoError(t, err)
		_, err = b.Ungated.GroupExposure().Add(ctx, app.GroupExposureAddRequest_builder{
			Group: app.GroupRef_builder{Id: g.GetId()}.Build(), Client: longerThan(core.MaxIdentifier),
		}.Build())
		refusedAt(t, err, "client")
	})

	// A consent is an app's four words about a document, and each is one.
	t.Run("what an app says of a consent", func(t *testing.T) {
		who := b.holder(t, ctx, b.Contoso, "agrees")
		_, err := b.Ungated.Consent().Agree(b.as(ctx, who, b.Contoso), app.ConsentAgreeRequest_builder{
			Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Documents: []*app.ConsentDocument{app.ConsentDocument_builder{
				Document: "terms", Version: longerThan(core.MaxIdentifier),
			}.Build()},
		}.Build())
		refusedAt(t, err, "version")
	})

}
