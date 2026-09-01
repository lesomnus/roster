package sso_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/examples/sso"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestAnAccountIsInertUntilAdmitted is the whole of the pattern the user asked
// about, end to end and over a real browser session: somebody signs in with
// Sso, may do nothing, an administrator admits them, and now they may -- with
// nothing about their sign-in having changed, and roster unmodified.
func TestAnAccountIsInertUntilAdmitted(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	// `Enrolling` is the JIT front door: a stranger the provider vouches for
	// gets a Holder and an Identity, and **no role** -- which is the pending
	// state, arrived at for free.
	d := serve(t, sso.Enrolling, map[string]string{"127.0.0.1": "contoso"})

	// What an ordinary account may do, and the group that carries it. `Admit`
	// puts somebody in this group; being in it is holding the role.
	memberMethods := []string{
		rstr.MeService_IssueKey_FullMethodName,
		rstr.HolderService_List_FullMethodName,
	}
	group := d.defineMembers(t, "member", memberMethods)

	// An administrator who may admit. It holds the membership writes **and**
	// the member methods themselves -- because admitting hands out that role,
	// and roster refuses handing out what the caller does not hold.
	gate := sso.NewAdmissions(d.adminClient(t, "admissions", append([]string{
		rstr.GroupMembershipService_Add_FullMethodName,
		rstr.GroupMembershipService_Erase_FullMethodName,
		rstr.GroupMembershipService_Get_FullMethodName,
	}, memberMethods...)), group)

	// The stranger signs in. One `GET /login` walks the whole flow and leaves a
	// session cookie in the jar, which the calls below reuse.
	d.idp.subject = "9000"
	d.idp.claims = map[string]any{"email": "newcomer@contoso.example", "email_verified": true}

	jar, err := cookiejar.New(nil)
	x.NoError(err)
	c := &http.Client{Jar: jar}

	res, err := c.Get(d.app.URL + "/login")
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode, "the stranger was not signed in")

	person := d.holderOf(t, "example", "9000")

	t.Run("signed in, and may do nothing", func(t *testing.T) {
		x := require.New(t)

		// Their own record reads -- `MeService.Get` is waived -- and it says in
		// its own words that they hold nothing.
		x.Empty(d.mayCall(t, c), "a role-less account was told it may call something")

		// And a gated act -- minting a key, a write that needs a role -- is
		// refused through the person's own session.
		x.Equal(http.StatusForbidden, d.mint(t, c, "one"),
			"an unadmitted account did something")

		ok, err := gate.Admitted(ctx, person)
		x.NoError(err)
		x.False(ok, "nobody admitted them, and Admitted said they were in")
	})

	t.Run("admitted, and may do what a member may", func(t *testing.T) {
		x := require.New(t)

		x.NoError(gate.Admit(ctx, person))

		ok, err := gate.Admitted(ctx, person)
		x.NoError(err)
		x.True(ok)

		// The same act, the same session, now allowed. Nothing about how they
		// signed in changed -- only what they hold.
		x.Equal(http.StatusOK, d.mint(t, c, "two"),
			"an admitted member still could not act")
		x.NotEmpty(d.mayCall(t, c), "the admitted member was told they hold nothing")
	})

	t.Run("suspended, inert again -- but still signed in", func(t *testing.T) {
		x := require.New(t)

		x.NoError(gate.Suspend(ctx, person))

		x.Equal(http.StatusForbidden, d.mint(t, c, "three"),
			"suspension left the role in place")

		// The sign-in itself is untouched: `/me` still reads. Suspend shut the
		// rooms, not the door -- which is the line between this and Disable.
		got, err := c.Get(d.app.URL + "/me")
		x.NoError(err)
		got.Body.Close()
		x.Equal(http.StatusOK, got.StatusCode, "suspend stopped the sign-in, which is Disable's job")
	})
}

// TestNobodyAdmitsIntoMoreThanTheyHold is the escalation guard the whole design
// rests on, felt from outside roster: adding somebody to a group that carries a
// role is handing out that role, so an administrator who does not hold it may
// not admit into it. D40, and it is why "who may approve" needs no new rule.
func TestNobodyAdmitsIntoMoreThanTheyHold(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	d := serve(t, sso.Enrolling, map[string]string{"127.0.0.1": "contoso"})
	memberMethods := []string{
		rstr.MeService_IssueKey_FullMethodName,
		rstr.HolderService_List_FullMethodName,
	}
	group := d.defineMembers(t, "member", memberMethods)

	// This administrator may write the membership, and holds none of what the
	// group grants. So admitting would hand out a role wider than their own.
	weak := sso.NewAdmissions(d.adminClient(t, "weak", []string{
		rstr.GroupMembershipService_Add_FullMethodName,
	}), group)

	victim, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  "victim",
	}.Build())
	x.NoError(err)

	id, err := pdid.From(victim.GetId())
	x.NoError(err)

	err = weak.Admit(ctx, id)
	x.Equal(codes.PermissionDenied, status.Code(err),
		"a group membership handed out a role its writer did not hold")
}

// defineMembers writes, once, what an ordinary account is: a role of the given
// methods, a group, and a binding of the one to the other across the tenant.
//
// Through `ungated`, because writing a role and binding it is the wide,
// deployment-shaped act the front door must never hold -- this is the
// deployment doing its own setup, not the login app.
func (d *deployment) defineMembers(t *testing.T, alias string, methods []string) pdid.Id {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	role, err := d.ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant:  rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:   alias,
		Methods: methods,
	}.Build())
	x.NoError(err)

	group, err := d.ungated.Group().Add(ctx, rstr.GroupAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  alias + "s",
	}.Build())
	x.NoError(err)

	// No site: bound across the tenant, so being in the group is holding the
	// role everywhere in it.
	_, err = d.ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:  rstr.RoleRef_builder{Id: role.GetId()}.Build(),
		Group: rstr.GroupRef_builder{Id: group.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	id, err := pdid.From(group.GetId())
	x.NoError(err)

	return id
}

// adminClient seeds a holder that holds `methods`, and dials roster as them.
//
// A real administrator, over the wire, so the gate and the escalation rule run
// -- which is the whole point of the negative test. Their key is an
// attenuation to `/roster.*/*`, capped at what the holder holds, exactly as the
// front door's is.
func (d *deployment) adminClient(t *testing.T, alias string, methods []string) rstr.Client {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	who, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	x.NoError(err)

	role, err := d.ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant:  rstr.TenantRef_builder{Id: d.tenant.Bytes()}.Build(),
		Alias:   alias + "-role",
		Methods: methods,
	}.Build())
	x.NoError(err)

	_, err = d.ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: rstr.HolderRef_builder{Id: who.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	token, sum, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)

	_, err = d.ungated.ApiKey().Add(ctx, rstr.ApiKeyAddRequest_builder{
		Holder:  rstr.HolderRef_builder{Id: who.GetId()}.Build(),
		Alias:   alias + "-key",
		Secret:  sum,
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)

	g, err := d.server.Grpc(ctx, cmd.Config{})
	x.NoError(err)

	return rstr.NewClient(serveRoster(t, g, auth.BearerProvider(token)))
}

// holderOf resolves the Holder an Sso subject was enrolled onto.
func (d *deployment) holderOf(t *testing.T, provider, subject string) pdid.Id {
	t.Helper()
	x := require.New(t)

	v, err := d.ungated.Identity().Get(t.Context(), rstr.IdentityGetRequest_builder{
		Ref: rstr.IdentityRef_builder{
			Subject: rstr.IdentityRefBySubject_builder{
				TenantId: d.tenant.Bytes(),
				Provider: proto.String(provider),
				Subject:  proto.String(subject),
			}.Build(),
		}.Build(),
		Select: rstr.IdentitySelect_builder{
			Holder: rstr.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	x.NoError(err)

	id, err := pdid.From(v.GetHolder().GetId())
	x.NoError(err)

	return id
}

// mayCall is what `GET /me` says this session holds.
func (d *deployment) mayCall(t *testing.T, c *http.Client) []string {
	t.Helper()
	x := require.New(t)

	got, err := c.Get(d.app.URL + "/me")
	x.NoError(err)
	defer got.Body.Close()
	x.Equal(http.StatusOK, got.StatusCode, "the person could not read their own record")

	var rec struct {
		MayCall []string `json:"may_call"`
	}
	x.NoError(json.NewDecoder(got.Body).Decode(&rec))

	return rec.MayCall
}

// mint is `POST /me/keys` through the session, and the status it answered.
//
// The key it asks for allows `HolderService.List`, which the member role holds
// -- so once admitted the escalation check passes, and before that the gate
// refuses `MeService.IssueKey` itself. A distinct alias each time, so a second
// mint is refused for want of a role and never for a name already taken.
func (d *deployment) mint(t *testing.T, c *http.Client, alias string) int {
	t.Helper()
	x := require.New(t)

	body := bytes.NewBufferString(fmt.Sprintf(
		`{"alias":%q,"methods":[%q]}`, alias, rstr.HolderService_List_FullMethodName))

	res, err := c.Post(d.app.URL+"/me/keys", "application/json", body)
	x.NoError(err)
	res.Body.Close()

	return res.StatusCode
}
