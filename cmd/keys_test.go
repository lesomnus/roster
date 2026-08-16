package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/vouch"
)

// roster answering to keys rather than to whoever its callers say they are.
//
// The control plane is roster again, in this process, on its own database: one
// tenant for the owner, a holder per service, and keys under those. See
// PLAN.md, D15.

// keyed is a deployment with a control plane, and a key for one service.
type keyedBuilt struct {
	*cmd.Server

	// Config is what built it, kept because a test that stands the same
	// deployment up on a port -- or runs a command against it -- needs the same
	// databases rather than two fresh ones.
	Config cmd.Config

	Conn  *grpc.ClientConn
	Acme  pdid.Id
	Who   pdid.Id
	Token string
}

// keyFor stands a deployment up and mints a key allowing exactly `methods`.
func keyFor(t *testing.T, methods ...string) *keyedBuilt {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	x.NotNil(s.Control, "no control plane was built")
	x.NoError(s.Ent.Schema.Create(ctx))
	x.NoError(s.Control.Ent.Schema.Create(ctx))

	// The data plane: a customer and somebody in it.
	acme := add(t, ctx, s, "acme")
	who := addHolder(t, ctx, s, acme, "someone")

	// The control plane: the owner, one service, one key.
	k := add(t, ctx, s.Control, "k")
	svc := addHolder(t, ctx, s.Control, k, "custody")

	token, sum, err := keys.Mint(keys.PrefixDeployment)
	x.NoError(err)

	_, err = s.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: svc.Bytes()}.Build(),
		Alias:   "production",
		Secret:  sum,
		Methods: methods,
	}.Build())
	x.NoError(err)

	return &keyedBuilt{
		Server: s,
		Config: c,
		Conn:   served(t, s),
		Acme:   acme, Who: who, Token: token,
	}
}

func add(t *testing.T, ctx context.Context, s *cmd.Server, alias string) pdid.Id {
	t.Helper()

	v, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias}.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

func addHolder(t *testing.T, ctx context.Context, s *cmd.Server, in pdid.Id, alias string) pdid.Id {
	t.Helper()

	v, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

// bearing is a call carrying a token, the way a service makes one.
func bearing(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

const verify = "/roster.VouchService/Verify"

// TestAKeyReachesWhatItWasMadeFor is the whole of it.
func TestAKeyReachesWhatItWasMadeFor(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	// A password to check, set through the server rather than by hand.
	_, err := vouch.New(b.Ungated, b.Ungated).Set(ctx, app.VouchSetRequest_builder{
		Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	v, err := app.NewVouchServiceClient(b.Conn).Verify(bearing(ctx, b.Token),
		app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
	x.NoError(err)
	x.True(v.GetOk())
	x.Equal(b.Who.Bytes(), v.GetHolder())
}

// TestAKeyReachesNothingElse, which is what makes it a key rather than a
// password for the whole deployment.
//
// The grant is a list of methods on the row, read by the interceptor before the
// handler. The scope is `Everything` -- a key belongs to the deployment and the
// deployment is every tenant in it -- so this is the **only** thing narrowing
// it, and it has to hold.
func TestAKeyReachesNothingElse(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := bearing(t.Context(), b.Token)

	_, err := app.NewHolderServiceClient(b.Conn).Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"a key for Verify read a person")

	_, err = app.NewHolderServiceClient(b.Conn).Erase(ctx,
		app.HolderRef_builder{Id: b.Who.Bytes()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "a key for Verify erased somebody")

	// And a key made for that method reaches it, so what refused above is the
	// grant and not some other thing that would have refused anyway.
	//
	// It also shows the scope: this key is in no tenant at all, and reads a
	// person in `acme` -- `frame.Everything`, narrowed only by the list of
	// methods on the row.
	other := keyFor(t, "/roster.HolderService/Get")
	v, err := app.NewHolderServiceClient(other.Conn).Get(bearing(t.Context(), other.Token),
		app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Id: other.Who.Bytes()}.Build(),
		}.Build())
	x.NoError(err)
	x.Equal("someone", v.GetAlias())
}

// TestAKeyThatAllowsNothingOpensNoDoor, which is `frame.Grant`'s zero value and
// the right way round: a row somebody forgot to fill in is refused rather than
// trusted.
func TestAKeyThatAllowsNothingOpensNoDoor(t *testing.T) {
	x := require.New(t)
	b := keyFor(t)

	_, err := app.NewVouchServiceClient(b.Conn).Verify(bearing(t.Context(), b.Token),
		app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret: []byte("hunter2"),
		}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
}

// TestNoKeyIsNoAnswer. With a control plane configured, nothing is served to a
// caller who did not present one -- `public` names nothing.
func TestNoKeyIsNoAnswer(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)

	_, err := app.NewVouchServiceClient(b.Conn).Verify(t.Context(),
		app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret: []byte("hunter2"),
		}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err))
}

// TestAKeyNobodyMintedIsRefused, and one that was tampered with reads the same.
func TestAKeyNobodyMintedIsRefused(t *testing.T) {
	b := keyFor(t, verify)

	for _, tt := range []struct {
		what  string
		token string
	}{
		{"not ours", "hunter2"},
		{"right shape, never minted", keys.PrefixDeployment + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"one byte off", b.Token[:len(b.Token)-1] + "x"},
	} {
		t.Run(tt.what, func(t *testing.T) {
			_, err := app.NewVouchServiceClient(b.Conn).Verify(bearing(t.Context(), tt.token),
				app.VouchVerifyRequest_builder{
					Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
					Secret: []byte("hunter2"),
				}.Build())
			require.Equal(t, codes.Unauthenticated, status.Code(err))
		})
	}
}

// TestRevokingAKeyStopsItAtOnce, which is the whole reason a key is a row.
func TestRevokingAKeyStopsItAtOnce(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	v, err := b.Control.Ent.ApiKey.Query().Only(ctx)
	x.NoError(err)

	_, err = b.Control.Ungated.ApiKey().Erase(ctx,
		app.ApiKeyRef_builder{Id: v.ID[:]}.Build())
	x.NoError(err)

	_, err = app.NewVouchServiceClient(b.Conn).Verify(bearing(ctx, b.Token),
		app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret: []byte("hunter2"),
		}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err))
}

// TestAnExpiredKeyIsRefused.
func TestAnExpiredKeyIsRefused(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	v, err := b.Control.Ent.ApiKey.Query().Only(ctx)
	x.NoError(err)

	_, err = b.Control.Ungated.ApiKey().Patch(ctx, app.ApiKeyPatchRequest_builder{
		Ref:              app.ApiKeyRef_builder{Id: v.ID[:]}.Build(),
		DateExpires:      timestamppb.New(time.Now().Add(-time.Minute)),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	_, err = app.NewVouchServiceClient(b.Conn).Verify(bearing(ctx, b.Token),
		app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret: []byte("hunter2"),
		}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err))
}

// TestTheKeyIsNotWhatIsStored.
func TestTheKeyIsNotWhatIsStored(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)

	v, err := b.Control.Ent.ApiKey.Query().Only(t.Context())
	x.NoError(err)

	x.NotContains(string(v.Secret), b.Token)
	x.Equal(keys.Sum(b.Token), v.Secret)
	x.Len(v.Secret, 32, "a SHA-256 is thirty-two bytes")
}

// TestTheTwoPlanesShareNothing, which is why a key cannot be reached by a fault
// in the wall: there is no query from one to the other.
func TestTheTwoPlanesShareNothing(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()

	// The data plane holds acme and its person, and knows nothing of keys.
	n, err := b.Ent.ApiKey.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n)

	// The control plane holds the owner and the service, and knows nothing of
	// acme.
	vs, err := b.Control.Ent.Tenant.Query().All(ctx)
	x.NoError(err)
	x.Len(vs, 1)
	x.Equal("k", vs[0].Alias)
}

// TestABatchIsTheSameKey, which is what `batch.Guard` exists for.
//
// A batch arrives as one method carrying many, so everything payday enforces by
// method name is enforced against `BatchService/Do` unless the guard applies it
// per operation. A key's scope comes from the policy, so a guard built without
// one would serve a key in a batch as a caller who may see nothing -- and its
// grant would go unchecked, which is the wider hole.
func TestABatchIsTheSameKey(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, "/roster.HolderService/Add", pdpb.BatchService_Do_FullMethodName)
	ctx := bearing(t.Context(), b.Token)

	op := func(method string, m proto.Message) *pdpb.Op {
		v, err := anypb.New(m)
		x.NoError(err)

		return pdpb.Op_builder{Method: method, Request: v}.Build()
	}

	// What the key is for, in a batch: it lands.
	_, err := pdpb.NewBatchServiceClient(b.Conn).Do(ctx, pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{op("/roster.HolderService/Add",
			app.HolderAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Acme.Bytes()}.Build(),
				Alias:  "another",
			}.Build())},
	}.Build())
	x.NoError(err)

	n, err := b.Ent.Holder.Query().Count(t.Context())
	x.NoError(err)
	x.Equal(2, n, "the batch did not write")

	// And what it is not for does not become allowed by being wrapped.
	_, err = pdpb.NewBatchServiceClient(b.Conn).Do(ctx, pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{op("/roster.HolderService/Erase",
			app.HolderRef_builder{Id: b.Who.Bytes()}.Build())},
	}.Build())
	x.Error(err, "a batch carried what the key may not call")
	x.Equal(codes.PermissionDenied, status.Code(err))
}

// TestTheFirstKeyMakesWhatItNeeds, which is what `roster key add --service
// custody` does against an empty control plane.
//
// A service is not something an operator creates on purpose before they need
// it: naming it in `key add` **is** the moment it becomes a caller. Asking for
// three commands to express one intent is how a runbook grows a step nobody
// remembers.
func TestTheFirstKeyMakesWhatItNeeds(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Control.Ent.Schema.Create(ctx))

	// Nothing at all yet.
	n, err := s.Control.Ent.Tenant.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n)

	who, err := cmd.ServiceOf(ctx, s.Control, "custody")
	x.NoError(err)

	// The owner's tenant and the service, made on the way.
	v, err := s.Control.Ent.Holder.Query().Only(ctx)
	x.NoError(err)
	x.Equal("custody", v.Alias)
	x.Equal(who.String(), pdid.Id(v.ID).String())

	// And asking again is the same service rather than a second one.
	again, err := cmd.ServiceOf(ctx, s.Control, "custody")
	x.NoError(err)
	x.Equal(who, again)

	n, err = s.Control.Ent.Holder.Query().Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "a second key made a second service")
}
