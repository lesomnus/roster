package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// These are docs/usage/ways-in.md § stopping one, asked at the doors a caller
// actually knocks on. The layer tests beside these prove each stop is decided
// correctly; what they cannot prove is that every served stack asks -- and a
// stop that one port forgets to ask about is not a stop, it is a race between
// the operator and whoever they are stopping.

// TestInvalidateEndsTheConsolesOwnSessions is `MeService.SignOutEverywhere`
// pressed where roster itself is the app in front.
//
// The contract sounds like it forbids this: roster answers *invalid since
// when*, an app answers *what is still alive*, and ending sessions is the
// app's half. For a product app that is the whole story. For the **console**
// both halves are roster's -- the session table is `server/session`, kept by
// the same deployment that wrote the stamp -- and until 2026-08-28 that store
// never read it. So the button voided a customer's delegations and left every
// console session alive, including the one a takeover had opened, which is
// the session the button exists to end.
func TestInvalidateEndsTheConsolesOwnSessions(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)
	pw := passwordFrom(t, out)

	// Two browsers, because "everywhere" is a claim about the sessions the
	// caller is *not* holding.
	one := signIn(t, s, "ops", pw)
	two := signIn(t, s, "ops", pw)
	x.NotNil(one)
	x.NotNil(two)

	g, err := s.Control.Grpc(ctx, cmd.Config{})
	x.NoError(err)
	conn := pdtest.Serve(t, g)

	with := func(name, value string) metadata.MD {
		return metadata.Pairs("cookie", name+"="+value)
	}

	me := app.NewMeServiceClient(conn)

	for _, c := range []*struct{ Name, Value string }{
		{one.Name, one.Value}, {two.Name, two.Value},
	} {
		_, err := me.Get(metadata.NewOutgoingContext(ctx, with(c.Name, c.Value)),
			app.MeGetRequest_builder{}.Build())
		x.NoError(err, "the baseline: both browsers are signed in")
	}

	// The stamp is `time.Now()` and SQLite's clock is coarse; the sleep is
	// what makes "issued before" mean before, as in `disable_test`.
	time.Sleep(2 * time.Millisecond)

	_, err = me.SignOutEverywhere(metadata.NewOutgoingContext(ctx, with(one.Name, one.Value)),
		app.MeSignOutEverywhereRequest_builder{}.Build())
	x.NoError(err)
	time.Sleep(2 * time.Millisecond)

	t.Run("the other browser is signed out", func(t *testing.T) {
		x := require.New(t)

		_, err := me.Get(metadata.NewOutgoingContext(ctx, with(two.Name, two.Value)),
			app.MeGetRequest_builder{}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err),
			"a session issued before the stamp was served after it")
	})

	t.Run("and so is the one that pressed the button", func(t *testing.T) {
		x := require.New(t)

		_, err := me.Get(metadata.NewOutgoingContext(ctx, with(one.Name, one.Value)),
			app.MeGetRequest_builder{}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	t.Run("and signing back in works", func(t *testing.T) {
		x := require.New(t)

		again := signIn(t, s, "ops", pw)
		x.NotNil(again, "an invalidation that cannot be recovered from is an erasure")

		_, err := me.Get(metadata.NewOutgoingContext(ctx, with(again.Name, again.Value)),
			app.MeGetRequest_builder{}.Build())
		x.NoError(err)
	})
}

// TestAnErasedHolderIsStoppedAtTheWire is the erase, felt by the two things
// the erased person could still be holding: a key, and a password somebody
// might present for them.
//
// Both stops are proven in-process already; this is the served stack asking.
// The rt_ is the case nothing anywhere covered: no test had ever presented an
// erased holder's key to a port.
func TestAnErasedHolderIsStoppedAtTheWire(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	b := keyFor(t, verify)

	_, err := vouch.New(b.Ungated, b.Ungated).Set(ctx, app.VouchSetRequest_builder{
		Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	key := mintFor(t, ctx, b, b.Who, "hers", []string{"/roster.MeService/Get"}, time.Time{})

	// The baseline, or the refusals below prove nothing.
	_, err = app.NewMeServiceClient(b.Conn).Get(bearing(ctx, key),
		app.MeGetRequest_builder{}.Build())
	x.NoError(err)

	v, err := app.NewVouchServiceClient(b.Conn).Verify(bearing(ctx, b.Token),
		app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
	x.NoError(err)
	x.True(v.GetOk())

	_, err = b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: b.Who.Bytes()}.Build())
	x.NoError(err)

	t.Run("their key finds nothing", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewMeServiceClient(b.Conn).Get(bearing(ctx, key),
			app.MeGetRequest_builder{}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err),
			"an erased holder's key was served")
	})

	t.Run("and their password answers no, not who", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewVouchServiceClient(b.Conn).Verify(bearing(ctx, b.Token),
			app.VouchVerifyRequest_builder{
				Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
				Secret: []byte("correct horse battery staple"),
			}.Build())
		x.NoError(err, "the request was fine; the answer is no")
		x.False(v.GetOk())
		x.Empty(v.GetHolder(), "a refusal named who it refused")
	})
}

// TestADisabledHolderIsRefusedBareOverTheWire is the suspension, read through
// the port a login app actually calls.
//
// The shape of the answer is the assertion: `ok:false` with **nothing else on
// it**. A `locked_until` would say "this account exists and is locked", which
// is more than a suspended person's login form should learn -- the in-process
// test pins that reasoning, and this pins that the served stack, with its
// interceptors between, answers the same bytes.
func TestADisabledHolderIsRefusedBareOverTheWire(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	b := keyFor(t, verify)

	_, err := vouch.New(b.Ungated, b.Ungated).Set(ctx, app.VouchSetRequest_builder{
		Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	disables(t, ctx, b, b.Who)

	v, err := app.NewVouchServiceClient(b.Conn).Verify(bearing(ctx, b.Token),
		app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret: []byte("correct horse battery staple"),
		}.Build())
	x.NoError(err)
	x.False(v.GetOk(), "a suspended person's correct password is still a no")
	x.Empty(v.GetHolder())
	x.Nil(v.GetLockedUntil(), "a suspension is not a lockout, and must not read as one")
}

// TestEveryNoOverTheWireIsTheSameNo is the indistinguishability rule, read
// through the served stack rather than off the layer that decides it.
//
// Four different facts -- a wrong password, an alias nobody has, a tenant that
// does not exist, an account with no password at all -- and one answer for the
// lot, compared as messages: a login form that can tell them apart is an
// enumeration oracle for whichever it can tell. The in-process tests pin each
// refusal; what they cannot pin is that no interceptor between here and the
// handler decorates one of them differently.
func TestEveryNoOverTheWireIsTheSameNo(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	b := keyFor(t, verify)

	_, err := vouch.New(b.Ungated, b.Ungated).Set(ctx, app.VouchSetRequest_builder{
		Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	// Somebody who exists and has no password, so the fourth case is a real
	// row rather than a misspelling of the second.
	addHolder(t, ctx, b.Server, b.Contoso, "bare")

	c := app.NewVouchServiceClient(b.Conn)
	ask := func(tenant, alias, secret string) *app.VouchVerifyResponse {
		t.Helper()

		v, err := c.Verify(bearing(ctx, b.Token), app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Tenant: tenant, Alias: alias}.Build(),
			Secret: []byte(secret),
		}.Build())
		require.NoError(t, err, "each of these is an answer, not an error")

		return v
	}

	first := ask("contoso", "someone", "wrong")
	for desc, v := range map[string]*app.VouchVerifyResponse{
		"an alias nobody has":     ask("contoso", "nobody", "wrong"),
		"a tenant nobody has":     ask("initech", "someone", "wrong"),
		"an account with no ways": ask("contoso", "bare", "wrong"),
	} {
		x.True(proto.Equal(first, v),
			"%s answers differently from a wrong password:\n%v\n%v", desc, first, v)
	}
	x.False(first.GetOk())
}
