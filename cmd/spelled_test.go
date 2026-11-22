package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestAnAliasIsWrittenAsItWillBeRead: a write that says an alias with a
// capital in it, or a space at either end, is refused with the spelling it
// would have to be, for every row that has an alias.
//
// # What it was
//
// `Add` lowered and trimmed an alias and wrote the result, and said nothing;
// a reference compares an alias as it is sent. So a tenant added as `ACME`
// was `acme`, and reading it back by `ACME` was `NotFound`; a site added as
// `"  Seoul "` was kept as `seoul`; and adding `SEOUL` after that was
// `AlreadyExists`, about a name the caller never wrote. Whoever wrote the row
// and then looked for it by what they wrote did not find it.
//
// # The rule
//
// Nothing is rewritten silently. The write is refused, InvalidArgument at
// `alias`, saying the spelling: the answer every other name here gives -- a
// hostname, an address, a subject. A reference stays exact, so what is written
// is what is found. What a *person types* to sign in is folded
// (`TestASignInFoldsWhatSomebodyTyped`).
func TestAnAliasIsWrittenAsItWillBeRead(t *testing.T) {
	b, ctx := build(t)
	b.mayAnything(b.ContosoUser, b.Contoso)
	wire := asOverTheWire(ctx, b.ContosoUser)
	conn := served(t, b.Server)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	refused := func(t *testing.T, err error, spelling string) {
		t.Helper()
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		require.Contains(t, status.Convert(err).Message(), "alias")
		require.Contains(t, status.Convert(err).Message(), `"`+spelling+`"`, "the refusal says the spelling it would have to be")
	}

	rows := []struct {
		row string
		add func(alias string) error
		get func(alias string) error
	}{
		{"a tenant", func(alias string) error {
			// The deployment's own write, which is the one that makes a
			// tenant here: the rule is about the name and not about who
			// says it.
			_, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias}.Build())

			return err
		}, func(alias string) error {
			_, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: app.TenantRef_builder{Alias: &alias}.Build()}.Build())

			return err
		}},
		{"a person", func(alias string) error {
			_, err := app.NewHolderServiceClient(conn).Add(wire, app.HolderAddRequest_builder{Tenant: at, Alias: alias}.Build())

			return err
		}, func(alias string) error {
			_, err := app.NewHolderServiceClient(conn).Get(wire, app.HolderGetRequest_builder{
				Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: &alias, Tenant: at}.Build()}.Build(),
			}.Build())

			return err
		}},
		{"a site", func(alias string) error {
			_, err := app.NewSiteServiceClient(conn).Add(wire, app.SiteAddRequest_builder{Tenant: at, Alias: alias}.Build())

			return err
		}, func(alias string) error {
			_, err := app.NewSiteServiceClient(conn).Get(wire, app.SiteGetRequest_builder{
				Ref: app.SiteRef_builder{Slug: app.SiteRefBySlug_builder{Alias: &alias, Tenant: at}.Build()}.Build(),
			}.Build())

			return err
		}},
		{"a team", func(alias string) error {
			_, err := app.NewTeamServiceClient(conn).Add(wire, app.TeamAddRequest_builder{Tenant: at, Alias: alias}.Build())

			return err
		}, nil},
		{"a group", func(alias string) error {
			_, err := app.NewGroupServiceClient(conn).Add(wire, app.GroupAddRequest_builder{Tenant: at, Alias: alias}.Build())

			return err
		}, nil},
		{"a role", func(alias string) error {
			_, err := app.NewRoleServiceClient(conn).Add(wire, app.RoleAddRequest_builder{
				Tenant: at, Alias: alias, Methods: []string{app.HolderService_Get_FullMethodName},
			}.Build())

			return err
		}, nil},
		{"a key", func(alias string) error {
			_, err := app.NewApiKeyServiceClient(conn).Issue(wire, app.ApiKeyIssueRequest_builder{
				Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(), Alias: alias,
				Methods: []string{app.HolderService_Get_FullMethodName},
			}.Build())

			return err
		}, nil},
	}
	for _, r := range rows {
		t.Run(r.row, func(t *testing.T) {
			refused(t, r.add("Seoul"), "seoul")
			refused(t, r.add("SEOUL"), "seoul")
			refused(t, r.add("  seoul "), "seoul")

			// Not a name in any spelling: refused as it always was, by the
			// grammar and not by a spelling there is none of.
			err := r.add("Seoul Office")
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)

			require.NoError(t, r.add("seoul"), "the spelling it was told is written")
			if r.get != nil {
				require.NoError(t, r.get("seoul"), "and found by it")
				require.Equal(t, codes.NotFound, status.Code(r.get("Seoul")), "a reference is exact")
			}
		})
	}

	t.Run("nor is somebody renamed into one", func(t *testing.T) {
		who := b.holder(t, ctx, b.Contoso, "alice")
		got, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: who.Bytes()}.Build()}.Build())
		require.NoError(t, err)

		_, err = app.NewHolderServiceClient(conn).Realias(wire, app.HolderRealiasRequest_builder{
			Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(), Alias: "Alice-Kim", DateUpdated: got.GetDateUpdated(),
		}.Build())
		refused(t, err, "alice-kim")

		v, err := app.NewHolderServiceClient(conn).Realias(wire, app.HolderRealiasRequest_builder{
			Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(), Alias: "alice-kim", DateUpdated: got.GetDateUpdated(),
		}.Build())
		require.NoError(t, err)
		require.Equal(t, "alice-kim", v.GetAlias())
	})

	t.Run("nor made by a directory under one", func(t *testing.T) {
		as := b.directory(t, ctx)
		_, err := b.provision(as, "Erin", erinOid, "erin@contoso.example")
		refused(t, err, "erin")

		_, err = b.provision(as, "erin", erinOid, "erin@contoso.example")
		require.NoError(t, err)
	})

	t.Run("and a name nobody gave is made up, as it was", func(t *testing.T) {
		v, err := app.NewHolderServiceClient(conn).Add(wire, app.HolderAddRequest_builder{Tenant: at}.Build())
		require.NoError(t, err)
		require.NotEmpty(t, v.GetAlias())
	})
}

// TestASignInFoldsWhatSomebodyTyped: at a sign-in, the alias and the tenant's
// alias are what a person typed into a form, and are folded before they are
// looked for, as an address is.
//
// A password that was right was refused for `Alice` and for `Contoso`, while
// the same person typing ` Alice@Contoso.Example ` was let in: `Verify` and
// `Delegate` answered `ok: false` for the first two and `ok: true` for the
// third, and roster's own sign-in refused an operator who typed `Admin`. A
// row's alias is in small letters by the rule above, so there is one row a
// typed name can mean.
//
// Only there. A reference in any other call is something a program wrote, and
// stays exact.
func TestASignInFoldsWhatSomebodyTyped(t *testing.T) {
	const password = "the password they chose"

	b, ctx := build(t)
	alice := b.holder(t, ctx, b.Contoso, "alice")
	b.sets(t, ctx, alice, password)
	v := b.vouched()

	for _, typed := range []struct{ tenant, alias string }{
		{"contoso", "alice"},
		{"contoso", "Alice"},
		{"Contoso", "alice"},
		{"CONTOSO", "ALICE"},
		{" contoso ", " Alice "},
	} {
		t.Run("@"+typed.tenant+"/"+typed.alias, func(t *testing.T) {
			x := require.New(t)
			who := app.VouchWho_builder{Tenant: typed.tenant, Alias: typed.alias}.Build()

			res, err := v.Verify(ctx, app.VouchVerifyRequest_builder{Who: who, Secret: []byte(password)}.Build())
			x.NoError(err)
			x.True(res.GetOk(), "the right password, and the name of the one row it could mean")

			wrong, err := v.Verify(ctx, app.VouchVerifyRequest_builder{Who: who, Secret: []byte("not the password")}.Build())
			x.NoError(err)
			x.False(wrong.GetOk())

			// A front door's own sign-in: checked and handed a delegation in
			// one call, as the caller it is.
			door := b.as(ctx, b.ContosoUser, b.Contoso)
			d, err := v.Delegate(door, app.VouchDelegateRequest_builder{
				Who: who, Secret: []byte(password), Methods: []string{app.MeService_Get_FullMethodName},
			}.Build())
			x.NoError(err)
			x.True(d.GetVerified().GetOk())
			x.NotEmpty(d.GetToken())
		})
	}

	// A link to sign in by is asked for with the same name somebody typed,
	// and is for the same person.
	t.Run("and so is who a mailed link is for", func(t *testing.T) {
		x := require.New(t)
		door := b.as(ctx, b.ContosoUser, b.Contoso)

		made, err := v.Link(door, app.VouchLinkRequest_builder{
			Who: app.VouchWho_builder{Tenant: "Contoso", Alias: "Alice"}.Build(),
		}.Build())
		x.NoError(err)

		res, err := v.Redeem(door, app.VouchRedeemRequest_builder{
			Token: made.GetToken(), Methods: []string{app.MeService_Get_FullMethodName},
		}.Build())
		x.NoError(err)
		x.True(res.GetVerified().GetOk(), "the link was for nobody")
		x.Equal(alice.Bytes(), res.GetVerified().GetHolder())
	})

	t.Run("and a reference in any other call is exact", func(t *testing.T) {
		_, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
				Alias: z.Ptr("Alice"), Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			}.Build()}.Build(),
		}.Build())
		require.Equal(t, codes.NotFound, status.Code(err), "%v", err)
	})
}

// TestRostersOwnSignInFoldsWhatAnOperatorTyped: the sign-in roster serves for
// its own consoles is a form somebody types a name into, and is held to what
// a front door's is. An operator who typed `Admin` and the right password was
// refused; right is a session.
func TestRostersOwnSignInFoldsWhatAnOperatorTyped(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)
	password := passwordFrom(t, out)

	for _, typed := range []string{"admin", "Admin", " ADMIN "} {
		c := signIn(t, s, typed, password)
		x.NotNil(c, "%q and the right password were refused", typed)

		// And the session is the operator's.
		g, err := s.GrpcAdmin(ctx, cmd.Config{})
		x.NoError(err)
		me, err := app.NewMeServiceClient(pdtest.Serve(t, g)).Get(
			metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value)), app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal("admin", me.GetAlias())
	}
	x.Nil(signIn(t, s, "Admin", "not the password"))
}

// TestTheCommandLineFoldsANameSomebodyTypes: a reference typed at the command
// line -- `@Contoso/Alice` -- is folded by the command line before anything
// is sent, in a command that writes as in one that reads, and what is sent is
// the spelling that is written. A person types there, and that is where what
// a person types is folded.
//
// What the command line sends as it was given is the request's own JSON,
// which a program wrote: an alias in capitals there is refused as it is from
// any caller, with the spelling.
func TestTheCommandLineFoldsANameSomebodyTypes(t *testing.T) {
	x := require.New(t)
	c := seeded(t)

	_, err := entities(t, c, "holder", "add", "@Contoso/Alice")
	x.NoError(err)

	for _, typed := range []string{"@contoso/alice", "@Contoso/Alice", "@CONTOSO/ALICE"} {
		out, err := entities(t, c, "holder", "get", "-o", "json", typed)
		x.NoError(err, typed)
		x.Contains(out, `"alice"`, typed)
	}

	_, err = entities(t, c, "holder", "add", `{"tenant":{"alias":"contoso"},"alias":"Bob"}`)
	x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	x.ErrorContains(err, `"bob"`)
}
