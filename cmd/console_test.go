package cmd_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
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

// signIn asks for a session the way a console does, and answers with the
// cookie.
//
// Through `AuthService` on the control plane's own server, which is where it is
// registered and the only place a sign-in is served at all. The cookie arrives
// as `set-cookie` **response metadata**, which is what reaches a browser as a
// header through `web.Transcode` -- so reading it here is reading what a page
// gets, one transcoding earlier.
func signIn(t *testing.T, s *cmd.Server, alias, password string) *http.Cookie {
	t.Helper()
	x := require.New(t)

	g, err := s.GrpcControl(t.Context(), cmd.Config{})
	x.NoError(err)

	var h metadata.MD
	_, err = app.NewAuthServiceClient(pdtest.Serve(t, g)).SignIn(t.Context(),
		app.AuthSignInRequest_builder{Alias: alias, Password: password}.Build(),
		grpc.Header(&h))
	if err != nil {
		return nil
	}

	res := http.Response{Header: http.Header{"Set-Cookie": h.Get("set-cookie")}}
	for _, c := range res.Cookies() {
		if c.Value != "" {
			return c
		}
	}

	return nil
}

// mustURL is a cookie jar's idea of where it is.
func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()

	u, err := url.Parse(s)
	require.NoError(t, err)

	return u
}

// TestAnOperatorSignsIn is the console's front door, end to end from what
// `roster init` printed.
//
// It is the seam payday left and could not fill: `auth` reads a credential and
// does not issue one. A browser has nowhere safe to keep a secret, so what it
// gets is an opaque cookie naming a session this server keeps -- and filling
// the seam is `AuthService`, an RPC like everything else here, whose cookie
// travels as the response metadata `web.Transcode` turns into a header. It read
// as "issuing is HTTP" for a while and there was a route beside the service
// saying so; there is one door now.
func TestAnOperatorSignsIn(t *testing.T) {
	x := require.New(t)

	s, out := inited(t)
	x.NotNil(s.Sessions, "a deployment with a control plane has a console door")

	secret := passwordFrom(t, out)

	t.Run("with what init printed", func(t *testing.T) {
		x := require.New(t)

		c := signIn(t, s, "admin", secret)
		x.NotNil(c, "the password init printed does not sign in")
		x.True(c.HttpOnly, "a cookie script can read is one script can send elsewhere")
		x.Equal(http.SameSiteLaxMode, c.SameSite)
		x.NotEmpty(c.Value)
	})

	t.Run("and not with a wrong password", func(t *testing.T) {
		x := require.New(t)
		x.Nil(signIn(t, s, "admin", secret+"x"))
	})

	t.Run("nor as somebody who is not there", func(t *testing.T) {
		x := require.New(t)
		x.Nil(signIn(t, s, "nobody", secret))
	})

	// A customer's person is not an operator, whatever their password is:
	// what they sign in to is a product app -- roster's own console is the
	// deployment's, and a customer's people are the customer's. Written out,
	// because `init` no longer seeds one (D56) and the operator is `admin` too
	// now, so a name alone would prove nothing.
	t.Run("nor as a data plane holder", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()

		tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
		x.NoError(err)
		alice, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(), Alias: "alice",
		}.Build())
		x.NoError(err)
		_, err = s.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
			Ref: app.HolderRef_builder{Id: alice.GetId()}.Build(), Secret: []byte(secret),
		}.Build())
		x.NoError(err)

		x.Nil(signIn(t, s, "alice", secret))
	})
}

// TestNoControlPlaneNoConsole -- a deployment that believes its callers has
// nobody to be, so the door is not there to knock on.
func TestNoControlPlaneNoConsole(t *testing.T) {
	x := require.New(t)

	// Built rather than `init`ed, because `init` refuses a deployment with no
	// control plane now -- and what this is about is the wiring, which is the
	// same however the rows got there.
	drv, dsn := pdtest.DB(t)

	s, err := cmd.Build(t.Context(), cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

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

	s, out := inited(t)
	c := signIn(t, s, "admin", passwordFrom(t, out))
	x.NotNil(c)

	// The control plane on a port, which is what a console reaches.
	g, err := s.Control.Grpc(ctx, cmd.Config{})
	require.NoError(t, err)

	conn := pdtest.Serve(t, g)
	as := metadata.NewOutgoingContext(ctx,
		metadata.Pairs("cookie", c.Name+"="+c.Value))

	t.Run("it answers who the operator is", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewMeServiceClient(conn).Get(as, app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal("admin", v.GetAlias())

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

		other := served(t, s)
		_, err := app.NewMeServiceClient(other).Get(as, app.MeGetRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
	})
}

// TestAConsoleManagesKeys is what the control plane's own listener is for.
//
// `ApiKeyService`'s management -- its reads and raw writes -- is closed on the
// data plane, because its generated `Get` answers with the verifier column to
// anybody the wall lets read a row. (Its one open verb there is `Issue`, the
// mint, which answers a token once and never a stored hash.) Here management is
// the point of the port — which is why that port is an address a console can
// reach and nothing else can, and why nothing in this process can enforce that.
func TestAConsoleManagesKeys(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)
	c := signIn(t, s, "admin", passwordFrom(t, out))
	x.NotNil(c)

	conn := servedControl(t, s)
	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value))

	// Nothing on the data plane's port manages keys, which is what the second
	// listener exists to change. `ApiKeyService` is registered there now for
	// its `Issue` overlay, so a management read is refused at the door -- an
	// uncredentialed reader is turned away before dispatch -- rather than at a
	// missing handler. Either way the data plane says nothing about them.
	t.Run("the data plane still says nothing about them", func(t *testing.T) {
		x := require.New(t)

		other := served(t, s)
		_, err := app.NewApiKeyServiceClient(other).List(ctx,
			app.ApiKeyListRequest_builder{}.Build())
		x.Error(err)
		x.Equal(codes.Unauthenticated, status.Code(err))
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

	s, out := inited(t)
	c := signIn(t, s, "admin", passwordFrom(t, out))
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

	s, out := inited(t)
	c := signIn(t, s, "admin", passwordFrom(t, out))
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
		if pdid.Id(v.ObjectId) == newco {
			data = v
		}
	}
	x.NotNil(data, "the data plane recorded nothing about the customer")
	x.NotEmpty(data.TraceId, "no trace, so nothing to join it to")

	// The control plane's row: who decided, written before the attempt.
	cs, err := s.Control.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	x.Equal(before+1, len(cs), "the decision was not recorded")

	var intent *ent.Audit
	for _, v := range cs {
		if string(v.TraceId) == string(data.TraceId) {
			intent = v
		}
	}
	x.NotNil(intent, "the two trails carry different traces and cannot be joined")

	// And the join is worth making: the actor the data plane could not resolve
	// is a row here.
	x.Equal("/roster.TenantService/Add", intent.Action)
	x.Equal(data.ActorId, intent.ActorId)

	who, err := s.Control.Ent.Holder.Get(ctx, intent.ActorId)
	x.NoError(err, "the operator does not resolve in the plane that recorded them")
	x.Equal("admin", who.Alias)
}

// TestNoVerifierReachesTheTrail is where `(payday.field).secret` was found to
// be half true.
//
// `CredentialService`'s and `ApiKeyService`'s generated reads are shut a method
// at a time so that nothing answers with a verifier. The trail went around all of it: the
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

	s, out := inited(t)
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

	s, out := inited(t)

	g, err := s.GrpcAdmin(ctx, cmd.Config{})
	x.NoError(err)

	h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
	x.NoError(err)

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

	// Signed in on the control plane's listener, which is the only one that
	// serves a sign-in -- and then carried here. That is the console's own
	// shape: one door to knock on, and the port it operates on is another.
	jar.SetCookies(mustURL(t, srv.URL),
		[]*http.Cookie{signIn(t, s, "admin", passwordFrom(t, out))})

	// And now the thing a console is for, over JSON, with the cookie the
	// browser is carrying.
	code, body := post("/roster.TenantService/Add", `{"alias":"newco"}`)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, "newco")

	// The customer really is there.
	n, err := s.Ent.Tenant.Query().Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "the one the console made, and `init` leaves no customer beside it")
}

// TestAConsoleReachesTheControlPlaneOverHttp is the **first** console's path.
//
// What an operator manages before any customer exists is the deployment itself:
// who else may sign in, which services call it, what each of their keys may do.
// All of that is control plane, none of it is on the other two ports, and a
// browser needs a transcoder in front of it like anything else.
func TestAConsoleReachesTheControlPlaneOverHttp(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)

	wg, err := s.GrpcControl(ctx, cmd.Config{})
	require.NoError(t, err)

	h, err := web.New(config.HttpConfig{AllowWeb: true}, wg)
	x.NoError(err)

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

	code, _ := post("/roster.MeService/Get", `{}`)
	x.Equal(http.StatusUnauthorized, code, "anonymous reached the control plane")

	// The sign-in is an RPC on this same mux, like everything else the page
	// calls -- and the cookie comes back as an ordinary `set-cookie` header,
	// which is the whole of what `web.Transcode` does with response metadata.
	code, body := post("/roster.AuthService/SignIn",
		`{"alias":"admin","password":"`+passwordFrom(t, out)+`"}`)
	x.Equal(http.StatusOK, code, body)
	x.NotEmpty(jar.Cookies(mustURL(t, srv.URL)), "signing in set no cookie")

	// The three screens the first console is, in the order it would draw them.
	t.Run("who am I", func(t *testing.T) {
		x := require.New(t)

		code, body := post("/roster.MeService/Get", `{}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"admin"`)
	})

	t.Run("who else runs this deployment", func(t *testing.T) {
		x := require.New(t)

		code, body := post("/roster.HolderService/List", `{}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"admin"`)
	})

	t.Run("and what may call it", func(t *testing.T) {
		x := require.New(t)

		// A service and a key, the way `roster key add` makes them.
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

		code, body := post("/roster.ApiKeyService/List", `{}`)
		x.Equal(http.StatusOK, code, body)
		x.Contains(body, `"production"`)

		// And the verifier is not in the answer, which is why this service is
		// on this port and no other.
		x.NotContains(body, string(sum))
	})

	// The sign-out a browser actually sends. It was `DELETE /session`, a route
	// beside the RPC that already did the same thing; it is the RPC now, over
	// this same transcoder.
	//
	// The claim is immediacy, and it is made the hard way: the cookie the jar
	// held is sent again by hand afterwards, so what refuses it is the row
	// being gone rather than the browser having dropped a header.
	t.Run("and signing out ends it, immediately", func(t *testing.T) {
		x := require.New(t)

		held := jar.Cookies(mustURL(t, srv.URL))
		x.NotEmpty(held, "nothing was holding a session to end")

		code, body := post("/roster.AuthService/SignOut", `{}`)
		x.Equal(http.StatusOK, code, body)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			srv.URL+"/roster.MeService/Get", strings.NewReader(`{}`))
		x.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		for _, v := range held {
			req.AddCookie(v)
		}

		res, err := http.DefaultClient.Do(req)
		x.NoError(err)
		defer res.Body.Close()

		x.Equal(http.StatusUnauthorized, res.StatusCode, "a signed-out cookie was served")
	})
}

// TestTheDataPlanesHttpHasNoSignIn is the trap, removed.
//
// It used to be `TestTheDataPlanesHttpSignsInNobody`, and what it asserted was
// the trap working as documented: `POST /session` was mounted on every listener
// that had HTTP, so the customer-facing port **answered** an operator's password
// with 204 and a cookie every walled call then named nobody with. A success
// that opens nothing is the one failure a caller cannot tell from a bug, and it
// cost a paragraph in `operating.md` to warn about.
//
// The route is gone (`AuthService`, `server/console`), and a service is
// registered per listener -- so the data plane has no sign-in to answer with at
// all. Which is what the port should always have said.
func TestTheDataPlanesHttpHasNoSignIn(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)

	g, err := s.Grpc(ctx, cmd.Config{})
	x.NoError(err)

	// Exactly what `serveHttp` builds, on the walled server's transcoder.
	h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
	x.NoError(err)

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

	// The route that was here.
	code, _ := post("/session", `{"alias":"admin","password":"`+passwordFrom(t, out)+`"}`)
	x.Equal(http.StatusNotFound, code, "the data plane still mints a session")

	// And the service that replaced it, which is registered on the control
	// listener and not on this one.
	code, _ = post("/roster.AuthService/SignIn",
		`{"alias":"admin","password":"`+passwordFrom(t, out)+`"}`)
	x.Equal(http.StatusNotFound, code, "the data plane serves a sign-in")

	here, err := url.Parse(srv.URL)
	x.NoError(err)
	x.Empty(jar.Cookies(here), "something on this listener answered with a cookie")

	code, body := post("/roster.MeService/Get", `{}`)
	x.Equal(http.StatusUnauthorized, code,
		"the data plane answered somebody who never signed in: %s", body)
}
