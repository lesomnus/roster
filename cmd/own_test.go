package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/gate"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent/binding"
	"github.com/lesomnus/roster/internal/ent/credential"
	"github.com/lesomnus/roster/internal/ent/holder"
	entrole "github.com/lesomnus/roster/internal/ent/role"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
)

// withoutSelfService erases the role everybody of contoso holds over their own
// account, for a test about what the gate does for somebody who holds
// nothing at all -- which is a tenant that erased it, as one may.
func (b *built) withoutSelfService(t *testing.T, ctx context.Context) {
	t.Helper()

	_, err := b.Ungated.Role().Erase(ctx, app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{
		Alias:  z.Ptr(core.Owns),
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
	}.Build()}.Build())
	require.NoError(t, err)
}

// TestWhatSomebodyDoesToTheirOwnAccountNeedsNoRole: an address, a number, a
// key, how they sign in -- somebody's own are theirs to change with no role
// bound to them, by the role everybody of their tenant holds over what is
// their own (`core.Owns`).
//
// # What it was
//
// Somebody added an address to their own account by holding
// `EmailService/Add`, which a role says of the whole tenant: a tenant that let
// its people manage their own accounts bound all of them the write, and each
// then held it over everybody who held no more -- each other. Somebody given
// their own keys to mint minted one on a colleague's account, and called as
// them (#274).
func TestWhatSomebodyDoesToTheirOwnAccountNeedsNoRole(t *testing.T) {
	b, ctx := build(t)
	conn := served(t, b.Server)

	erin := b.holder(t, ctx, b.Contoso, "erin")
	as := asOverTheWire(ctx, erin)
	own := app.HolderRef_builder{Id: erin.Bytes()}.Build()

	t.Run("a tenant is made with the role, and nobody is bound it", func(t *testing.T) {
		x := require.New(t)

		r, err := b.Ent.Role.Query().Where(entrole.TenantIdEQ(b.Contoso.Uuid()), entrole.AliasEQ(core.Owns)).Only(ctx)
		x.NoError(err)
		x.ElementsMatch(core.Owning, r.Methods)

		n, err := b.Ent.Binding.Query().Where(binding.HasRoleWith(entrole.AliasEQ(core.Owns))).Count(ctx)
		x.NoError(err)
		x.Zero(n, "somebody was bound something for it")
	})

	t.Run("their own address, added and read and taken away", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewEmailServiceClient(conn).Add(as, app.EmailAddRequest_builder{Holder: own, Address: "erin@contoso.example"}.Build())
		x.NoError(err)

		vs, err := app.NewEmailServiceClient(conn).List(as, app.EmailListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)

		gone, err := app.NewEmailServiceClient(conn).Erase(as, app.EmailRef_builder{Id: v.GetId()}.Build())
		x.NoError(err)
		x.True(gone.GetErased())
	})

	t.Run("a link that proves an address is not theirs to ask roster for", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewEmailServiceClient(conn).Add(as, app.EmailAddRequest_builder{Holder: own, Address: "ceo@contoso.example"}.Build())
		x.NoError(err, "writing an address onto your own account is not what is refused")

		// `Verify` answers the link to its caller, and whoever minted a link
		// spends it. Somebody's own to call, it left one verb between them
		// and an address they do not read -- and somebody bound that verb
		// alone, `Confirm`, proved the CEO's address onto their own account.
		_, err = app.NewEmailServiceClient(conn).Verify(as, app.EmailVerifyRequest_builder{Ref: app.EmailRef_builder{Id: v.GetId()}.Build()}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		// And somebody bound `Confirm`, which is what it took: there is no
		// link of theirs to spend.
		sam := b.holder(t, ctx, b.Contoso, "sam")
		b.binds(t, sam, b.role(t, ctx, "confirms", app.EmailService_Confirm_FullMethodName), nil)
		asSam := asOverTheWire(ctx, sam)

		w, err := app.NewEmailServiceClient(conn).Add(asSam, app.EmailAddRequest_builder{
			Holder: app.HolderRef_builder{Id: sam.Bytes()}.Build(), Address: "cfo@contoso.example",
		}.Build())
		x.NoError(err)
		_, err = app.NewEmailServiceClient(conn).Verify(asSam, app.EmailVerifyRequest_builder{Ref: app.EmailRef_builder{Id: w.GetId()}.Build()}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		for _, id := range [][]byte{v.GetId(), w.GetId()} {
			got, err := b.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{Ref: app.EmailRef_builder{Id: id}.Build()}.Build())
			x.NoError(err)
			x.Nil(got.GetDateVerified(), "an address was proved by somebody asking roster for the link")
		}
	})

	t.Run("and who they say they are", func(t *testing.T) {
		x := require.New(t)

		got, err := app.NewHolderServiceClient(conn).Get(as, app.HolderGetRequest_builder{Ref: own}.Build())
		x.NoError(err)

		v, err := app.NewHolderServiceClient(conn).Update(as, app.HolderUpdateRequest_builder{
			Ref: own, DateUpdated: got.GetDateUpdated(), Profile: app.Profile_builder{DisplayName: "Erin Park"}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("Erin Park", v.GetProfile().GetDisplayName())
	})

	t.Run("it is said to them apart from what they hold", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewMeServiceClient(conn).Get(as, app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Empty(v.GetMethods(), "somebody who holds nothing was told they hold something")
		x.ElementsMatch(core.Owning, v.GetOwn())

		// And it is not what somebody is said to reach, by whoever asks.
		r, err := b.Ungated.Holder().Reaches(ctx, app.HolderReachesRequest_builder{Ref: own}.Build())
		x.NoError(err)
		x.Empty(r.GetMethods())
	})

	t.Run("what is not somebody's own still asks for a role", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewHolderServiceClient(conn).List(as, app.HolderListRequest_builder{}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
		_, err = app.NewTeamServiceClient(conn).List(as, app.TeamListRequest_builder{}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("a tenant takes a method out, and it is nobody's", func(t *testing.T) {
		x := require.New(t)

		role := app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{
			Alias: z.Ptr(core.Owns), Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		}.Build()}.Build()
		_, err := b.Ungated.Role().Narrow(ctx, app.RoleNarrowRequest_builder{
			Ref: role, Methods: []string{app.EmailService_Add_FullMethodName},
		}.Build())
		x.NoError(err)

		_, err = app.NewEmailServiceClient(conn).Add(as, app.EmailAddRequest_builder{Holder: own, Address: "again@contoso.example"}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

		// The rest is as it was.
		_, err = app.NewEmailServiceClient(conn).List(as, app.EmailListRequest_builder{}.Build())
		x.NoError(err)

		// And a tenant that erases the role has none of it.
		_, err = b.Ungated.Role().Erase(ctx, role)
		x.NoError(err)
		_, err = app.NewEmailServiceClient(conn).List(as, app.EmailListRequest_builder{}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	})
}

// TestWhatIsSomebodysOwnReachesNobodyElse is every method of `core.Owning`,
// called by somebody who holds nothing, **about somebody else**.
//
// These are let through the gate for everybody, and each takes a "whose".
// What keeps one from being about anybody is not the absence of that
// argument, as it is for `MeService` (`TestNothingTheWallIsWaivedForCanNameAnybody`):
// it is that a method held nowhere reaches the caller's own rows and no
// others (`cmd.People`). So that is what is tried, one method at a time -- and
// a method added to the list with no case here fails this.
func TestWhatIsSomebodysOwnReachesNobodyElse(t *testing.T) {
	b, ctx := build(t)
	conn := served(t, b.Server)

	// Two people who hold nothing, which is two colleagues the day they are
	// given accounts. The one is asking about the other.
	mallory, victim := b.holder(t, ctx, b.Contoso, "mallory"), b.holder(t, ctx, b.Contoso, "victim")
	as := asOverTheWire(ctx, mallory)
	theirs := app.HolderRef_builder{Id: victim.Bytes()}.Build()

	// What the other has.
	b.sets(t, ctx, victim, "the one the victim chose")
	e, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: theirs, Address: "victim@contoso.example"}.Build())
	require.NoError(t, err)
	p, err := b.Ungated.Phone().Add(ctx, app.PhoneAddRequest_builder{Holder: theirs, Number: "555 0100", Kind: "work"}.Build())
	require.NoError(t, err)
	k, err := b.Walled.ApiKey().Issue(b.as(ctx, b.ContosoUser, b.Contoso), app.ApiKeyIssueRequest_builder{
		Holder: theirs, Alias: "the-victims", Methods: []string{app.MeService_Get_FullMethodName},
	}.Build())
	require.NoError(t, err)
	secret, err := b.Ent.Credential.Query().Where(credential.HasHolderWith(holder.IdEQ(victim.Uuid()))).FirstId(ctx)
	require.NoError(t, err)
	account := b.identity(t, ctx, victim, "github", "the-victims")

	// refused is a call that did nothing: the row is not one the caller sees,
	// or the write is one the layer refuses for it.
	refused := func(x *require.Assertions, err error) {
		x.Contains([]codes.Code{codes.NotFound, codes.PermissionDenied}, status.Code(err), "%v", err)
	}
	still := func(x *require.Assertions) {
		_, err := b.Ungated.Email().Get(ctx, app.EmailGetRequest_builder{Ref: app.EmailRef_builder{Id: e.GetId()}.Build()}.Build())
		x.NoError(err, "the victim's address is gone")
		_, err = b.Ungated.Phone().Get(ctx, app.PhoneGetRequest_builder{Ref: app.PhoneRef_builder{Id: p.GetId()}.Build()}.Build())
		x.NoError(err, "the victim's number is gone")
		row, err := b.Ent.ApiKey.Get(ctx, mustId(t, k.GetKey().GetId()).Uuid())
		x.NoError(err)
		x.Nil(row.DateErased, "the victim's key was ended")
		n, err := b.Ent.Credential.Query().Where(credential.HasHolderWith(holder.IdEQ(victim.Uuid()))).Count(ctx)
		x.NoError(err)
		x.Equal(1, n, "the victim's way in was changed")
		got, err := b.Ent.Identity.Get(ctx, mustId(t, account.GetId()).Uuid())
		x.NoError(err)
		x.Nil(got.DateErased, "the victim's account at a provider was taken away")
	}

	holders, emails, phones := app.NewHolderServiceClient(conn), app.NewEmailServiceClient(conn), app.NewPhoneServiceClient(conn)
	keys, creds, idents, dels := app.NewApiKeyServiceClient(conn), app.NewCredentialServiceClient(conn), app.NewIdentityServiceClient(conn), app.NewDelegationServiceClient(conn)

	cases := map[string]func(x *require.Assertions){
		app.HolderService_Get_FullMethodName: func(x *require.Assertions) {
			_, err := holders.Get(as, app.HolderGetRequest_builder{Ref: theirs}.Build())
			x.Equal(codes.NotFound, status.Code(err), "%v", err)
		},
		app.HolderService_Update_FullMethodName: func(x *require.Assertions) {
			_, err := holders.Update(as, app.HolderUpdateRequest_builder{Ref: theirs, Profile: app.Profile_builder{DisplayName: "Mallory"}.Build()}.Build())
			refused(x, err)
		},
		app.HolderService_Portray_FullMethodName: func(x *require.Assertions) {
			_, err := holders.Portray(as, app.HolderPortrayRequest_builder{Ref: theirs, Image: []byte("not an image")}.Build())
			x.NotEqual(codes.OK, status.Code(err), "%v", err)
		},

		app.EmailService_Get_FullMethodName: func(x *require.Assertions) {
			_, err := emails.Get(as, app.EmailGetRequest_builder{Ref: app.EmailRef_builder{Id: e.GetId()}.Build()}.Build())
			x.Equal(codes.NotFound, status.Code(err), "%v", err)
		},
		app.EmailService_List_FullMethodName: func(x *require.Assertions) {
			vs, err := emails.List(as, app.EmailListRequest_builder{}.Build())
			x.NoError(err)
			x.Empty(vs.GetItems(), "a list that named nobody answered somebody else's")
		},
		app.EmailService_Add_FullMethodName: func(x *require.Assertions) {
			_, err := emails.Add(as, app.EmailAddRequest_builder{Holder: theirs, Address: "mallory-reads-this@elsewhere.example"}.Build())
			refused(x, err)
		},
		app.EmailService_Erase_FullMethodName: func(x *require.Assertions) {
			v, err := emails.Erase(as, app.EmailRef_builder{Id: e.GetId()}.Build())
			x.NoError(err)
			x.False(v.GetErased())
		},

		app.PhoneService_Get_FullMethodName: func(x *require.Assertions) {
			_, err := phones.Get(as, app.PhoneGetRequest_builder{Ref: app.PhoneRef_builder{Id: p.GetId()}.Build()}.Build())
			x.Equal(codes.NotFound, status.Code(err), "%v", err)
		},
		app.PhoneService_List_FullMethodName: func(x *require.Assertions) {
			vs, err := phones.List(as, app.PhoneListRequest_builder{}.Build())
			x.NoError(err)
			x.Empty(vs.GetItems())
		},
		app.PhoneService_Add_FullMethodName: func(x *require.Assertions) {
			_, err := phones.Add(as, app.PhoneAddRequest_builder{Holder: theirs, Number: "555 0199", Kind: "mobile"}.Build())
			refused(x, err)
		},
		app.PhoneService_Erase_FullMethodName: func(x *require.Assertions) {
			v, err := phones.Erase(as, app.PhoneRef_builder{Id: p.GetId()}.Build())
			x.NoError(err)
			x.False(v.GetErased())
		},

		app.IdentityService_Add_FullMethodName: func(x *require.Assertions) {
			_, err := idents.Add(as, app.IdentityAddRequest_builder{Holder: theirs, Provider: "github", Subject: "an-account-of-mallorys"}.Build())
			refused(x, err)
		},
		app.IdentityService_Erase_FullMethodName: func(x *require.Assertions) {
			v, err := idents.Erase(as, app.IdentityRef_builder{Id: account.GetId()}.Build())
			x.NoError(err)
			x.False(v.GetErased())
		},
		app.CredentialService_Set_FullMethodName: func(x *require.Assertions) {
			_, err := creds.Set(as, app.CredentialSetRequest_builder{Holder: theirs, Kind: "password", Secret: []byte("one mallory chose, long enough")}.Build())
			refused(x, err)
		},
		app.CredentialService_Enrol_FullMethodName: func(x *require.Assertions) {
			_, err := creds.Enrol(as, app.CredentialEnrolRequest_builder{Holder: theirs, Kind: "totp", Name: "mallorys-phone"}.Build())
			refused(x, err)
		},
		app.CredentialService_Erase_FullMethodName: func(x *require.Assertions) {
			_, err := creds.Erase(as, app.CredentialRef_builder{Id: secret[:]}.Build())
			if err == nil {
				return // nothing erased, which `still` reads
			}
			refused(x, err)
		},

		app.ApiKeyService_List_FullMethodName: func(x *require.Assertions) {
			vs, err := keys.List(as, app.ApiKeyListRequest_builder{}.Build())
			x.NoError(err)
			x.Empty(vs.GetItems())
		},
		app.ApiKeyService_Issue_FullMethodName: func(x *require.Assertions) {
			// The call the finding was: a key on a colleague's account.
			_, err := keys.Issue(as, app.ApiKeyIssueRequest_builder{Holder: theirs, Alias: "not-the-victims", Methods: []string{app.MeService_Get_FullMethodName}}.Build())
			refused(x, err)
		},
		app.ApiKeyService_Erase_FullMethodName: func(x *require.Assertions) {
			_, err := keys.Erase(as, app.ApiKeyRef_builder{Id: k.GetKey().GetId()}.Build())
			if err == nil {
				return
			}
			refused(x, err)
		},
		app.DelegationService_List_FullMethodName: func(x *require.Assertions) {
			vs, err := dels.List(as, app.DelegationListRequest_builder{}.Build())
			x.NoError(err)
			x.Empty(vs.GetItems())
		},
		app.DelegationService_Erase_FullMethodName: func(x *require.Assertions) {
			_, err := dels.Erase(as, app.DelegationRef_builder{Id: victim.Bytes()}.Build())
			if err == nil {
				return
			}
			refused(x, err)
		},
	}

	for _, m := range core.Owning {
		t.Run(m, func(t *testing.T) {
			x := require.New(t)

			try, ok := cases[m]
			x.True(ok, "%s is somebody's own to call and nothing here tries it about somebody else", m)

			// Let through, so that what is tried is the wall and not the gate.
			x.NoError(cmd.Policy(b.Ent).May(ctx, gate.Call{Actor: mallory, Tenant: b.Contoso, Action: m}))

			try(x)
			still(x)
		})
	}
	require.Len(t, cases, len(core.Owning), "a case for a method that is not on the list")
}

// TestTheRoleEverybodyHoldsNamesOnlyWhatIsTheirOwn: the role is held by
// everybody, so what it may name is what is somebody's own to do -- refused at
// the write, and not held when it is there anyway.
func TestTheRoleEverybodyHoldsNamesOnlyWhatIsTheirOwn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	role := app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{Alias: z.Ptr(core.Owns), Tenant: at}.Build()}.Build()
	admin := b.as(ctx, b.ContosoUser, b.Contoso)

	// Widened with a verb about anybody, by the tenant's administrator.
	_, err := b.Walled.Role().Widen(admin, app.RoleWidenRequest_builder{
		Ref: role, Methods: []string{app.HolderService_Erase_FullMethodName},
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)

	// Made again with one, by a tenant that had erased it; or in a site.
	_, err = b.Ungated.Role().Erase(ctx, role)
	x.NoError(err)
	_, err = b.Walled.Role().Add(admin, app.RoleAddRequest_builder{
		Tenant: at, Alias: core.Owns, Methods: []string{app.EmailService_Add_FullMethodName, app.HolderService_List_FullMethodName},
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	seoul := b.site(t, ctx, b.Contoso, "seoul")
	_, err = b.Walled.Role().Add(admin, app.RoleAddRequest_builder{
		Tenant: at, Site: app.SiteRef_builder{Id: seoul.Bytes()}.Build(), Alias: core.Owns, Methods: []string{app.EmailService_Add_FullMethodName},
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)

	// And written past the layer -- a row from before the rule, a database
	// edited by hand -- what is not somebody's own is not held through it.
	_, err = b.Walled.Role().Add(admin, app.RoleAddRequest_builder{
		Tenant: at, Alias: core.Owns, Methods: []string{app.EmailService_Add_FullMethodName},
	}.Build())
	x.NoError(err)
	n, err := b.Ent.Role.Update().
		Where(entrole.TenantIdEQ(b.Contoso.Uuid()), entrole.AliasEQ(core.Owns), entrole.DateErasedIsNil()).
		SetMethods([]string{"/roster.*/*"}).
		Save(ctx)
	x.NoError(err)
	x.Equal(1, n)

	nobody := b.holder(t, ctx, b.Contoso, "nobody")
	may := func(method string) error {
		return cmd.Policy(b.Ent).May(ctx, gate.Call{Actor: nobody, Tenant: b.Contoso, Action: method})
	}
	x.NoError(may(app.EmailService_Add_FullMethodName), "a pattern that covers a method on the list holds it")
	x.Equal(codes.PermissionDenied, status.Code(may(app.HolderService_Erase_FullMethodName)))
	x.Equal(codes.PermissionDenied, status.Code(may(app.HolderService_List_FullMethodName)))
	x.Equal(codes.PermissionDenied, status.Code(may(app.TenantService_Close_FullMethodName)))
}
