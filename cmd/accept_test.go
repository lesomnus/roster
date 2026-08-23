package cmd_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

const (
	accept     = "/roster.VouchService/Accept"
	listPeople = "/roster.HolderService/List"
)

// TestAFrontDoorThatDidItsOwnCheckingIsBelieved.
//
// The last thing D23 left open, in as many words: *a deployment with Hydra in
// front does not call `Vouch` at all, so there is nothing for the token to ride
// back on. Exchanging an `id_token` for one is the obvious route and it is not
// designed.* So an OIDC-fronted deployment could find out who somebody was and
// could not get a credential to act **as** them, which is the whole of what
// `roster-as` is for.
//
// roster does not check the token, and that was decided before this:
// `connection.proto` says *using it means doing the OIDC exchange, which is
// being the relying party and is what D19 says roster is not.* So the front
// door checks, and roster accepts -- which is a different act and is named like
// one.
func TestAFrontDoorThatDidItsOwnCheckingIsBelieved(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, accept, listPeople)
	ctx := t.Context()

	// Somebody who arrives through a provider, which is the case with no
	// password to prove.
	who := addHolder(t, ctx, b.Server, b.Contoso, "arrives-through-entra")
	mustIdentity(t, ctx, b.Server, who, "entra", "entra-subject-1")

	// What **they** may do, which is the ceiling on anything minted for them.
	mayList(t, ctx, b, who, listPeople)

	c := app.NewVouchServiceClient(b.Conn)

	res, err := c.Accept(bearing(ctx, b.Token), app.VouchAcceptRequest_builder{
		Claim: app.VouchClaim_builder{
			Tenant:   b.Contoso.Bytes(),
			Provider: "entra",
			Subject:  "entra-subject-1",
		}.Build(),
		Methods: []string{listPeople},
	}.Build())
	x.NoError(err)

	x.True(res.GetVerified().GetOk())
	x.Equal(who.Bytes(), res.GetVerified().GetHolder())
	x.NotEmpty(res.GetToken())

	t.Run("and what comes back is a delegation, for them", func(t *testing.T) {
		x := require.New(t)

		x.True(strings.HasPrefix(res.GetToken(), keys.PrefixDelegation),
			"the front door was handed something other than a delegation: %q", res.GetToken())

		// It rides in `roster-as` beside the app's own key, which is the pair
		// `keys.Acting` needs -- a delegation alone is refused, and there is a
		// test that says so.
		vs, err := app.NewHolderServiceClient(b.Conn).List(
			acting(ctx, b.Token, res.GetToken()), app.HolderListRequest_builder{}.Build())
		x.NoError(err)
		x.NotEmpty(vs.GetItems())
	})

	t.Run("and it is not wider than the caller", func(t *testing.T) {
		x := require.New(t)

		// A method the **caller** does not hold, which `mayDelegate` refuses --
		// the same bound `Delegate` is under, unchanged.
		_, err := c.Accept(bearing(ctx, b.Token), app.VouchAcceptRequest_builder{
			Claim: app.VouchClaim_builder{
				Tenant:   b.Contoso.Bytes(),
				Provider: "entra",
				Subject:  "entra-subject-1",
			}.Build(),
			Methods: []string{"/roster.HolderService/Erase"},
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("and not wider than the person either", func(t *testing.T) {
		x := require.New(t)

		// Somebody with an identity and **no roles at all**, which is the
		// ordinary state of a person who has just been provisioned. A front
		// door minting for them gets a delegation that opens nothing, and that
		// is the point: a token says who somebody is and never what they may
		// do.
		nobody := addHolder(t, ctx, b.Server, b.Contoso, "no-roles")
		mustIdentity(t, ctx, b.Server, nobody, "entra", "entra-subject-3")

		res, err := c.Accept(bearing(ctx, b.Token), app.VouchAcceptRequest_builder{
			Claim: app.VouchClaim_builder{
				Tenant:   b.Contoso.Bytes(),
				Provider: "entra",
				Subject:  "entra-subject-3",
			}.Build(),
			Methods: []string{listPeople},
		}.Build())
		x.NoError(err, "the mint is about the caller's grant, not the person's")

		_, err = app.NewHolderServiceClient(b.Conn).List(
			acting(ctx, b.Token, res.GetToken()), app.HolderListRequest_builder{}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"a delegation opened a door its holder cannot")
	})

	t.Run("and a claim that reaches nobody is not provisioning", func(t *testing.T) {
		x := require.New(t)

		// Refused rather than creating a person. A front door that made rows by
		// receiving a token would be doing provisioning, which is a different
		// act with a different name.
		_, err := c.Accept(bearing(ctx, b.Token), app.VouchAcceptRequest_builder{
			Claim: app.VouchClaim_builder{
				Tenant:   b.Contoso.Bytes(),
				Provider: "entra",
				Subject:  "nobody-here",
			}.Build(),
			Methods: []string{listPeople},
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})

	t.Run("and a suspension holds whichever door somebody came through", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Ungated.Holder().Disable(ctx,
			app.HolderDisableRequest_builder{
				Ref: app.HolderRef_builder{Id: who.Bytes()}.Build(),
			}.Build())
		x.NoError(err)

		_, err = c.Accept(bearing(ctx, b.Token), app.VouchAcceptRequest_builder{
			Claim: app.VouchClaim_builder{
				Tenant:   b.Contoso.Bytes(),
				Provider: "entra",
				Subject:  "entra-subject-1",
			}.Build(),
			Methods: []string{listPeople},
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err),
			"a suspension that holds for a password and not for a token is a suspension that depends on the door")
	})
}

// TestNobodyAcceptsWithoutBeingAllowedTo.
//
// The grant is the whole of the control here -- there is no secret and no
// proof -- so it has to be a **method**, named in a role and in `--allow`,
// rather than a consequence of holding a key at all. An app that checks
// passwords through `Verify` and must never mint for somebody it did not check
// is a different grant from one that runs an OIDC flow, and before this they
// were the same.
func TestNobodyAcceptsWithoutBeingAllowedTo(t *testing.T) {
	x := require.New(t)

	// A Login App's key: it may check a password, and nothing else.
	b := keyFor(t, verify, listPeople)
	ctx := t.Context()

	who := addHolder(t, ctx, b.Server, b.Contoso, "somebody")
	mustIdentity(t, ctx, b.Server, who, "entra", "entra-subject-2")

	_, err := app.NewVouchServiceClient(b.Conn).Accept(bearing(ctx, b.Token),
		app.VouchAcceptRequest_builder{
			Claim: app.VouchClaim_builder{
				Tenant:   b.Contoso.Bytes(),
				Provider: "entra",
				Subject:  "entra-subject-2",
			}.Build(),
			Methods: []string{listPeople},
		}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err),
		"a key that may only check passwords minted a credential for somebody it did not check")

	t.Run("and minting one is warned about", func(t *testing.T) {
		x := require.New(t)

		v := cmd.Widest([]string{accept})
		x.NotEmpty(v, "a key that mints for anybody was minted quietly")
		x.Contains(v, "on the caller's word")
	})
}

func mustIdentity(t *testing.T, ctx context.Context, s *cmd.Server, of pdid.Id, provider, subject string) {
	t.Helper()

	_, err := s.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
		Holder:   app.HolderRef_builder{Id: of.Bytes()}.Build(),
		Provider: provider,
		Subject:  subject,
	}.Build())
	require.NoError(t, err)
}
