package cmd_test

import (
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/prove"
)

// TestAGeneralWriteDoesNotWriteANamePastItsProof: a name, a claim to one, and
// what a tenant says of its front door are written by verbs of their own, and
// their general writes are not served, whatever a deployment says of general
// writes.
//
// # What it was
//
// A name is a tenant's by a record published for it (`HostService/Add`), and
// the rule is on that verb. `Host.Patch` asked that the caller hold it across
// the tenant and that the name be spelled as it is kept; `Host.Apply` asked the
// first and not the second. Neither asked for a proof. So with
// `allow_general_writes` -- one line, turned on for some other entity's sake --
// a tenant's administrator renamed a row they had proved to a name nobody had,
// and `FrontService/WhoseHost` answered their tenant for it, the row still
// saying when it was proved.
//
// A claim's window was the same: stamped by `HostProof.Add` and refused from a
// request there, and set to a hundred years by `HostProof.Patch`. And a
// tenant's own settings: `Tenant.Update` holds `config.front_door` to an origin
// and a secret's reference to who may write one, and `Tenant.Patch` held it to
// nothing (#277).
//
// It is the shape a credential's general writes had (#275) and a key's (#276):
// the rule is on a verb, and the general write goes round it. Each of these has
// a verb for everything about it that may be changed -- `Update` -- so the
// general writes are shut by name.
func TestAGeneralWriteDoesNotWriteANamePastItsProof(t *testing.T) {
	x := require.New(t)
	published := map[string][]string{}
	b, ctx := build(t, zone(published))
	b.mayAnything(b.ContosoUser, b.Contoso)

	// contoso proves a name of its own, and begins a claim to another.
	const mine = "mine.contoso.example"
	c := claiming(t, b, b.ContosoUser, b.Contoso, mine)
	published[prove.Record+"."+mine] = []string{c.GetToken()}
	h, err := adding(t, b, b.ContosoUser, b.Contoso, mine)
	x.NoError(err)
	x.NotNil(h.GetDateProved())
	later := claiming(t, b, b.ContosoUser, b.Contoso, "later.contoso.example")

	// A deployment that turned general writes on.
	g, err := b.Grpc(ctx, cmd.Config{Server: cmd.ServerConfig{ServerConfig: config.ServerConfig{AllowGeneralWrites: true}}})
	x.NoError(err)
	conn := pdtest.Serve(t, g)
	wire := asOverTheWire(ctx, b.ContosoUser)

	notServed := func(what string, err error) {
		t.Helper()
		x.Equal(codes.Unimplemented, status.Code(err), "%s: %v", what, err)
		x.ErrorContains(err, "is not served")
	}

	hosts := app.NewHostServiceClient(conn)
	_, err = hosts.Patch(wire, app.HostPatchRequest_builder{
		Ref: app.HostRef_builder{Id: h.GetId()}.Build(), Name: z.Ptr("never-proved.victim.example"), DateUpdated: h.GetDateUpdated(),
	}.Build())
	notServed("Host.Patch", err)
	_, err = hosts.Apply(wire, app.HostApplyRequest_builder{Ref: app.HostRef_builder{Id: h.GetId()}.Build()}.Build())
	notServed("Host.Apply", err)

	_, err = front.New(b.Ungated).WhoseHost(ctx, app.FrontWhoseHostRequest_builder{Host: "never-proved.victim.example"}.Build())
	x.Equal(codes.NotFound, status.Code(err), "a name nobody proved answers a tenant: %v", err)

	proofs := app.NewHostProofServiceClient(conn)
	_, err = proofs.Patch(wire, app.HostProofPatchRequest_builder{
		Ref:         app.HostProofRef_builder{Id: later.GetId()}.Build(),
		DateExpires: timestamppb.New(time.Now().Add(100 * 365 * 24 * time.Hour)),
		DateUpdated: later.GetDateUpdated(),
	}.Build())
	notServed("HostProof.Patch", err)
	_, err = proofs.Apply(wire, app.HostProofApplyRequest_builder{Ref: app.HostProofRef_builder{Id: later.GetId()}.Build()}.Build())
	notServed("HostProof.Apply", err)

	tenants := app.NewTenantServiceClient(conn)
	ref := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	tn, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	_, err = tenants.Patch(wire, app.TenantPatchRequest_builder{
		Ref: ref, DateUpdated: tn.GetDateUpdated(),
		Config: app.TenantConfig_builder{FrontDoor: "https://elsewhere.example/path?x=1"}.Build(),
	}.Build())
	notServed("Tenant.Patch", err)
	_, err = tenants.Apply(wire, app.TenantApplyRequest_builder{Ref: ref}.Build())
	notServed("Tenant.Apply", err)

	t.Run("and the verb of its own is there, held to its rule", func(t *testing.T) {
		x := require.New(t)

		_, err := tenants.Update(wire, app.TenantUpdateRequest_builder{
			Ref: ref, DateUpdated: tn.GetDateUpdated(),
			Config: app.TenantConfig_builder{FrontDoor: "https://elsewhere.example/path?x=1"}.Build(),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

		_, err = hosts.Update(wire, app.HostUpdateRequest_builder{
			Ref: app.HostRef_builder{Id: h.GetId()}.Build(), Desc: z.Ptr("the shop"), DateUpdated: h.GetDateUpdated(),
		}.Build())
		x.NoError(err)
	})
}

// TestAFileDeclaresANameOfItsOwnTenant: a `Host` a file declares is its
// tenant's, or the file is refused.
//
// # What it was
//
// `resources apply` found a declared `Host` by its name, across the
// deployment, and never looked whose row it had found. A file that said
// `shop.example.com` is contoso's, where the name was fabrikam's, applied green
// -- *changed* -- with the name still fabrikam's; and stamped fabrikam's row as
// declared, which a row's own tenant can then not erase or edit: *this row is
// declared in resources.yaml and is written from there* (#277).
func TestAFileDeclaresANameOfItsOwnTenant(t *testing.T) {
	x := require.New(t)
	cdrv, cdsn := pdtest.DB(t)
	b, ctx := build(t, func(c *cmd.Config) {
		c.Control = cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}}
	})
	x.NoError(entmigrate.NewSchema(b.Control.Drv).Create(ctx))

	// fabrikam holds the name.
	_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(), Name: "shop.example.com",
	}.Build())
	x.NoError(err)

	rs, err := cmd.ReadResources(declare(t, `
resources:
  - kind: Host
    tenant: contoso
    name: shop.example.com
`))
	x.NoError(err)

	for _, dry := range []bool{true, false} {
		_, err = cmd.ApplyResources(ctx, b.Server, rs, dry)
		x.Error(err, "dry=%v: a file declared a name that is another tenant's, and it applied", dry)
		x.ErrorContains(err, "shop.example.com")
		x.ErrorContains(err, "another tenant")
	}

	got, err := b.Ungated.Host().Get(ctx, app.HostGetRequest_builder{
		Ref:    app.HostRef_builder{Name: proto.String("shop.example.com")}.Build(),
		Select: app.HostSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(b.Fabrikam.Bytes(), got.GetTenant().GetId())
	x.Empty(got.GetLabels()[cmd.Declared], "the row of the tenant that holds the name was marked as the file's")

	// And it is still its own tenant's to erase.
	theirs := b.holder(t, ctx, b.Fabrikam, "admin")
	_, err = b.Walled.Host().Erase(b.as(ctx, theirs, b.Fabrikam), app.HostRef_builder{Id: got.GetId()}.Build())
	x.NoError(err)
}

// TestANameTakenIsOnTheTrailOfWhoeverHeldIt: a name that changes hands by a
// newer proof is written down where the tenant that lost it reads.
//
// # What it was
//
// The latest proof takes a name, and the incumbent is not asked: that is
// decided, and three places say where it is recorded -- *the erase of the
// incumbent's row is on the trail*. It was not. The incumbent's row is erased
// beneath the servers, since no walled server can see another tenant's row, and
// what is written beneath the servers is on the trail only if something puts it
// there. A tenant whose name had gone found its `Host` row erased and nothing
// in its trail saying who, when or how (#277).
//
// And a row a file declares was taken the same way, which every other road
// refuses: the file is where it is written from.
func TestANameTakenIsOnTheTrailOfWhoeverHeldIt(t *testing.T) {
	const name = "shared.example.com"

	x := require.New(t)
	published := map[string][]string{}
	b, ctx := build(t, zone(published))
	b.mayAnything(b.ContosoUser, b.Contoso)
	theirs := b.holder(t, ctx, b.Fabrikam, "someone")
	b.mayAnything(theirs, b.Fabrikam)

	conn := pdtest.Serve(t, b.grpc(t))
	proofs := app.NewHostProofServiceClient(conn)
	hosts := app.NewHostServiceClient(conn)

	take := func(who, in pdid.Id, name string) (*app.Host, error) {
		wire := asOverTheWire(ctx, who)
		c, err := proofs.Add(wire, app.HostProofAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(), Name: name,
		}.Build())
		x.NoError(err)
		published[prove.Record+"."+name] = []string{c.GetToken()}

		return hosts.Add(wire, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(), Name: name,
		}.Build())
	}

	first, err := take(b.ContosoUser, b.Contoso, name)
	x.NoError(err)
	lost, err := pdid.From(first.GetId())
	x.NoError(err)

	_, err = take(theirs, b.Fabrikam, name)
	x.NoError(err)

	vs, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	released := 0
	for _, v := range vs {
		if pdid.Id(v.ObjectId) != lost || v.Action != cmd.Released {
			continue
		}
		released++
		x.Equal(b.Contoso, pdid.Id(v.TenantId), "the taking of contoso's name is not in contoso's trail")
		x.Equal(theirs, pdid.Id(v.ActorId), "and does not say who took it")
	}
	x.Equal(1, released, "a name was taken and the tenant that held it has nothing saying so")

	t.Run("and a name a file declares is not taken", func(t *testing.T) {
		x := require.New(t)
		const declared = "declared.example.com"

		_, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Name: declared,
			Labels: map[string]string{cmd.Declared: "resources.yaml"},
		}.Build())
		x.NoError(err)

		_, err = take(theirs, b.Fabrikam, declared)
		x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)

		who, err := front.New(b.Ungated).WhoseHost(ctx, app.FrontWhoseHostRequest_builder{Host: declared}.Build())
		x.NoError(err)
		x.Equal(b.Contoso.Bytes(), who.GetTenant())
	})
}

// TestWhereFromAnswersACallerOfItsOwnTenant: `FrontService/WhereFrom` says
// where the people at an address authenticate, to a caller of that tenant.
//
// It took the tenant from the request and read through no wall, and nothing
// compared the two: a caller inside one tenant, whose role named the method,
// was answered another tenant's mail routing -- the row `MailDomain.Get`
// refuses the same caller (#277). A caller of another tenant is answered what a
// domain nobody has said anything about is answered, which is nothing.
func TestWhereFromAnswersACallerOfItsOwnTenant(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	for tenant, name := range map[pdid.Id]string{b.Contoso: "contoso.com", b.Fabrikam: "fabrikam.com"} {
		_, err := b.Ungated.MailDomain().Add(ctx, app.MailDomainAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(), Name: name, Provider: "entra",
		}.Build())
		x.NoError(err)
	}

	theirs := b.holder(t, ctx, b.Fabrikam, "frontdoor")
	role, err := b.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Fabrikam.Bytes()}.Build(), Alias: "front", Methods: []string{"/roster.FrontService/*"},
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role: app.RoleRef_builder{Id: role.GetId()}.Build(), Holder: app.HolderRef_builder{Id: theirs.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	c := app.NewFrontServiceClient(pdtest.Serve(t, b.grpc(t)))
	wire := asOverTheWire(ctx, theirs)

	res, err := c.WhereFrom(wire, app.FrontWhereFromRequest_builder{Tenant: b.Fabrikam.Bytes(), Address: "anybody@fabrikam.com"}.Build())
	x.NoError(err)
	x.Equal("entra", res.GetProvider(), "the control: its own tenant's")

	res, err = c.WhereFrom(wire, app.FrontWhereFromRequest_builder{Tenant: b.Contoso.Bytes(), Address: "anybody@contoso.com"}.Build())
	x.NoError(err)
	x.Empty(res.GetProvider(), "a caller of fabrikam was told how contoso's people sign in")
}
