package cmd_test

import (
	"context"
	"testing"

	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"

	entsession "github.com/lesomnus/roster/internal/ent/session"
	"github.com/lesomnus/roster/server/session"
)

// rewriteGrant puts arbitrary bytes in a session's grant column.
//
// It goes to the connection rather than through the ent client because the
// column is `immutable`, so the generated update builder has no `SetGrant` at
// all -- which is the schema saying, correctly, that nothing in this app may
// change what a live session allows. That leaves the two ways the column can
// still end up holding something this app cannot read: a schema change under a
// running deployment, and a restore of rows written by an older one. Neither
// asks ent's permission, so neither can be staged through it.
//
// Built with ent's own Sql builder rather than written out, because the two
// databases this runs on do not spell a statement the same way: the
// placeholders differ, `?` against `$1`, and the quoting of identifiers with
// them. A literal here would pass on SQLite and fail on PostgreSql, which is
// the half of the matrix this test most wants to be right about.
func rewriteGrant(t *testing.T, ctx context.Context, s *cmd.Server, key string, grant []byte) {
	t.Helper()
	x := require.New(t)

	q, args := entsql.Dialect(s.Dialect).
		Update(entsession.Table).
		Set(entsession.FieldGrant, grant).
		Where(entsql.EQ(entsession.FieldSecret, session.Sum(key))).
		Query()

	res, err := s.Db.ExecContext(ctx, q, args...)
	x.NoError(err, "%s", q)

	n, err := res.RowsAffected()
	x.NoError(err)
	x.Equal(int64(1), n, "the session this test is about was not the row that changed")
}

// TestAGrantNothingCanReadAllowsNothing is `decode`'s other direction, which
// until now only ran on rows this process had just written itself.
//
// The happy path cannot tell the two failure directions apart: a grant that
// round trips looks the same whether the reader falls back to nothing or to
// everything when it cannot read one. The fallback only ever runs on a row
// some *other* version of this binary wrote -- a field renumbered, a column
// widened, a restore from before `payday.Grant` had the action axis -- so it
// is exactly the code that is never exercised until the morning it decides
// what a stranger's cookie is worth.
//
// Wrong by one line it is a privilege escalation with no attacker in it: every
// operator session in the table, including ones minted narrowed, silently
// becomes `frame.Whole`. Nothing logs it, because from the reader's side a
// whole grant is the ordinary answer. Right, it is a person signing in again.
func TestAGrantNothingCanReadAllowsNothing(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)
	c := signIn(t, s, "ops", passwordFrom(t, out))
	x.NotNil(c)

	// The test's own premise, asserted rather than assumed: these bytes have to
	// actually be unreadable as a `payday.Grant`, or this runs the happy path
	// under a name that claims otherwise and passes forever.
	bad := []byte("not a grant")
	x.Error(proto.Unmarshal(bad, &pdpb.Grant{}),
		"the bytes this test corrupts the column with parse as a grant")

	store := session.New(s.Control.Ent)

	// The contrast, from the same store and the same key one moment earlier:
	// what the console mints is whole, so anything short of whole below is this
	// row's grant becoming unreadable and nothing else.
	was, err := store.Get(ctx, c.Value)
	x.NoError(err)
	x.True(was.Grant.IsWhole(), "the console mints a session that narrows nothing")

	rewriteGrant(t, ctx, s.Control, c.Value, bad)

	got, err := store.Get(ctx, c.Value)
	x.NoError(err, "a row that cannot be read became a store error rather than an empty grant")

	// It is still a session and still names the operator -- that part of the
	// row is intact and pretending otherwise would be a second failure mode.
	// What it no longer is, is permission to do anything.
	x.Equal(was.Id, got.Id)
	x.False(got.Grant.IsWhole(), "an unreadable grant read as one that narrows nothing")
	x.False(got.Grant.Allows("/roster.HolderService/List"),
		"an unreadable grant allowed a method")
	x.False(got.Grant.Allows("/roster.MeService/Get"),
		"an unreadable grant allowed a method")

	// Every axis, because `frame.Grant`'s rule is that each carries a flag
	// beside its list and an empty list means **nothing**. A fallback that got
	// one axis right and left another's flag set would still hand out a
	// credential for every tenant in the deployment.
	x.False(got.Grant.AnyTenant())
	x.Empty(got.Grant.TenantIds())
	x.False(got.Grant.AnySet())
	x.Empty(got.Grant.SetIds())
	x.False(got.Grant.AnyAction())
	x.Empty(got.Grant.Actions())

	// And what that is worth on the wire, which is the only place it matters.
	//
	// `auth`'s interceptor checks the grant itself, once, before anything the
	// wall decides -- so a cookie whose grant no longer says anything is a
	// caller who is authenticated and may call nothing. Refused rather than
	// served, and refused as `PermissionDenied` rather than `Unauthenticated`:
	// the browser has a perfectly good session and the answer is that this
	// credential is not for this, which is what sends somebody to sign in
	// again instead of retrying the same cookie.
	t.Run("and the cookie opens nothing", func(t *testing.T) {
		x := require.New(t)

		conn := servedControl(t, s)
		as := metadata.NewOutgoingContext(ctx,
			metadata.Pairs("cookie", c.Name+"="+c.Value))

		_, err := app.NewMeServiceClient(conn).Get(as, app.MeGetRequest_builder{}.Build())
		x.Error(err, "an operator whose grant is unreadable was still served")
		x.Equal(codes.PermissionDenied, status.Code(err))

		_, err = app.NewHolderServiceClient(conn).List(as, app.HolderListRequest_builder{}.Build())
		x.Error(err, "an operator whose grant is unreadable still administered the deployment")
		x.Equal(codes.PermissionDenied, status.Code(err))
	})
}

// TestAnEmptyGrantColumnIsNotAWholeGrant is the same fallback reached the other
// way, and it is the way `decode`'s error branch does **not** catch.
//
// Empty bytes are a *valid* encoding of `payday.Grant` -- every field absent,
// which `proto.Unmarshal` accepts without complaint. So nothing here fails and
// nothing falls back; the whole weight is on the one rule the encoder owns,
// that an axis whose flag is unset and whose list is empty allows nothing. Get
// that backwards -- read a missing flag as "unrestricted", which is how most
// wire formats would have it -- and a column somebody's migration defaulted to
// empty is a table full of unrestricted sessions.
//
// That is not a hypothetical shape for this column: it is `bytes` and not
// optional, so a schema change that adds it to existing rows has exactly this
// value to add.
func TestAnEmptyGrantColumnIsNotAWholeGrant(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)
	c := signIn(t, s, "ops", passwordFrom(t, out))
	x.NotNil(c)

	x.NoError(proto.Unmarshal(nil, &pdpb.Grant{}),
		"empty bytes are meant to be a grant this reads without error")

	rewriteGrant(t, ctx, s.Control, c.Value, []byte{})

	got, err := session.New(s.Control.Ent).Get(ctx, c.Value)
	x.NoError(err)
	x.False(got.Grant.IsWhole(), "an empty grant column read as permission for everything")
	x.False(got.Grant.Allows("/roster.HolderService/List"))
	x.False(got.Grant.AnyTenant())
	x.False(got.Grant.AnySet())
	x.False(got.Grant.AnyAction())
}
