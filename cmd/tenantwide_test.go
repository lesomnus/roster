package cmd_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/lesomnus/protobuf-patch/patch"
	"github.com/lesomnus/protobuf-patch/patchpb"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// TestAVerbAboutTheWholeTenantIsHeldAcrossIt: closing a tenant, restoring it,
// and setting how everybody in it signs in are each about all of it, so a role
// that reaches somebody only in a site, or only in a team, does not reach them
// -- however its methods read. The gate counts both, which is right for a verb
// on rows a site narrows and is not for the tenant's own row
// (`server/core/close.go`, `Core.acrossTheTenant`).
func TestAVerbAboutTheWholeTenantIsHeldAcrossIt(t *testing.T) {
	b, ctx := build(t)

	seoul := b.site(t, ctx, b.Contoso, "seoul")
	ours := b.team(t, ctx, seoul, "ours")
	admin := b.role(t, ctx, "tenant-admin", "/roster.TenantService/*")

	conn := served(t, b.Server)
	tenants := app.NewTenantServiceClient(conn)
	ref := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	// update turns passwords off for everybody, which is the setting the
	// finding was about besides closing: how the whole tenant signs in.
	update := func(as context.Context) error {
		was, err := tenants.Get(as, app.TenantGetRequest_builder{
			Ref: ref, Select: app.TenantSelect_builder{DateUpdated: z.Ptr(true)}.Build(),
		}.Build())
		if err != nil {
			return err
		}
		_, err = tenants.Update(as, app.TenantUpdateRequest_builder{
			Ref:         ref,
			DateUpdated: was.GetDateUpdated(),
			Config:      app.TenantConfig_builder{Password: z.Ptr(false)}.Build(),
		}.Build())

		return err
	}
	closing := func(as context.Context) error {
		_, err := tenants.Close(as, app.TenantCloseRequest_builder{Ref: ref}.Build())

		return err
	}
	// Restore is asked of the stack itself: once a tenant is closed nobody of
	// it is let in at a port, so this is the rule standing behind that one.
	restore := func(who pdid.Id) error {
		return restores(b.asNobody(ctx, who, b.Contoso), b.Walled, b.Contoso)
	}
	open := func(t *testing.T) {
		t.Helper()
		v, err := b.Ent.Tenant.Get(ctx, b.Contoso.Uuid())
		require.NoError(t, err)
		require.Nil(t, v.DateClosed, "the tenant was closed by somebody who holds that in a part of it")
		require.True(t, v.Config.GetPassword() || !v.Config.HasPassword(), "the tenant's sign-in was set by somebody who holds that in a part of it")
	}

	refused := func(t *testing.T, who pdid.Id) {
		t.Helper()
		x := require.New(t)
		as := asOverTheWire(ctx, who)

		x.Equal(codes.PermissionDenied, status.Code(closing(as)))
		x.Equal(codes.PermissionDenied, status.Code(update(as)))
		open(t)

		closes(t, ctx, b.Ungated, b.Contoso)
		x.Equal(codes.PermissionDenied, status.Code(restore(who)))
		x.NoError(restores(ctx, b.Ungated, b.Contoso))
	}

	t.Run("a role bound in a site", func(t *testing.T) {
		who := b.holder(t, ctx, b.Contoso, "seoul-admin")
		b.binds(t, who, admin, &seoul)
		refused(t, who)
	})

	t.Run("a role held in a team", func(t *testing.T) {
		who := b.holder(t, ctx, b.Contoso, "team-admin")
		b.inTeam(t, ctx, who, ours, admin)
		refused(t, who)
	})

	// The same role bound across the tenant is what it says.
	t.Run("the same role across the tenant", func(t *testing.T) {
		x := require.New(t)
		who := b.holder(t, ctx, b.Contoso, "admin")
		b.binds(t, who, admin, nil)
		as := asOverTheWire(ctx, who)

		x.NoError(update(as))
		x.NoError(closing(as))
		x.NoError(restore(who))
	})
}

// TestWhatIsTheTenantsAsAWholeIsWrittenAcrossIt: the rows that are no site's --
// the directories a tenant's people arrive through, the names it answers at
// and the claims to them, where an address at its domains signs in, who an
// app's key is answered as, and the sites it is divided into -- are each
// written by somebody who holds the write across the tenant. A role bound in a
// site, or held in a team, reaches none of them, whatever its methods say; the
// same role across the tenant reaches every one (`server/core/tenantwide.go`).
//
// Each kind's writes over the wire, and `Patch` and `Apply` -- which are shut at
// the transport and are what the deployment's own writers use -- through the
// stack as the same callers.
func TestWhatIsTheTenantsAsAWholeIsWrittenAcrossIt(t *testing.T) {
	b, ctx := build(t)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	seoul := b.site(t, ctx, b.Contoso, "seoul")
	ours := b.team(t, ctx, seoul, "ours")
	conn := served(t, b.Server)

	// One caller per shape a role reaches somebody in, each holding every
	// method: in a site, in a team, and across the tenant.
	everything := b.role(t, ctx, "every-method", "/roster.*/*")
	inSite := b.holder(t, ctx, b.Contoso, "in-site")
	b.binds(t, inSite, everything, &seoul)
	inTeam := b.holder(t, ctx, b.Contoso, "in-team")
	b.inTeam(t, ctx, inTeam, ours, everything)
	across := b.holder(t, ctx, b.Contoso, "across")
	b.binds(t, across, everything, nil)

	n := 0
	fresh := func(prefix string) string {
		n++

		return fmt.Sprintf("%s%d", prefix, n)
	}
	app2 := b.holder(t, ctx, b.Contoso, "an-app")

	// A write is answered for each caller: over the wire for the verbs a
	// caller reaches, and through the stack for the two that are shut there.
	type write struct {
		name string
		do   func(wire, stack context.Context) error
	}
	kinds := []struct {
		name   string
		writes []write
	}{
		{"connection", []write{
			{"Add", func(wire, _ context.Context) error {
				_, err := app.NewConnectionServiceClient(conn).Add(wire, app.ConnectionAddRequest_builder{
					Tenant: at, Name: fresh("entra-"), Issuer: "https://idp.example.test", ClientId: "x",
				}.Build())
				return err
			}},
			{"Update", func(wire, _ context.Context) error {
				c := seededConnection(t, b, fresh("update-"))
				_, err := app.NewConnectionServiceClient(conn).Update(wire, app.ConnectionUpdateRequest_builder{
					Ref: c.Ref(), DateUpdated: c.GetDateUpdated(), Desc: z.Ptr("changed"),
				}.Build())
				return err
			}},
			{"Erase", func(wire, _ context.Context) error {
				_, err := app.NewConnectionServiceClient(conn).Erase(wire, seededConnection(t, b, fresh("erase-")).Ref())
				return err
			}},
			{"Patch", func(_, stack context.Context) error {
				c := seededConnection(t, b, fresh("patch-"))
				_, err := b.Walled.Connection().Patch(stack, app.ConnectionPatchRequest_builder{
					Ref: c.Ref(), Desc: z.Ptr("patched"), DateUpdatedForce: z.Ptr(true),
				}.Build())
				return err
			}},
			{"Apply", func(_, stack context.Context) error {
				_, err := b.Walled.Connection().Apply(stack, app.ConnectionApplyRequest_builder{
					Ref: seededConnection(t, b, fresh("apply-")).Ref(), Patch: describing(t, "roster.Connection", "desc"),
				}.Build())
				return err
			}},
		}},
		{"host", []write{
			{"Add", func(wire, _ context.Context) error {
				_, err := app.NewHostServiceClient(conn).Add(wire, app.HostAddRequest_builder{Tenant: at, Name: fresh("add") + ".contoso.test"}.Build())
				return err
			}},
			{"Update", func(wire, _ context.Context) error {
				h := seededHost(t, b, fresh("update")+".contoso.test")
				_, err := app.NewHostServiceClient(conn).Update(wire, app.HostUpdateRequest_builder{
					Ref: h.Ref(), DateUpdated: h.GetDateUpdated(), Desc: z.Ptr("changed"),
				}.Build())
				return err
			}},
			{"Erase", func(wire, _ context.Context) error {
				_, err := app.NewHostServiceClient(conn).Erase(wire, seededHost(t, b, fresh("erase")+".contoso.test").Ref())
				return err
			}},
			{"Patch", func(_, stack context.Context) error {
				h := seededHost(t, b, fresh("patch")+".contoso.test")
				_, err := b.Walled.Host().Patch(stack, app.HostPatchRequest_builder{
					Ref: h.Ref(), Desc: z.Ptr("patched"), DateUpdatedForce: z.Ptr(true),
				}.Build())
				return err
			}},
			{"Apply", func(_, stack context.Context) error {
				_, err := b.Walled.Host().Apply(stack, app.HostApplyRequest_builder{
					Ref: seededHost(t, b, fresh("apply")+".contoso.test").Ref(), Patch: describing(t, "roster.Host", "desc"),
				}.Build())
				return err
			}},
		}},
		{"host proof", []write{
			{"Add", func(wire, _ context.Context) error {
				_, err := app.NewHostProofServiceClient(conn).Add(wire, app.HostProofAddRequest_builder{Tenant: at, Name: fresh("claim") + ".contoso.test"}.Build())
				return err
			}},
			{"Erase", func(wire, _ context.Context) error {
				_, err := app.NewHostProofServiceClient(conn).Erase(wire, seededProof(t, b, fresh("erase")+".contoso.test").Ref())
				return err
			}},
			{"Patch", func(_, stack context.Context) error {
				p := seededProof(t, b, fresh("patch")+".contoso.test")
				_, err := b.Walled.HostProof().Patch(stack, app.HostProofPatchRequest_builder{
					Ref: p.Ref(), Desc: z.Ptr("patched"), DateUpdatedForce: z.Ptr(true),
				}.Build())
				return err
			}},
			{"Apply", func(_, stack context.Context) error {
				_, err := b.Walled.HostProof().Apply(stack, app.HostProofApplyRequest_builder{
					Ref: seededProof(t, b, fresh("apply")+".contoso.test").Ref(), Patch: describing(t, "roster.HostProof", "desc"),
				}.Build())
				return err
			}},
		}},
		{"mail domain", []write{
			{"Add", func(wire, _ context.Context) error {
				_, err := app.NewMailDomainServiceClient(conn).Add(wire, app.MailDomainAddRequest_builder{Tenant: at, Name: fresh("add") + ".contoso.test"}.Build())
				return err
			}},
			{"Update", func(wire, _ context.Context) error {
				m := seededDomain(t, b, fresh("update")+".contoso.test")
				_, err := app.NewMailDomainServiceClient(conn).Update(wire, app.MailDomainUpdateRequest_builder{
					Ref: m.Ref(), DateUpdated: m.GetDateUpdated(), Desc: z.Ptr("changed"),
				}.Build())
				return err
			}},
			{"Erase", func(wire, _ context.Context) error {
				_, err := app.NewMailDomainServiceClient(conn).Erase(wire, seededDomain(t, b, fresh("erase")+".contoso.test").Ref())
				return err
			}},
			{"Patch", func(_, stack context.Context) error {
				m := seededDomain(t, b, fresh("patch")+".contoso.test")
				_, err := b.Walled.MailDomain().Patch(stack, app.MailDomainPatchRequest_builder{
					Ref: m.Ref(), Desc: z.Ptr("patched"), DateUpdatedForce: z.Ptr(true),
				}.Build())
				return err
			}},
			{"Apply", func(_, stack context.Context) error {
				_, err := b.Walled.MailDomain().Apply(stack, app.MailDomainApplyRequest_builder{
					Ref: seededDomain(t, b, fresh("apply")+".contoso.test").Ref(), Patch: describing(t, "roster.MailDomain", "desc"),
				}.Build())
				return err
			}},
		}},
		{"nomination", []write{
			{"Add", func(wire, _ context.Context) error {
				_, err := app.NewNominationServiceClient(conn).Add(wire, app.NominationAddRequest_builder{
					Tenant: at, Name: fresh("app-"), BorrowerId: pdid.New(pd.HolderDomain).Bytes(),
					ActsAs: app.HolderRef_builder{Id: app2.Bytes()}.Build(),
				}.Build())
				return err
			}},
			{"Erase", func(wire, _ context.Context) error {
				_, err := app.NewNominationServiceClient(conn).Erase(wire, seededNomination(t, b, app2, fresh("erase-")).Ref())
				return err
			}},
			{"Patch", func(_, stack context.Context) error {
				v := seededNomination(t, b, app2, fresh("patch-"))
				_, err := b.Walled.Nomination().Patch(stack, app.NominationPatchRequest_builder{
					Ref: v.Ref(), Name: z.Ptr("patched"), DateUpdatedForce: z.Ptr(true),
				}.Build())
				return err
			}},
			{"Apply", func(_, stack context.Context) error {
				_, err := b.Walled.Nomination().Apply(stack, app.NominationApplyRequest_builder{
					Ref: seededNomination(t, b, app2, fresh("apply-")).Ref(), Patch: describing(t, "roster.Nomination", "name"),
				}.Build())
				return err
			}},
		}},
		{"site", []write{
			{"Add", func(wire, _ context.Context) error {
				_, err := app.NewSiteServiceClient(conn).Add(wire, app.SiteAddRequest_builder{Tenant: at, Alias: fresh("site-")}.Build())
				return err
			}},
			{"Erase", func(wire, _ context.Context) error {
				_, err := app.NewSiteServiceClient(conn).Erase(wire, app.SiteRef_builder{Id: b.site(t, ctx, b.Contoso, fresh("erase-")).Bytes()}.Build())
				return err
			}},
			{"Patch", func(_, stack context.Context) error {
				_, err := b.Walled.Site().Patch(stack, app.SitePatchRequest_builder{
					Ref:  app.SiteRef_builder{Id: b.site(t, ctx, b.Contoso, fresh("patch-")).Bytes()}.Build(),
					Desc: z.Ptr("patched"), DateUpdatedForce: z.Ptr(true),
				}.Build())
				return err
			}},
			{"Apply", func(_, stack context.Context) error {
				_, err := b.Walled.Site().Apply(stack, app.SiteApplyRequest_builder{
					Ref:   app.SiteRef_builder{Id: b.site(t, ctx, b.Contoso, fresh("apply-")).Bytes()}.Build(),
					Patch: describing(t, "roster.Site", "desc"),
				}.Build())
				return err
			}},
		}},
	}

	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			for _, w := range k.writes {
				for _, who := range []struct {
					name string
					id   pdid.Id
				}{{"in a site", inSite}, {"in a team", inTeam}} {
					err := w.do(asOverTheWire(ctx, who.id), b.asNobody(ctx, who.id, b.Contoso))
					require.Equal(t, codes.PermissionDenied, status.Code(err), "%s by a role held %s: %v", w.name, who.name, err)
				}

				// Across the tenant the rule passes, whatever the write's own
				// rules then say -- a name nobody has proved, say.
				err := w.do(asOverTheWire(ctx, across), b.asNobody(ctx, across, b.Contoso))
				require.NotEqual(t, codes.PermissionDenied, status.Code(err), "%s across the tenant: %v", w.name, err)
			}
		})
	}
}

// The rows each kind's writes are made against, put down by the deployment.

func seededConnection(t *testing.T, b *built, name string) *app.Connection {
	t.Helper()
	v, err := b.Ungated.Connection().Add(t.Context(), app.ConnectionAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Name: name, Issuer: "https://idp.example.test", ClientId: "x",
	}.Build())
	require.NoError(t, err)

	return v
}

func seededHost(t *testing.T, b *built, name string) *app.Host {
	t.Helper()
	v, err := b.Ungated.Host().Add(t.Context(), app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Name: name,
	}.Build())
	require.NoError(t, err)

	return v
}

func seededProof(t *testing.T, b *built, name string) *app.HostProof {
	t.Helper()
	v, err := b.Ungated.HostProof().Add(t.Context(), app.HostProofAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Name: name,
	}.Build())
	require.NoError(t, err)

	return v
}

func seededDomain(t *testing.T, b *built, name string) *app.MailDomain {
	t.Helper()
	v, err := b.Ungated.MailDomain().Add(t.Context(), app.MailDomainAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Name: name,
	}.Build())
	require.NoError(t, err)

	return v
}

func seededNomination(t *testing.T, b *built, actsAs pdid.Id, name string) *app.Nomination {
	t.Helper()
	v, err := b.Ungated.Nomination().Add(t.Context(), app.NominationAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Name: name,
		BorrowerId: pdid.New(pd.HolderDomain).Bytes(), ActsAs: app.HolderRef_builder{Id: actsAs.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)

	return v
}

// describing is a patch that sets one text field of a row, for an `Apply`.
func describing(t *testing.T, message, field string) *patchpb.Patch {
	t.Helper()
	v, err := patch.New(message, patch.Target(patch.Name(field)).Assign(patch.Str("applied")))
	require.NoError(t, err)

	return v
}
