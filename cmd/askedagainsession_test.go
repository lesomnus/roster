package cmd_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	app "github.com/lesomnus/roster/rstr"
)

// TestAskingAnOpenStreamAgainIsNotUseOfItsSession: a console session whose
// only activity is an open stream ends at its idle deadline.
//
// # What it was
//
// A stream is asked every `stream.recheck` who opened it ([cmd.AskedAgain]),
// and a session was asked through `authsession.Sessions.Handler`, which is a
// read that counts as use: past half its idle window it moved the deadline. So
// a page left open on a `Watch` and calling nothing kept its session alive for
// as long as the tab stayed open, and only the absolute clock ended it --
// where the idle clock is there for exactly that tab (#275). Asked again, the
// session reads as it is and is written nothing; a call is still use.
func TestAskingAnOpenStreamAgainIsNotUseOfItsSession(t *testing.T) {
	const every = 20 * time.Millisecond

	// An operator's cookie, from the admin port, as a console signs in.
	operator := func(t *testing.T, s *cmd.Server, out string) metadata.MD {
		c := signIn(t, s, "admin", passwordFrom(t, out))
		require.NotNil(t, c)

		return metadata.Pairs("cookie", c.Name+"="+c.Value)
	}

	for name, at := range map[string]func(*testing.T) askedAgainSession{
		// The admin port, which is the one an operator's console calls and
		// which serves the customers' rows.
		"admin": func(t *testing.T) askedAgainSession {
			s, out := initedWith(t, func(c *cmd.Config) { c.Stream.Recheck = every })
			md := operator(t, s, out)
			g, err := s.GrpcAdmin(t.Context(), cmd.Config{})
			require.NoError(t, err)

			tn, err := s.Ungated.Tenant().Add(t.Context(), app.TenantAddRequest_builder{Alias: "contoso"}.Build())
			require.NoError(t, err)
			alice, err := s.Ungated.Holder().Add(t.Context(), app.HolderAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(), Alias: "alice",
			}.Build())
			require.NoError(t, err)

			return askedAgainSession{pdtest.Serve(t, g), metadata.NewOutgoingContext(t.Context(), md), alice.GetId(), s.Control.Ent}
		},

		// The control plane's own listener, which serves the operators' rows.
		"control": func(t *testing.T) askedAgainSession {
			s, out := initedWith(t, func(c *cmd.Config) { c.Stream.Recheck = every })
			md := operator(t, s, out)
			g, err := s.Control.Grpc(t.Context(), cmd.Config{})
			require.NoError(t, err)

			admin, err := s.Control.Ent.Holder.Query().Where(entholder.Alias("admin")).Only(t.Context())
			require.NoError(t, err)

			return askedAgainSession{pdtest.Serve(t, g), metadata.NewOutgoingContext(t.Context(), md), admin.Id[:], s.Control.Ent}
		},

		// The data plane, where a deployment signs its own people in
		// (`sign_in.enabled`), with a control plane below it.
		"data": func(t *testing.T) askedAgainSession {
			s, _ := initedWith(t, func(c *cmd.Config) {
				c.Stream.Recheck = every
				c.SignIn.Enabled = true
			})

			return signedInAtTheData(t, s)
		},

		// And with none: a cookie, and then whoever the deployment believes
		// (`auth.Seq(People.Recheck(), auth.Plain())`, `cmd/serve.go`). The
		// chain is another one, and it was asked again the same way and
		// opened by no test.
		"data, with no control plane": func(t *testing.T) askedAgainSession {
			b, _ := build(t, func(c *cmd.Config) {
				c.Stream.Recheck = every
				c.SignIn.Enabled = true
			})
			require.Nil(t, b.Control, "a control plane was built, so this proves nothing")

			return signedInAtTheData(t, b.Server)
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			ctx := t.Context()
			v := at(t)

			// The stream, opened while the session is fresh, so that opening
			// it moves nothing either way.
			w, err := app.NewHolderServiceClient(v.conn).Watch(v.as, app.HolderWatchRequest_builder{
				Filters: []*app.HolderFilter{app.HolderFilter_builder{
					Ref: app.HolderRef_builder{Id: v.watched}.Build(),
				}.Build()},
			}.Build())
			x.NoError(err)
			_, err = w.Recv()
			x.NoError(err, "the control: the stream opens")

			// And then nothing is called for most of the idle window: the
			// deadline is put a quarter of a window away, past the half that a
			// use moves it at.
			row, err := v.rows.Session.Query().Only(ctx)
			x.NoError(err)
			idle := time.Now().Add(authsession.DefaultIdle / 4).Truncate(time.Second)
			x.NoError(row.Update().SetDateIdle(idle).Exec(ctx))

			// Asked again a few times over, the stream still open.
			time.Sleep(6 * every)

			row, err = v.rows.Session.Query().Only(ctx)
			x.NoError(err)
			x.NotNil(row.DateIdle)
			x.True(idle.Equal(*row.DateIdle), "asking an open stream again moved its session's idle deadline from %s to %s", idle, *row.DateIdle)

			// The control: a call is use, and moves it.
			_, err = app.NewHolderServiceClient(v.conn).Get(v.as, app.HolderGetRequest_builder{
				Ref: app.HolderRef_builder{Id: v.watched}.Build(),
			}.Build())
			x.NoError(err)
			row, err = v.rows.Session.Query().Only(ctx)
			x.NoError(err)
			x.True(row.DateIdle.After(idle), "a call is use of the session, and moves its idle deadline")
		})
	}
}

// askedAgainSession is one place a session is a way in: the listener, a
// cookie for it, a row its holder may watch, and the plane its sessions are
// rows of.
type askedAgainSession struct {
	conn    *grpc.ClientConn
	as      context.Context
	watched []byte
	rows    *ent.Client
}

// signedInAtTheData is [TestAskingAnOpenStreamAgainIsNotUseOfItsSession]'s
// place on the data plane: a person of a tenant, signed in at the tenant's
// name, with a role that lets them watch their own row.
func signedInAtTheData(t *testing.T, s *cmd.Server) askedAgainSession {
	ctx := t.Context()

	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "tailspin"}.Build())
	require.NoError(t, err)
	_, err = s.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(), Name: "tailspin.example",
	}.Build())
	require.NoError(t, err)
	alice, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(), Alias: "alice",
	}.Build())
	require.NoError(t, err)
	role, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(), Alias: "watches",
		Methods: []string{app.HolderService_Watch_FullMethodName, app.HolderService_Get_FullMethodName},
	}.Build())
	require.NoError(t, err)
	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: alice.GetId()}.Build(),
	}.Build())
	require.NoError(t, err)
	secret, err := s.Ungated.Credential().Issue(ctx, app.CredentialIssueRequest_builder{
		Holder: app.HolderRef_builder{Id: alice.GetId()}.Build(),
	}.Build())
	require.NoError(t, err)

	g, err := s.Grpc(ctx, cmd.Config{})
	require.NoError(t, err)
	conn := pdtest.Serve(t, g)

	var h metadata.MD
	_, err = app.NewAuthServiceClient(conn).SignIn(metadata.NewOutgoingContext(ctx, arrivingAt(t, "tailspin.example")),
		app.AuthSignInRequest_builder{Alias: "alice", Secret: []byte(secret.GetSecret())}.Build(), grpc.Header(&h))
	require.NoError(t, err)
	res := http.Response{Header: http.Header{"Set-Cookie": h.Get("set-cookie")}}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Value != "" {
			cookie = c
		}
	}
	require.NotNil(t, cookie, "the person was handed no cookie")

	as := metadata.NewOutgoingContext(ctx, metadata.Join(
		metadata.Pairs("cookie", cookie.Name+"="+cookie.Value), arrivingAt(t, "tailspin.example")))

	return askedAgainSession{conn, as, alice.GetId(), s.Ent}
}
