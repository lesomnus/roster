package cmd_test

import (
	"strings"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestAnIdentityIsHeldToAForm: an identity is found by its provider and its
// subject, compared as written, and both were written as anybody typed them.
// `Entra` beside the connection `entra` was a second account for one person,
// held to nothing the connection says of its subjects; ` entra` and a subject
// ` 4242 ` were accepted, and are rows no sign-in asks for. A provider is
// named as a connection is, and a subject is what the provider said with no
// space before or after it. Neither is folded into what was probably meant:
// what is refused is typed again, and what was rewritten would be a row its
// writer does not find by what they wrote.
func TestAnIdentityIsHeldToAForm(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)

	add := func(alias, provider, subject string) error {
		t.Helper()
		who := b.holder(t, ctx, b.Contoso, alias)
		_, err := b.Walled.Identity().Add(admin, app.IdentityAddRequest_builder{
			Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(), Provider: provider, Subject: subject,
		}.Build())

		return err
	}

	t.Run("a provider is named as a connection is", func(t *testing.T) {
		for _, provider := range []string{"Entra", " entra", "entra ", "entra id", "a/b", ""} {
			err := add("erin", provider, "4242")
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%q was taken for a provider: %v", provider, err)
		}
	})

	t.Run("and a subject has no space at either end", func(t *testing.T) {
		for _, subject := range []string{" 4242", "4242 ", " 4242 ", "\t4242", "4242\n", " ", ""} {
			err := add("erin", "github", subject)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%q was taken for a subject: %v", subject, err)
		}
	})

	t.Run("what a provider says is otherwise its own", func(t *testing.T) {
		x := require.New(t)
		x.NoError(add("erin", "github", "4242"))
		x.NoError(add("erin", "ldap", "uid=erin,ou=people,dc=contoso,dc=example"))
		x.NoError(add("erin", "okta-staff", "00u1 a2B3"))
	})

	t.Run("the deployment is held to it too", func(t *testing.T) {
		x := require.New(t)
		fred := b.holder(t, ctx, b.Contoso, "fred")
		_, err := b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
			Holder: app.HolderRef_builder{Id: fred.Bytes()}.Build(), Provider: "GitHub", Subject: "4243",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
		_, err = b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
			Holder: app.HolderRef_builder{Id: fred.Bytes()}.Build(), Provider: "github", Subject: " 4243",
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	})
}

// TestAGUIDIsOneSpelling: at a connection that names its people by `oid`, a
// subject is a GUID, and a GUID is the same one in capitals and in small
// letters. It was stored as it was written and compared as stored: the same
// `oid` in capitals on one person and in small letters on another was two
// rows, each spelling a different person, and a row typed in the spelling a
// token does not use was never found by a sign-in -- under `enrolling`, that
// arrival was a second person. So such a subject is kept in small letters,
// which is how Entra writes one, and is looked for in small letters however it
// was asked for: by a write, by a sign-in, by a directory.
func TestAGUIDIsOneSpelling(t *testing.T) {
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	capitals := strings.ToUpper(erinOid)
	require.NotEqual(t, erinOid, capitals)

	// A connection people signed in through before it named `oid`, so that two
	// of them hold the pairwise `sub` they were keyed by.
	_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/contoso/v2.0", ClientId: "the-app",
	}.Build())
	require.NoError(t, err)
	gail := b.identity(t, ctx, b.holder(t, ctx, b.Contoso, "gail"), "entra", "GGGGGGGGGGGGGGGGGGGGGIkzqFVrSaSaFHy782bbtaQ")
	hana := b.identity(t, ctx, b.holder(t, ctx, b.Contoso, "hana"), "entra", "HHHHHHHHHHHHHHHHHHHHHIkzqFVrSaSaFHy782bbtaQ")

	c := b.connectionOf(t, ctx, b.Contoso, "entra")
	_, err = b.Ungated.Connection().Update(ctx, app.ConnectionUpdateRequest_builder{
		Ref: app.ConnectionRef_builder{Id: c.GetId()}.Build(), DateUpdated: c.GetDateUpdated(),
		SubjectClaim: z.Ptr("oid"), Provisions: z.Ptr(true),
	}.Build())
	require.NoError(t, err)

	erin := b.holder(t, ctx, b.Contoso, "erin")
	fred := b.holder(t, ctx, b.Contoso, "fred")
	add := func(who []byte, provider, subject string) (*app.Identity, error) {
		t.Helper()

		return b.Walled.Identity().Add(admin, app.IdentityAddRequest_builder{
			Holder: app.HolderRef_builder{Id: who}.Build(), Provider: provider, Subject: subject,
		}.Build())
	}
	whose := func(provider, subject string) (*app.Identity, error) {
		t.Helper()

		return b.Walled.Identity().Get(admin, app.IdentityGetRequest_builder{
			Ref: app.IdentityRef_builder{Subject: app.IdentityRefBySubject_builder{
				TenantId: b.Contoso.Bytes(), Provider: z.Ptr(provider), Subject: z.Ptr(subject),
			}.Build()}.Build(),
			Select: app.IdentitySelect_builder{Holder: app.HolderSelect_builder{}.Build(), Subject: z.Ptr(true)}.Build(),
		}.Build())
	}

	t.Run("written in capitals, it is kept in small letters", func(t *testing.T) {
		x := require.New(t)
		v, err := add(erin.Bytes(), "entra", capitals)
		x.NoError(err)
		x.Equal(erinOid, v.GetSubject())
		x.Equal(erinOid, idOfHolder(t, b, ctx, erin).GetSubject())
	})

	t.Run("and is nobody else's in the other spelling", func(t *testing.T) {
		x := require.New(t)
		_, err := add(fred.Bytes(), "entra", erinOid)
		x.Equal(codes.AlreadyExists, status.Code(err), "one oid was written onto two people: %v", err)
		_, err = add(fred.Bytes(), "entra", capitals)
		x.Equal(codes.AlreadyExists, status.Code(err), "%v", err)
	})

	t.Run("and is found by either", func(t *testing.T) {
		x := require.New(t)
		for _, asked := range []string{erinOid, capitals} {
			v, err := whose("entra", asked)
			x.NoError(err, "nobody is found by %s", asked)
			x.Equal(erin.Bytes(), v.GetHolder().GetId())
		}
	})

	t.Run("an identity moved to its oid is moved to the one spelling", func(t *testing.T) {
		x := require.New(t)
		move := func(ref []byte, subject string) (*app.Identity, error) {
			return b.Walled.Identity().Resubject(admin, app.IdentityResubjectRequest_builder{
				Ref: app.IdentityRef_builder{Id: ref}.Build(), Subject: subject,
			}.Build())
		}

		_, err := move(hana.GetId(), capitals)
		x.Equal(codes.AlreadyExists, status.Code(err), "moved to somebody else's oid, in the other spelling: %v", err)

		v, err := move(gail.GetId(), strings.ToUpper(fredOid))
		x.NoError(err)
		x.Equal(fredOid, v.GetSubject())
	})

	t.Run("somebody a directory makes is made by it", func(t *testing.T) {
		x := require.New(t)
		const ivanOid = "3c4d5e6f-7081-92a3-b4c5-d6e7f8091a2b"
		provision := func(alias, subject string) (*app.Holder, error) {
			return b.Ungated.Holder().Provision(ctx, app.HolderProvisionRequest_builder{
				Tenant: at, Alias: alias, Provider: "entra", Subject: subject,
			}.Build())
		}

		h, err := provision("ivan", strings.ToUpper(ivanOid))
		x.NoError(err)
		x.Equal(ivanOid, idOfHolder(t, b, ctx, mustId(t, h.GetId())).GetSubject())

		_, err = provision("ivan-again", ivanOid)
		x.Equal(codes.AlreadyExists, status.Code(err), "one oid made two people: %v", err)
	})

	t.Run("and what a directory calls somebody is kept by it", func(t *testing.T) {
		x := require.New(t)
		const janeOid = "4d5e6f70-8192-a3b4-c5d6-e7f8091a2b3c"
		jane := b.holder(t, ctx, b.Contoso, "jane")
		_, err := b.Ungated.Holder().Tell(ctx, app.HolderTellRequest_builder{
			Ref: app.HolderRef_builder{Id: jane.Bytes()}.Build(), Entry: app.DirectoryEntry_builder{Subject: z.Ptr(strings.ToUpper(janeOid))}.Build(),
		}.Build())
		x.NoError(err)

		v, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Known: app.HolderRefByKnown_builder{
				DirectorySubject: z.Ptr(janeOid), Tenant: at,
			}.Build()}.Build(),
			Select: app.HolderSelect_builder{DirectorySubject: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err, "what the directory said in capitals is not found in small letters")
		x.Equal(jane.Bytes(), v.GetId())
		x.Equal(janeOid, v.GetDirectorySubject())
	})

	t.Run("and it is unlinked by either", func(t *testing.T) {
		x := require.New(t)
		// Another way in first, so that this one is not their last.
		b.sets(t, ctx, erin, "correct horse battery staple")
		res, err := b.Walled.Identity().Erase(admin, app.IdentityRef_builder{Subject: app.IdentityRefBySubject_builder{
			TenantId: b.Contoso.Bytes(), Provider: z.Ptr("entra"), Subject: z.Ptr(capitals),
		}.Build()}.Build())
		x.NoError(err)
		x.True(res.GetErased(), "the row asked for in capitals was not found to erase")
	})

	// Only where the subject is a GUID by its connection's own word. Anywhere
	// else a subject is whatever its provider says, capitals and all, and two
	// that differ are two.
	t.Run("a provider that does not name people by oid keeps what it says", func(t *testing.T) {
		x := require.New(t)
		v, err := add(fred.Bytes(), "ldap", capitals)
		x.NoError(err)
		x.Equal(capitals, v.GetSubject())

		_, err = whose("ldap", erinOid)
		x.Equal(codes.NotFound, status.Code(err), "%v", err)
	})
}

// TestASignInFindsAnOidInEitherSpelling: a front door presents the subject a
// token carried (`VouchService.Accept`), and at a connection that names its
// people by `oid` the person is whoever holds that GUID, whichever case the
// token wrote it in. It was whoever held that spelling, and nobody otherwise.
func TestASignInFindsAnOidInEitherSpelling(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, accept, listPeople)
	ctx := t.Context()

	_, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(), Name: "entra",
		Issuer: "https://login.microsoftonline.com/contoso/v2.0", ClientId: "the-app", SubjectClaim: "oid",
	}.Build())
	x.NoError(err)

	who := addHolder(t, ctx, b.Server, b.Contoso, "erin")
	mustIdentity(t, ctx, b.Server, who, "entra", erinOid)

	c := app.NewVouchServiceClient(b.Conn)
	for _, said := range []string{erinOid, strings.ToUpper(erinOid)} {
		res, err := c.Accept(bearing(ctx, b.Token), app.VouchAcceptRequest_builder{
			Claim:   app.VouchClaim_builder{Tenant: b.Contoso.Bytes(), Provider: "entra", Subject: said}.Build(),
			Methods: []string{listPeople},
		}.Build())
		x.NoError(err, "a token that says %s names nobody", said)
		x.Equal(who.Bytes(), res.GetVerified().GetHolder())
	}
}
