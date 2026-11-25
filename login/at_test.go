package login_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
)

// moved is a clock a test moves, for [login.Config.Now].
type moved struct {
	mu sync.Mutex
	at time.Time
}

func (c *moved) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.at
}

func (c *moved) pass(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// brand is what the screen of a login challenge is told to call the tenant.
func (d *deployment) brand(t *testing.T, challenge string) string {
	t.Helper()
	x := require.New(t)

	res, err := d.browser(t).Get(d.app.URL + "/flow?login_challenge=" + challenge)
	x.NoError(err)
	defer res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode)

	var v struct {
		Brand string `json:"brand"`
	}
	x.NoError(json.NewDecoder(res.Body).Decode(&v))

	return v.Brand
}

// TestATenantsNewAliasSignsItsPeopleInOnceAMinuteHasPassed: what a name
// resolved to is remembered a minute, and then read again.
//
// It was remembered for the life of the process -- which tenant, and that
// tenant's alias and name with it. A password sign-in is addressed by the
// alias, so a tenant whose alias changed had every right password of its
// people answered 401 until the app was restarted, and its screen went on
// being drawn under the old name. A minute, which is what the account app
// keeps a name for: within it the app may go on with what it resolved, and
// after it the tenant is read afresh.
func TestATenantsNewAliasSignsItsPeopleInOnceAMinuteHasPassed(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	clock := &moved{at: time.Now()}
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Now = clock.now })

	// A flow first, so that the name is one this app has resolved.
	d.hydra.raise("c1", "contoso-web")
	_, code := d.signIn(t, d.browser(t), "c1", "erin", password)
	x.Equal(http.StatusOK, code)
	x.Equal("contoso", d.brand(t, "c1"))

	// At the box, since the wire's `Patch` is closed.
	tn, err := d.s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
		Ref:    rstr.TenantRef_builder{Alias: proto.String("contoso")}.Build(),
		Select: rstr.TenantSelect_builder{DateUpdated: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Tenant().Patch(ctx, rstr.TenantPatchRequest_builder{
		Ref:         rstr.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:       proto.String("contoso-group"),
		Name:        proto.String("Contoso Group"),
		DateUpdated: tn.GetDateUpdated(),
	}.Build())
	x.NoError(err)

	// Remembered still, which is what makes it a minute and not a read of the
	// tenant at every hop of every flow.
	clock.pass(30 * time.Second)
	d.hydra.raise("c2", "contoso-web")
	x.Equal("contoso", d.brand(t, "c2"), "half a minute on, the tenant had been read again")

	clock.pass(31 * time.Second)
	d.hydra.raise("c3", "contoso-web")
	_, code = d.signIn(t, d.browser(t), "c3", "erin", password)
	x.Equal(http.StatusOK, code, "a minute after its tenant's alias changed, the right password was still refused")
	x.Equal("Contoso Group", d.brand(t, "c3"), "and the screen was still drawn under the old name")

	subject, _ := d.hydra.told()
	x.Equal(d.who["contoso"].String(), subject)
}

// TestANameThatMovedSignsInTheTenantItMovedTo: a name is its tenant's for as
// long as its `Host` row says, and a minute more at most.
//
// Which tenant a name is, was asked once and kept until restart. A name that
// left one tenant for another went on being the first one's here: the calls
// went out narrowed by the name, which roster answers as the tenant that has
// it now, and named the tenant that had it before -- so the new tenant's
// people, with the right password, were answered 401. After a minute the name
// is asked about again and is whose it is.
func TestANameThatMovedSignsInTheTenantItMovedTo(t *testing.T) {
	const name = "contoso.app.test"

	x := require.New(t)
	ctx := t.Context()
	clock := &moved{at: time.Now()}
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Now = clock.now })

	d.hydra.raise("c1", "contoso-web")
	_, code := d.signIn(t, d.browser(t), "c1", "erin", password)
	x.Equal(http.StatusOK, code)
	subject, _ := d.hydra.told()
	x.Equal(d.who["contoso"].String(), subject)

	// The name leaves contoso and is fabrikam's.
	was, err := d.s.Ungated.Host().Get(ctx, rstr.HostGetRequest_builder{
		Ref: rstr.HostRef_builder{Name: proto.String(name)}.Build(),
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Host().Erase(ctx, rstr.HostRef_builder{Id: was.GetId()}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Host().Add(ctx, rstr.HostAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Alias: proto.String("fabrikam")}.Build(), Name: name,
	}.Build())
	x.NoError(err)

	clock.pass(time.Minute + time.Second)

	// The same client and the same redirect, so the same name: fabrikam's now,
	// and it is fabrikam's Erin whose password is checked.
	d.hydra.raise("c2", "contoso-web")
	_, code = d.signIn(t, d.browser(t), "c2", "erin", password)
	x.Equal(http.StatusOK, code, "a minute after a name moved, its new tenant's people were still refused")
	subject, _ = d.hydra.told()
	x.Equal(d.who["fabrikam"].String(), subject, "the name was still the tenant's that had it before")
}

// commandLine is a flow a command line raised through somebody's browser: its
// authorization request names `redirect`, a loopback address on whatever port
// it opened, and its client registered `registered`.
func (h *hydra) commandLine(challenge, client, redirect string, registered ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.client[challenge] = client
	h.sendsTo[challenge] = redirect
	h.registers[client] = registered
}

// TestACommandLinesLoopbackRedirectIsTheTenantItsClientRegistered: a loopback
// redirect says nothing about the tenant, so the client's registration does.
//
// A command line signing somebody in through their browser redirects to the
// loopback interface on whatever port it could open (RFC 8252 §7.3). The host
// of that redirect was read as the tenant's, and `127.0.0.1` is nobody's: the
// right password was answered 502, *this login is not working*. Registered
// beside the tenant's own host, the loopback address is now read as a request
// that named no redirect, and the registration's one other host is the
// tenant.
func TestACommandLinesLoopbackRedirectIsTheTenantItsClientRegistered(t *testing.T) {
	for name, loopback := range map[string]string{
		"IPv4": "http://127.0.0.1:53117/callback",
		"IPv6": "http://[::1]:53117/callback",
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			d := serve(t)

			d.hydra.commandLine("c1", "contoso-cli", loopback,
				"http://127.0.0.1/callback", "http://[::1]/callback", "https://contoso.app.test/callback")
			_, code := d.signIn(t, d.browser(t), "c1", "erin", password)
			x.Equal(http.StatusOK, code, "a loopback redirect was read as a tenant's host")

			subject, _ := d.hydra.told()
			x.Equal(d.who["contoso"].String(), subject)
		})
	}
}

// TestLocalhostIsANameATenantAnswersAt: `localhost` is not a loopback address
// here, and is read as any other name is.
//
// RFC 8252 §8.3 says a native app does not use it, and a name is something a
// tenant can claim with a `Host` row; so a redirect at `localhost` is about
// the tenant that claimed it, and not about the client's registration.
func TestLocalhostIsANameATenantAnswersAt(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	d := serve(t)

	_, err := d.s.Ungated.Host().Add(ctx, rstr.HostAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Alias: proto.String("fabrikam")}.Build(), Name: "localhost",
	}.Build())
	x.NoError(err)

	d.hydra.commandLine("c1", "contoso-cli", "http://localhost:53117/callback",
		"http://localhost/callback", "https://contoso.app.test/callback")
	_, code := d.signIn(t, d.browser(t), "c1", "erin", password)
	x.Equal(http.StatusOK, code)

	subject, _ := d.hydra.told()
	x.Equal(d.who["fabrikam"].String(), subject, "localhost was read as a loopback address and not as the name fabrikam claimed")
}

// TestAClientOfLoopbackAddressesAloneNamesNoTenant: with nothing registered but
// loopback addresses, no flow of its client is about anybody, and nobody is
// signed in.
//
// `login/at.go` says so in the refusal, and what to register beside them; the
// sentence itself is held by `TestALoopbackAddressIsNeverATenantsHost`.
func TestAClientOfLoopbackAddressesAloneNamesNoTenant(t *testing.T) {
	x := require.New(t)
	d := serve(t)

	d.hydra.commandLine("c1", "contoso-cli", "http://127.0.0.1:53117/callback",
		"http://127.0.0.1/callback", "http://[::1]/callback")
	_, code := d.signIn(t, d.browser(t), "c1", "erin", password)
	x.Equal(http.StatusBadGateway, code)

	accepted, _ := d.hydra.said()
	x.Zero(accepted, "a flow that named no tenant signed somebody in")
}

// TestALoopbackAddressBesideTwoTenantsHostsLeavesTwoAnswers: leaving loopback
// addresses out of a registration does not make two tenants' hosts one.
func TestALoopbackAddressBesideTwoTenantsHostsLeavesTwoAnswers(t *testing.T) {
	x := require.New(t)
	d := serve(t)

	d.hydra.commandLine("c1", "contoso-cli", "http://127.0.0.1:53117/callback",
		"http://127.0.0.1/callback", "https://contoso.app.test/callback", "https://fabrikam.app.test/callback")
	_, code := d.signIn(t, d.browser(t), "c1", "erin", password)
	x.Equal(http.StatusBadGateway, code)

	accepted, _ := d.hydra.said()
	x.Zero(accepted, "a flow with two answers was signed in as one of them")
}

// TestADeviceWhoseClientRegisteredALoopbackAddressToo: a client that does the
// device grant and a command line's loopback redirect beside it is still the
// tenant's whose host it registered.
//
// A device flow names no redirect, so its tenant is read off the client's
// registration -- which, with a loopback address beside the tenant's host,
// was two hosts and refused, so registering a command line broke the device
// grant of the same client.
func TestADeviceWhoseClientRegisteredALoopbackAddressToo(t *testing.T) {
	x := require.New(t)
	d := serve(t)

	d.hydra.printedFor("c1", "contoso-cli")
	d.hydra.registers["contoso-cli"] = []string{"http://127.0.0.1/callback", "https://contoso.app.test/callback"}

	_, code := d.signIn(t, d.browser(t), "c1", "erin", password)
	x.Equal(http.StatusOK, code, "a device flow's client registered a loopback address and resolved no tenant")

	subject, _ := d.hydra.told()
	x.Equal(d.who["contoso"].String(), subject)
}
