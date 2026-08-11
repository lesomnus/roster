package cmd_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/pd"
)

// forged is an identifier in the api-key domain that names no row anywhere.
//
// A `pdid` carries its domain in a byte of the identifier, so making one is
// arithmetic rather than a lookup -- which is the whole of what the test below
// is about.
func forged() pdid.Id {
	return pdid.New(pd.ApiKeyDomain)
}

// as is a call claiming to be somebody, the way `auth.Plain` reads one.
func as(t *testing.T, who string) metadata.MD {
	t.Helper()

	return metadata.Pairs("authorization", "Plain "+who)
}

// TestPlainDoesNotHandOutEveryTenant is the deployment that named no control
// plane, which `auth.Plain` serves and which OPERATING.md calls "believes its
// callers".
//
// It believes them about **who they are**. What it must not do is let a caller
// choose to be a *kind* of thing that the policy treats as the deployment
// itself -- and that is what an api-key identifier was, because `policy.Where`
// reads the domain byte and answers `frame.Everything` without touching a row.
//
// So the claim is not "Plain is safe". It is that Plain's blast radius is
// impersonating somebody, and not crossing the wall between two customers who
// have never heard of each other.
func TestPlainDoesNotHandOutEveryTenant(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	s, err := cmd.Build(ctx, cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},

		// No control plane, so `auth.Plain` -- what a checkout gets.
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))
	x.Nil(s.Control, "this deployment names no control plane")

	// Two customers who must never see each other.
	acme := add(t, ctx, s, "acme")
	hooli := add(t, ctx, s, "hooli")
	alice := addHolder(t, ctx, s, acme, "alice")
	addHolder(t, ctx, s, hooli, "bob")

	// Alice may list holders, so that the baseline below is a read that
	// succeeds. Without one, deny-by-default refuses her and the forgery could
	// pass for a reason that has nothing to do with what it names.
	const listHolders = "/roster.HolderService/List"

	role, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: acme.Bytes()}.Build(),
		Alias:   "reader",
		Methods: []string{listHolders},
	}.Build())
	x.NoError(err)

	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	conn := pdtest.Serve(t, s.Grpc(ctx, cmd.Config{}))
	c := app.NewHolderServiceClient(conn)

	list := func(md metadata.MD) (*app.HolderListResponse, error) {
		return c.List(metadata.NewOutgoingContext(ctx, md),
			app.HolderListRequest_builder{}.Build())
	}

	// An ordinary caller sees their own tenant and no more, which is the
	// baseline this is measured against -- without it, a refusal below could be
	// nothing working at all.
	t.Run("a person sees one tenant", func(t *testing.T) {
		x := require.New(t)

		v, err := list(as(t, "@acme/alice"))
		x.NoError(err)
		x.Len(v.GetItems(), 1)
		x.Equal("alice", v.GetItems()[0].GetAlias())
	})

	// The forgery. Nobody minted this, it is in no database, and until now the
	// domain byte alone was enough to be handed every tenant there is.
	t.Run("an identifier nobody minted sees none", func(t *testing.T) {
		x := require.New(t)

		_, err := list(as(t, forged().String()))
		x.Error(err, "a key that names no row was served")

		code := status.Code(err)
		x.True(code == codes.Unauthenticated || code == codes.PermissionDenied,
			"refused as a credential rather than by finding nothing: got %s", code)
	})

	// And the same shape with a well-formed identifier that is simply not one:
	// a holder identifier that names nobody is refused, so that the case above
	// is not passing for a reason that has nothing to do with keys.
	t.Run("a holder nobody added sees none either", func(t *testing.T) {
		x := require.New(t)

		_, err := list(as(t, pdid.Id(uuid.New()).String()))
		x.Error(err)
	})
}

// TestAKeyIsARowThatExists is the same rule where there **is** a control plane.
//
// The deployment above has none, so nothing could have been minted and every
// api-key identifier is a forgery. Here keys are real, and the question is
// whether being one is still a claim that gets checked -- asked of the resolver
// directly, because `auth.Bearer` looks a token up rather than believing an
// identifier, so there is no way to put a forged one on the wire.
//
// That is worth saying plainly: with a control plane the forgery is unreachable
// today. This holds the resolver to it anyway, since what stops it is a wiring
// choice in `Grpc` and not anything the resolver knows.
func TestAKeyIsARowThatExists(t *testing.T) {
	ctx := t.Context()
	b := keyFor(t, "/roster.VouchService/Verify")

	r := cmd.Resolver(b.Ungated, b.Control.Ungated)

	t.Run("a key that was minted resolves", func(t *testing.T) {
		x := require.New(t)

		// The identity `keys.Store` answers with, obtained the way it is: by
		// presenting the token to the store.
		id, err := keys.Store(b.Control.Ungated).Lookup(ctx, b.Token)
		x.NoError(err)

		f, err := r.Resolve(ctx, id)
		x.NoError(err)
		x.Equal(id.Id, f.Actor.String())
		x.Equal(pdid.Nil, f.Tenant, "a key belongs to the deployment")
	})

	t.Run("an identifier shaped like a key but naming no row does not", func(t *testing.T) {
		x := require.New(t)

		_, err := r.Resolve(ctx, auth.Identity{Id: forged().String()})
		x.Error(err)
		x.ErrorIs(err, auth.ErrNoCredential,
			"a credential that is present and wrong, not one that is missing")
	})
}
