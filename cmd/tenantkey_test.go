package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/frame"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestATenantKeyIsTheirsAndNotTheDeploymentS is the two kinds of key being two
// kinds of caller.
//
// They are the same table, the same hash and the same column of methods. What
// differs is the prefix, and through it which database the row is in and who
// the token is served as -- and that difference is the whole of what keeps a
// customer's key from being the deployment's.
//
// Before this there was one prefix and one answer. A key minted anywhere
// resolved to the key, whose identifier says "api key", which the policy hands
// `frame.Everything`. So the moment a customer could mint one, they could read
// every other customer.
func TestATenantKeyIsTheirsAndNotTheDeploymentS(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	// A second customer, so that "sees one tenant" is a claim with something to
	// be wrong about.
	fabrikam := add(t, ctx, b.Server, "fabrikam")
	addHolder(t, ctx, b.Server, fabrikam, "erlich")

	const listHolders = "/roster.HolderService/List"

	// Alice may list holders in her own tenant, the ordinary way.
	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "reader",
		Methods: []string{listHolders},
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	hers := mintFor(t, ctx, b, b.Who, "her-script", []string{listHolders}, time.Time{})

	c := app.NewHolderServiceClient(b.Conn)
	list := func(token string) (*app.HolderListResponse, error) {
		return c.List(bearing(ctx, token), app.HolderListRequest_builder{}.Build())
	}

	t.Run("a tenant key sees its holder's tenant and no other", func(t *testing.T) {
		x := require.New(t)

		v, err := list(hers)
		x.NoError(err)

		// One tenant's worth of people. fabrikam exists and is not among them.
		for _, h := range v.GetItems() {
			x.NotEqual("erlich", h.GetAlias(), "a customer's key read another customer")
		}
		x.NotEmpty(v.GetItems())
	})

	// The same call with the deployment's key, which is what the tenant key is
	// being measured against. It crosses because it is the deployment's, and
	// that is the scope a customer must not be able to reach.
	t.Run("a deployment key is the one that sees everything", func(t *testing.T) {
		x := require.New(t)

		wide := keyFor(t, listHolders)

		fabrikam := add(t, ctx, wide.Server, "fabrikam")
		addHolder(t, ctx, wide.Server, fabrikam, "erlich")

		v, err := app.NewHolderServiceClient(wide.Conn).List(bearing(ctx, wide.Token),
			app.HolderListRequest_builder{}.Build())
		x.NoError(err)

		var aliases []string
		for _, h := range v.GetItems() {
			aliases = append(aliases, h.GetAlias())
		}
		x.Contains(aliases, "erlich")
		x.Contains(aliases, "someone")
	})

	// And a tenant key is still narrowed by its own methods, on top of
	// everything its holder's bindings already decided.
	t.Run("a tenant key reaches no further than it was made for", func(t *testing.T) {
		x := require.New(t)

		narrow := mintFor(t, ctx, b, b.Who, "narrower", []string{"/roster.MeService/Get"}, time.Time{})

		_, err := list(narrow)
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	// A slug naming the other tenant, on her key. fabrikam is given somebody
	// with **her** alias first, so that a resolver that quietly substituted
	// "the caller's tenant" for the one written down would have a row to
	// answer with -- and the answer has to be that there is nobody, not that
	// there is a namesake.
	t.Run("a foreign slug names nobody, not a namesake", func(t *testing.T) {
		x := require.New(t)

		addHolder(t, ctx, b.Server, fabrikam, "someone")

		const get = "/roster.HolderService/Get"
		permits(t, ctx, b, b.Contoso, b.Who, "getter", get)
		hers := mintFor(t, ctx, b, b.Who, "her-getter", []string{get}, time.Time{})

		_, err := c.Get(bearing(ctx, hers), app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{
				Slug: app.HolderRefBySlug_builder{
					Alias:  z.Ptr("someone"),
					Tenant: app.TenantRef_builder{Alias: z.Ptr("fabrikam")}.Build(),
				}.Build(),
			}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err),
			"a foreign slug was answered -- with whose row?")
	})

	// And her key's Watch is walled exactly as her reads are. Every rt_ in the
	// suite so far read; a stream is narrowed by a different line of generated
	// code, so "the wall applies to the key" has to be asserted once per shape.
	t.Run("and a watch on her key agrees with her reads", func(t *testing.T) {
		x := require.New(t)

		erlich := addHolder(t, ctx, b.Server, fabrikam, "watched")

		const watchHolders = "/roster.HolderService/Watch"
		permits(t, ctx, b, b.Contoso, b.Who, "watcher", watchHolders, "/roster.HolderService/Get")
		hers := mintFor(t, ctx, b, b.Who, "her-watcher",
			[]string{watchHolders, "/roster.HolderService/Get"}, time.Time{})

		wire, cancel := context.WithTimeout(bearing(ctx, hers), 3*time.Second)
		defer cancel()

		_, get := c.Get(wire, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Id: erlich.Bytes()}.Build(),
		}.Build())

		out, err := c.Watch(wire, app.HolderWatchRequest_builder{
			Filters: []*app.HolderFilter{
				app.HolderFilter_builder{
					Ref: app.HolderRef_builder{Id: erlich.Bytes()}.Build(),
				}.Build(),
			},
		}.Build())
		x.NoError(err, "the stream is refused on its first Recv, not on the call")

		_, watch := out.Recv()

		x.Equal(codes.NotFound, status.Code(get), "the control")
		x.Equal(status.Code(get), status.Code(watch),
			"her watch and her get disagreed about another tenant's row")
	})

	// A tenant key cannot reach past its holder either: the methods on the row
	// are an attenuation of what that person may do, not a grant of their own.
	t.Run("a tenant key holds no more than its holder", func(t *testing.T) {
		x := require.New(t)

		// Bob may do nothing -- no binding at all.
		bob := addHolder(t, ctx, b.Server, b.Contoso, "bob")
		his := mintFor(t, ctx, b, bob, "his-script", []string{listHolders}, time.Time{})

		_, err := list(his)
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err),
			"the methods on a key granted what its holder does not hold")
	})
}

// TestThePrefixDecidesWhichDatabase is that a key of one kind is not a key of
// the other, however well-formed.
func TestThePrefixDecidesWhichDatabase(t *testing.T) {
	b := keyFor(t, verify)
	ctx := t.Context()

	// A real row in the data plane, and its token rewritten to claim to be the
	// deployment's. The bytes after the prefix are a real secret; what is wrong
	// is only which store is asked.
	tenant := mintFor(t, ctx, b, b.Who, "hers", []string{verify}, time.Time{})

	for _, tc := range []struct{ desc, token string }{
		{"a tenant key wearing the deployment prefix",
			keys.PrefixDeployment + tenant[len(keys.PrefixTenant):]},
		{"no prefix at all", tenant[len(keys.PrefixTenant):]},
		{"a prefix nobody issues", "rx_" + tenant[len(keys.PrefixTenant):]},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			_, err := app.NewVouchServiceClient(b.Conn).Verify(bearing(ctx, tc.token),
				app.VouchVerifyRequest_builder{
					Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
					Secret: []byte("whatever"),
				}.Build())
			x.Error(err)
			x.Equal(codes.Unauthenticated, status.Code(err))
		})
	}

	// The same row under its own prefix **is** a caller, so the three refusals
	// above are about the prefix and not about the row being unusable.
	//
	// It is still refused, and which refusal is the interesting part: not
	// `Unauthenticated` -- roster worked out who this is -- but
	// `PermissionDenied`, because the holder it resolved to has no binding
	// allowing `Verify`. The key's own `methods` said it may, and a key holds no
	// more than its holder.
	t.Run("and the same row under its own prefix is a caller", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewVouchServiceClient(b.Conn).Verify(bearing(ctx, tenant),
			app.VouchVerifyRequest_builder{
				Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
				Secret: []byte("whatever"),
			}.Build())
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err),
			"recognised as a caller, then refused by the rules -- not turned away at the door")
	})
}

// TestNobodyMintsAKeyForWhatTheyDoNotHold is the third place a method list is
// written down being held to the rule the other two are.
//
// A role has to be bound to somebody before it does anything. A key **is** the
// credential -- whoever holds the string calls whatever the column says -- so it
// is the most direct of the three, and it was the one `mayGrant` was not wired
// to.
//
// It was invisible because minting needed a shell, and `roster key add` writes
// through `Ungated`, where there is no frame and `mayGrant` is a no-op by
// design. A console is what removes the shell.
func TestNobodyMintsAKeyForWhatTheyDoNotHold(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	const mint = "/roster.ApiKeyService/Add"
	const listHolders = "/roster.HolderService/List"

	// Alice may mint keys, and may list holders. She may not erase one.
	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:   "minter",
		Methods: []string{mint, listHolders},
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// Written through the walled stack as Alice, which is what a console would
	// do. `Ungated` is the deployment's own door and is deliberately exempt.
	// The scope too, which `gate.Decide` fills in on the wire and nothing fills
	// in here. `ApiKeyService` is unregistered and closed, so there is no wire to
	// go over -- see `cmd/serve.go`. What is being tested is the layer, and the
	// layer runs behind the wall either way.
	as := frame.Into(ctx, frame.New(b.Who, b.Contoso, frame.Whole()).WithScope(frame.Only(b.Contoso)))

	add := func(alias string, methods []string) (*app.ApiKey, error) {
		return b.Walled.ApiKey().Add(as, app.ApiKeyAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
			Alias:   alias,
			Secret:  []byte(alias + "-not-a-real-verifier"),
			Methods: methods,
		}.Build())
	}

	var mine *app.ApiKey

	t.Run("what she holds she may put on a key", func(t *testing.T) {
		x := require.New(t)

		v, err := add("mine", []string{listHolders})
		x.NoError(err)

		mine = v
	})

	t.Run("what she does not hold she may not", func(t *testing.T) {
		x := require.New(t)

		_, err := add("wider", []string{"/roster.HolderService/Erase"})
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("nor may she widen one afterwards", func(t *testing.T) {
		x := require.New(t)
		x.NotNil(mine, "the key to widen was never written")

		_, err := b.Walled.ApiKey().Patch(as, app.ApiKeyPatchRequest_builder{
			Ref:         app.ApiKeyRef_builder{Id: mine.GetId()}.Build(),
			Methods:     []string{"/roster.HolderService/Erase"},
			DateUpdated: mine.GetDateUpdated(),
		}.Build())
		x.Error(err)
		x.Equal(codes.PermissionDenied, status.Code(err))
	})
}
