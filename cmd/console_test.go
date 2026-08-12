package cmd_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/internal/ent"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// signIn posts a password the way a console does, and answers with the cookie.
func signIn(t *testing.T, s *cmd.Server, alias, password string) *http.Cookie {
	t.Helper()
	x := require.New(t)

	body, err := json.Marshal(map[string]string{"alias": alias, "password": password})
	x.NoError(err)

	r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Sessions.Serve(cmd.Login(s.Control)).ServeHTTP(w, r)

	res := w.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	if res.StatusCode != http.StatusNoContent {
		return nil
	}
	for _, c := range res.Cookies() {
		if c.Value != "" {
			return c
		}
	}

	return nil
}

// TestAnOperatorSignsIn is the console's front door, end to end from what
// `roster init` printed.
//
// It is the seam payday left and could not fill: `auth` reads a credential and
// does not issue one, and issuing is an HTTP endpoint. A browser has nowhere
// safe to keep a secret, so what it gets is an opaque cookie naming a session
// this server keeps.
func TestAnOperatorSignsIn(t *testing.T) {
	x := require.New(t)

	s, out := inited(t, true)
	x.NotNil(s.Sessions, "a deployment with a control plane has a console door")

	secret := passwordFrom(t, out)

	t.Run("with what init printed", func(t *testing.T) {
		x := require.New(t)

		c := signIn(t, s, "ops", secret)
		x.NotNil(c, "the password init printed does not sign in")
		x.True(c.HttpOnly, "a cookie script can read is one script can send elsewhere")
		x.Equal(http.SameSiteLaxMode, c.SameSite)
		x.NotEmpty(c.Value)
	})

	t.Run("and not with a wrong password", func(t *testing.T) {
		x := require.New(t)
		x.Nil(signIn(t, s, "ops", secret+"x"))
	})

	t.Run("nor as somebody who is not there", func(t *testing.T) {
		x := require.New(t)
		x.Nil(signIn(t, s, "nobody", secret))
	})

	// The data plane's admin is not an operator. `init` prints them as somebody
	// to sign in as, and what they sign in to is a product app -- roster's own
	// console is the deployment's, and a customer's people are the customer's.
	t.Run("nor as a data plane holder", func(t *testing.T) {
		x := require.New(t)
		x.Nil(signIn(t, s, "admin", secret))
	})
}

// TestNoControlPlaneNoConsole -- a deployment that believes its callers has
// nobody to be, so the door is not there to knock on.
func TestNoControlPlaneNoConsole(t *testing.T) {
	x := require.New(t)

	s, _ := inited(t, false)
	x.Nil(s.Control)
	x.Nil(s.Sessions, "a console door was opened where nobody can sign in")
}

// TestTheCookieOpensTheControlPlane is the other half: signing in is only worth
// something if the cookie is a credential the server reads back.
//
// It also pins where that is true. A session names a control plane holder, so
// it resolves only where that row is — put on the data plane's chain it would
// authenticate and then resolve to nobody, since the two are separate databases
// with no query between them.
func TestTheCookieOpensTheControlPlane(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t, true)
	c := signIn(t, s, "ops", passwordFrom(t, out))
	x.NotNil(c)

	// The control plane on a port, which is what a console reaches.
	conn := pdtest.Serve(t, s.Control.Grpc(ctx, cmd.Config{}))
	as := metadata.NewOutgoingContext(ctx,
		metadata.Pairs("cookie", c.Name+"="+c.Value))

	t.Run("it answers who the operator is", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewMeServiceClient(conn).Get(as, app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal("ops", v.GetAlias())

		// And what init bound them, which is what a console draws its menu from.
		x.Equal([]string{"/roster.*/*"}, v.GetMethods())
	})

	t.Run("and lets them administer the deployment", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewHolderServiceClient(conn).List(as,
			app.HolderListRequest_builder{}.Build())
		x.NoError(err)
	})

	t.Run("a cookie nobody minted opens nothing", func(t *testing.T) {
		x := require.New(t)

		bad := metadata.NewOutgoingContext(ctx,
			metadata.Pairs("cookie", c.Name+"=not-a-session"))

		_, err := app.NewMeServiceClient(conn).Get(bad, app.MeGetRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	// The same cookie on the data plane names somebody who is not there.
	t.Run("and it is not a credential for the data plane", func(t *testing.T) {
		x := require.New(t)

		other := pdtest.Serve(t, s.Grpc(ctx, cmd.Config{}))
		_, err := app.NewMeServiceClient(other).Get(as, app.MeGetRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})
}

// TestAConsoleManagesKeys is what the control plane's own listener is for.
//
// `ApiKeyService` is unregistered and closed on the data plane, because its
// generated `Get` answers with the verifier column to anybody the wall lets
// read a row. Here it is the point of the port — which is why that port is an
// address a console can reach and nothing else can, and why nothing in this
// process can enforce that.
func TestAConsoleManagesKeys(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t, true)
	c := signIn(t, s, "ops", passwordFrom(t, out))
	x.NotNil(c)

	conn := pdtest.Serve(t, s.GrpcControl(ctx, cmd.Config{}))
	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value))

	// Nothing on the data plane's port answers about keys, which is what the
	// second listener exists to change.
	t.Run("the data plane still says nothing about them", func(t *testing.T) {
		x := require.New(t)

		other := pdtest.Serve(t, s.Grpc(ctx, cmd.Config{}))
		_, err := app.NewApiKeyServiceClient(other).List(ctx,
			app.ApiKeyListRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.Unimplemented, status.Code(err))
	})

	t.Run("an operator lists what exists", func(t *testing.T) {
		x := require.New(t)

		// A key to find, minted the way the CLI does.
		who, err := cmd.ServiceOf(ctx, s.Control, "custody")
		x.NoError(err)

		_, sum, err := keys.Mint(keys.PrefixDeployment)
		x.NoError(err)

		_, err = s.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
			Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
			Alias:   "production",
			Secret:  sum,
			Methods: []string{"/roster.VouchService/Verify"},
		}.Build())
		x.NoError(err)

		v, err := app.NewApiKeyServiceClient(conn).List(as,
			app.ApiKeyListRequest_builder{}.Build())
		x.NoError(err)
		x.NotEmpty(v.GetItems())
	})
}

// TestAnOperatorAdministersCustomers is the third listener doing what neither
// of the others can.
//
// The data plane's port is walled and an operator has no tenant there, so it
// shows them nothing. The control plane's port has their own rows and not a
// customer's. This is the one that reaches a customer from outside every
// tenant.
func TestAnOperatorAdministersCustomers(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t, true)
	c := signIn(t, s, "ops", passwordFrom(t, out))
	x.NotNil(c)

	g, err := s.GrpcAdmin(ctx, cmd.Config{})
	x.NoError(err)
	conn := pdtest.Serve(t, g)

	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value))

	// The whole of setting a customer up, which is what `roster init` does for
	// the first one. The last two are what fails when `core` reads the wrong
	// database: `Granted` looks for the operator's bindings in the data plane
	// and finds none.
	tn, err := app.NewTenantServiceClient(conn).Add(as,
		app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)

	h, err := app.NewHolderServiceClient(conn).Add(as, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:  "admin",
	}.Build())
	x.NoError(err)

	r, err := app.NewRoleServiceClient(conn).Add(as, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:   "everything",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err, "the operator could not give the new customer's admin anything")

	_, err = app.NewBindingServiceClient(conn).Add(as, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	// And what neither port serves, however private this one is: its generated
	// `Get` answers with the verifier column.
	t.Run("and still not a password hash", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewCredentialServiceClient(conn).List(as,
			app.CredentialListRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.Unimplemented, status.Code(err))
	})

	t.Run("nobody without a session gets in", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewTenantServiceClient(conn).Add(ctx,
			app.TenantAddRequest_builder{Alias: "nope"}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})
}

// TestTheTwoTrailsAreJoined is the property the whole arrangement rests on.
//
// The two planes are separate databases with no query between them, so an
// operator's write leaves two rows: the decision, in the control plane where
// the operator resolves, and what changed, in the data plane where the customer
// does. Neither is complete alone -- the data plane's names an actor that
// resolves in neither database -- and what joins them is the trace.
//
// It must not depend on `otel:` being configured. Observability is a thing a
// deployment may turn off; an audit trail that comes apart when it does is not
// an audit trail. Confirmed by running it: with no otel configured, every row
// used to come back with an empty trace.
func TestTheTwoTrailsAreJoined(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t, true)
	c := signIn(t, s, "ops", passwordFrom(t, out))
	x.NotNil(c)

	g, err := s.GrpcAdmin(ctx, cmd.Config{})
	x.NoError(err)
	conn := pdtest.Serve(t, g)
	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value))

	before, err := s.Control.Ent.Audit.Query().Count(ctx)
	x.NoError(err)

	tn, err := app.NewTenantServiceClient(conn).Add(as,
		app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)
	newco := mustId(t, tn.GetId())

	// The data plane's row: what changed, and an actor that resolves nowhere
	// here.
	ds, err := s.Ent.Audit.Query().All(ctx)
	x.NoError(err)

	var data *ent.Audit
	for _, v := range ds {
		if pdid.Id(v.ObjectID) == newco {
			data = v
		}
	}
	x.NotNil(data, "the data plane recorded nothing about the customer")
	x.NotEmpty(data.TraceID, "no trace, so nothing to join it to")

	// The control plane's row: who decided, written before the attempt.
	cs, err := s.Control.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	x.Equal(before+1, len(cs), "the decision was not recorded")

	var intent *ent.Audit
	for _, v := range cs {
		if string(v.TraceID) == string(data.TraceID) {
			intent = v
		}
	}
	x.NotNil(intent, "the two trails carry different traces and cannot be joined")

	// And the join is worth making: the actor the data plane could not resolve
	// is a row here.
	x.Equal("/roster.TenantService/Add", intent.Action)
	x.Equal(data.ActorID, intent.ActorID)

	who, err := s.Control.Ent.Holder.Get(ctx, intent.ActorID)
	x.NoError(err, "the operator does not resolve in the plane that recorded them")
	x.Equal("ops", who.Alias)
}

// TestNoVerifierReachesTheTrail is where `(payday.field).secret` was found to
// be half true.
//
// `CredentialService` and `ApiKeyService` are unregistered and closed so that
// nothing answers with a verifier. The trail went around all of it: the
// recorder reads the bare server on purpose -- a row is recorded as it was
// written, not as somebody was allowed to see it -- so an argon2id hash sat in
// `Audit.value`, in the one table nothing erases, readable by anybody who may
// read the trail.
//
// The declaration on the field is what the recorder reads. The layer only
// covers the way out, and `vouch` and `keys` read these columns through an
// unwalled server on purpose, so the layer could never have covered this.
func TestNoVerifierReachesTheTrail(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t, true)
	x.NotEmpty(passwordFrom(t, out))

	// The operator's password, hashed by the RPC that hashes it.
	cred, err := s.Control.Ent.Credential.Query().Only(ctx)
	x.NoError(err)
	x.NotEmpty(cred.Secret, "nothing was stored, so this proves nothing")

	// And a key, which is the other verifier.
	who, err := cmd.ServiceOf(ctx, s.Control, "custody")
	x.NoError(err)

	_, sum, err := keys.Mint(keys.PrefixDeployment)
	x.NoError(err)

	_, err = s.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:   "production",
		Secret:  sum,
		Methods: []string{"/roster.VouchService/Verify"},
	}.Build())
	x.NoError(err)

	vs, err := s.Control.Ent.Audit.Query().All(ctx)
	x.NoError(err)

	values := 0
	for _, v := range vs {
		x.NotContains(string(v.Value), string(cred.Secret), "the trail holds a password hash")
		x.NotContains(string(v.Value), string(sum), "the trail holds a key hash")
		if len(v.Value) > 0 {
			values++
		}
	}
	x.NotZero(values, "no row carried a value, so the checks above never looked at one")

	// The columns really are still there to be read, through the server that
	// exists to read them. Clearing the trail must not have cleared the row.
	x.NotEmpty(cred.Secret)
}

// TestAConsoleReachesTheAdminPortOverHttp is what a browser can actually do.
//
// A browser cannot speak gRPC, so a port with no transcoder is a port a console
// cannot reach. Until this, the only one was in front of the **data plane**,
// where an operator's session names nobody -- so a console could sign in and
// then had nothing to call.
func TestAConsoleReachesTheAdminPortOverHttp(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t, true)

	g, err := s.GrpcAdmin(ctx, cmd.Config{})
	x.NoError(err)

	h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
	x.NoError(err)

	v := cmd.Login(s.Control)
	h.Handle("POST /session", s.Sessions.Serve(v))

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	x.NoError(err)
	c := &http.Client{Jar: jar}

	post := func(path, body string) (int, string) {
		t.Helper()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, strings.NewReader(body))
		x.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")

		res, err := c.Do(req)
		x.NoError(err)
		defer res.Body.Close()

		b, _ := io.ReadAll(res.Body)

		return res.StatusCode, string(b)
	}

	// Anonymous first, so what changes is the sign-in.
	code, _ := post("/roster.TenantService/Add", `{"alias":"newco"}`)
	x.Equal(http.StatusUnauthorized, code)

	code, body := post("/session",
		`{"alias":"ops","password":"`+passwordFrom(t, out)+`"}`)
	x.Equal(http.StatusNoContent, code, body)

	// And now the thing a console is for, over JSON, with the cookie the
	// browser is carrying.
	code, body = post("/roster.TenantService/Add", `{"alias":"newco"}`)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, "newco")

	// The customer really is there.
	n, err := s.Ent.Tenant.Query().Count(ctx)
	x.NoError(err)
	x.Equal(2, n, "acme from init, and the one the console made")
}
