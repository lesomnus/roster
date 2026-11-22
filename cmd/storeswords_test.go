package cmd_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestTheStoresWordsAreNotSaidToWhoeverAsks: a store that could not be asked
// is answered in roster's words, and what the store said is in the log.
//
// # What it was
//
// With the control store away, a key nobody ever minted was answered
// `Unavailable: Bearer: auth: cannot say whether the credential is good: sql:
// database is closed`: the key's handler handed the store's error on, and the
// door puts a handler's error in the message whole. Whoever could reach the
// port was told the store's sentence without holding anything, and on
// PostgreSQL that sentence names the database's user, its name and its
// address. `Unavailable` is right, since it is what tells a client to come
// back; the words are not the caller's to read, and the operator who can do
// something about them reads the log. The same of a delegation and of a key
// narrowed by `roster-at`, which said `Unauthenticated`.
func TestTheStoresWordsAreNotSaidToWhoeverAsks(t *testing.T) {
	b := keyFor(t, app.HolderService_Get_FullMethodName)
	require.NoError(t, b.Control.Drv.Close())

	madeUp := keys.PrefixDeployment + strings.Repeat("A", 43)
	delegation := keys.PrefixDelegation + strings.Repeat("A", 43)

	// Each road a credential is read by, and the handler that reads it there.
	for name, road := range map[string]struct {
		md      []string
		handler auth.Handler
	}{
		"a key": {
			[]string{"authorization", "Bearer " + madeUp},
			auth.Bearer(keys.Store(b.Control.Ungated, b.Ungated)),
		},
		// The delegation's road and `roster-at`'s answered the same failure
		// `Unauthenticated`: the first in the store's words, the second in
		// none. That code tells a client to throw away a credential that may
		// be good, which the store being away is not.
		"a delegation": {
			[]string{"authorization", "Bearer " + madeUp, keys.HeaderActing, delegation},
			keys.Acting(b.Control.Ungated, b.Ungated),
		},
		"a key narrowed by roster-at": {
			[]string{"authorization", "Bearer " + madeUp, keys.HeaderAt, "@contoso"},
			keys.At(b.Control.Ungated, b.Ungated),
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			ctx := metadata.AppendToOutgoingContext(t.Context(), road.md...)
			_, err := app.NewHolderServiceClient(b.Conn).Get(ctx, app.HolderGetRequest_builder{
				Ref: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
			}.Build())
			x.Equal(codes.Unavailable, status.Code(err), "%v", err)
			x.NotContains(status.Convert(err).Message(), "sql", "the store's words were said to a stranger")
			x.NotContains(status.Convert(err).Message(), "closed", "the store's words were said to a stranger")

			// What the store said is in the log, at the level somebody is
			// woken for. Asked of the handler in process, whose log is the
			// context's: a call over the wire logs to the server's telemetry,
			// which a test does not have.
			said := &heard{}
			in := metadata.NewIncomingContext(log.Into(t.Context(), slog.New(slog.NewJSONHandler(said, nil))), metadata.Pairs(road.md...))
			_, err = road.handler.Handle(in)
			x.ErrorIs(err, auth.ErrUnavailable)
			logged := false
			for _, l := range said.lines(t, "database is closed") {
				if l["level"] == "ERROR" {
					logged = true
				}
			}
			x.True(logged, "the log does not say what the store said: %s", said.b.String())
		})
	}
}

// identifierPort is one port, somebody who may write groups, roles and
// teams there, their tenant, and a group in it.
type identifierPort struct {
	conn   *grpc.ClientConn
	as     context.Context
	tenant []byte
	group  []byte
}

// identifierPorts is a call at each of the two ports where a tenant's
// administrator or an operator writes groups, roles and teams.
func identifierPorts(t *testing.T) map[string]identifierPort {
	t.Helper()
	ctx := t.Context()

	out := map[string]identifierPort{}

	b, _ := build(t)
	b.mayAnything(b.ContosoUser, b.Contoso)
	data := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)
	group, err := app.NewGroupServiceClient(data).Add(wire, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Alias: "readers",
	}.Build())
	require.NoError(t, err)
	out["the data port"] = identifierPort{data, wire, b.Contoso.Bytes(), group.GetId()}

	s, c, printed := adminDeployment(t, nil)
	admin, as := adminPort(t, s, c, printed)
	tn, err := app.NewTenantServiceClient(admin).Add(as, app.TenantAddRequest_builder{Alias: "newco"}.Build())
	require.NoError(t, err)
	group, err = app.NewGroupServiceClient(admin).Add(as, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(), Alias: "readers",
	}.Build())
	require.NoError(t, err)
	out["the operators' port"] = identifierPort{admin, as, tn.GetId(), group.GetId()}

	return out
}

// TestAnIdentifierThatDoesNotReadIsInvalidArgument: a reference whose
// identifier is not one is the caller's mistake, said as one, at both ports.
//
// # What it was
//
// `GroupService/Update` with a reference of three bytes answered `Unknown:
// not a payday identifier: is 3 bytes, want 16` at both ports, and at the
// operators' port so did `GroupService/Add`, `RoleService/Add` and
// `TeamService/Add` naming their tenant so, where the data port said
// `InvalidArgument: id: got 3 bytes, want 16`. `Unknown` is a server's fault
// to every monitor and every page; the request was wrong, and it is said in
// the words every generated read already says it in.
func TestAnIdentifierThatDoesNotReadIsInvalidArgument(t *testing.T) {
	short := []byte{1, 2, 3}

	for port, p := range identifierPorts(t) {
		t.Run(port, func(t *testing.T) {
			got, err := app.NewGroupServiceClient(p.conn).Get(p.as, app.GroupGetRequest_builder{
				Ref: app.GroupRef_builder{Id: p.group}.Build(),
			}.Build())
			require.NoError(t, err)

			for name, call := range map[string]func() error{
				"GroupService/Update": func() error {
					_, err := app.NewGroupServiceClient(p.conn).Update(p.as, app.GroupUpdateRequest_builder{
						Ref:         app.GroupRef_builder{Id: short}.Build(),
						DateUpdated: timestamppb.New(got.GetDateUpdated().AsTime()),
						Name:        z.Ptr("Readers"),
					}.Build())
					return err
				},
				"GroupService/Add": func() error {
					_, err := app.NewGroupServiceClient(p.conn).Add(p.as, app.GroupAddRequest_builder{
						Tenant: app.TenantRef_builder{Id: short}.Build(), Alias: "writers",
					}.Build())
					return err
				},
				"RoleService/Add": func() error {
					_, err := app.NewRoleServiceClient(p.conn).Add(p.as, app.RoleAddRequest_builder{
						Tenant: app.TenantRef_builder{Id: short}.Build(), Alias: "writers",
					}.Build())
					return err
				},
				"TeamService/Add": func() error {
					_, err := app.NewTeamServiceClient(p.conn).Add(p.as, app.TeamAddRequest_builder{
						Tenant: app.TenantRef_builder{Id: short}.Build(), Alias: "writers",
					}.Build())
					return err
				},
			} {
				t.Run(name, func(t *testing.T) {
					err := call()
					require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
					require.Equal(t, "id: got 3 bytes, want 16", status.Convert(err).Message())
				})
			}
		})
	}
}

// TestHolderAddRefusesDataItCannotRead: `data` of a type roster does not
// know is refused as the caller's mistake by `Add`, as `Update` refuses it.
//
// # What it was
//
// `HolderService/Add` answered `Unknown: proto: google.protobuf.Any: unable
// to resolve "…"`, the store's encoder failing on the way in, where `Update`
// with the same answered `InvalidArgument`.
func TestHolderAddRefusesDataItCannotRead(t *testing.T) {
	b, ctx := build(t)
	b.mayAnything(b.ContosoUser, b.Contoso)
	conn := served(t, b.Server)
	wire := asOverTheWire(ctx, b.ContosoUser)

	nope := &anypb.Any{TypeUrl: "type.googleapis.com/nope.Nope", Value: []byte{1}}

	_, err := app.NewHolderServiceClient(conn).Add(wire, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "unreadable",
		Data:   nope,
	}.Build())
	require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
	require.True(t, strings.HasPrefix(status.Convert(err).Message(), "data: "), "%v", err)

	// And `Update` says the same of it.
	ref := app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build()
	was, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref}.Build())
	require.NoError(t, err)
	_, err = app.NewHolderServiceClient(conn).Update(wire, app.HolderUpdateRequest_builder{
		Ref: ref, DateUpdated: was.GetDateUpdated(), Data: nope,
	}.Build())
	require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
}
