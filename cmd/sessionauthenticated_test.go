package cmd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/session"
	"github.com/lesomnus/roster/server/vouch"
)

// TestASessionKeepsWhenItsPersonSignedIn.
//
// payday's sessions carry when somebody proved who they are
// (`authsession.Session.DateAuthenticated`, payday#265), and a store is to
// keep it. roster's did not: read back, every session's was unknown, and
// roster's own sign-in set none. A session keeps the moment its sign-in was
// finished -- the second factor, where there is one, and not the password
// before it -- and nothing that uses or rechecks it moves it.
func TestASessionKeepsWhenItsPersonSignedIn(t *testing.T) {
	t.Run("the store keeps it, and moving the idle clock does not move it", func(t *testing.T) {
		x := require.New(t)
		b := keyFor(t)
		ctx := t.Context()

		who := addHolder(t, ctx, b.Control, controlTenantOf(t, ctx, b), "ops")
		s := session.New(b.Control.Ent, audit.Contents{})

		signed := time.Now().Add(-time.Minute).Truncate(time.Second)
		v := authsession.Session{
			Key:               "a-cookie-value",
			Id:                who.String(),
			Grant:             frame.Whole(),
			DateAuthenticated: signed,
			Expires:           time.Now().Add(time.Hour),
			Idle:              time.Now().Add(10 * time.Minute),
		}
		x.NoError(s.Put(ctx, v))

		got, err := s.Get(ctx, "a-cookie-value")
		x.NoError(err)
		x.True(signed.Equal(got.DateAuthenticated), "when its person signed in was not kept: %s", got.DateAuthenticated)

		v.Idle = time.Now().Add(30 * time.Minute)
		v.DateAuthenticated = time.Now()
		x.NoError(s.Put(ctx, v))
		got, err = s.Get(ctx, "a-cookie-value")
		x.NoError(err)
		x.True(signed.Equal(got.DateAuthenticated), "the idle clock moved when its person signed in, to %s", got.DateAuthenticated)

		// And one that cannot say is unknown, as payday reads zero.
		x.NoError(s.Put(ctx, authsession.Session{Key: "another", Id: who.String(), Grant: frame.Whole()}))
		got, err = s.Get(ctx, "another")
		x.NoError(err)
		x.True(got.DateAuthenticated.IsZero())
	})

	t.Run("roster's own sign-in says it, at its last step", func(t *testing.T) {
		x := require.New(t)
		const password = "correct horse battery staple"

		b, ctx := build(t, func(c *cmd.Config) { c.SignIn.Enabled = true })
		_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Name: "contoso.example",
		}.Build())
		x.NoError(err)

		kim := b.holder(t, ctx, b.Contoso, "kim")
		seed := enrolled(t, ctx, b.Ungated.Credential(), b.keyedLocal(t), kim)

		auth := app.NewAuthServiceClient(pdtest.Serve(t, b.grpc(t)))
		at := metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example"))

		first, err := auth.SignIn(at, app.AuthSignInRequest_builder{Alias: "kim", Secret: []byte(password)}.Build())
		x.NoError(err)
		x.NotEmpty(first.GetContinuation())

		asked := time.Now()
		var h metadata.MD
		_, err = auth.SignIn(at, app.AuthSignInRequest_builder{
			Continuation: first.GetContinuation(), Kind: vouch.KindTotp,
			Secret: []byte(vouch.CodeAt(seed, time.Now().Unix()/30)),
		}.Build(), grpc.Header(&h))
		x.NoError(err)
		answered := time.Now()

		cookie := cookieOf(h)
		x.NotEmpty(cookie)
		got, err := session.New(b.Ent, audit.Contents{}).Get(context.Background(), cookie[strings.IndexByte(cookie, '=')+1:])
		x.NoError(err)
		x.False(got.DateAuthenticated.IsZero(), "roster's own sign-in said nothing of when its person signed in")
		x.False(got.DateAuthenticated.Before(asked.Truncate(time.Microsecond)), "the session says the password's moment and not the code's")
		x.False(got.DateAuthenticated.After(answered))
	})
}
