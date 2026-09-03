package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// TestSomebodySeesWhereTheyAreSignedInAndEndsOne is `Delegation.List` and
// `Delegation.Erase` on the wire (`ts/plan.md` § C): the entity's own verbs,
// served behind the layer that strips the token and the rule that narrows the
// rows, instead of a curated field on `Me.Get`.
//
// A delegation is minted for erin through a front door; she lists her own and
// sees the row without its secret, ends it, and the front door's next call as
// her is refused. Somebody plain cannot list a wider person's; an operator
// reaching them can.
func TestSomebodySeesWhereTheyAreSignedInAndEndsOne(t *testing.T) {
	const (
		delegate = "/roster.VouchService/Delegate"
		meGet    = "/roster.MeService/Get"
		list     = "/roster.DelegationService/List"
		erase    = "/roster.DelegationService/Erase"
	)

	x := require.New(t)
	b := keyFor(t, delegate, meGet)
	ctx := t.Context()

	own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()
	const password = "correct horse battery staple"
	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{Ref: own, Secret: []byte(password)}.Build())
	x.NoError(err)

	// The front door signs her in and holds the delegation, as `frontdoor` does.
	res, err := app.NewVouchServiceClient(b.Conn).Delegate(bearing(ctx, b.Token), app.VouchDelegateRequest_builder{
		Who:     app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		Secret:  []byte(password),
		Methods: []string{meGet},
	}.Build())
	x.NoError(err)
	x.NotEmpty(res.GetToken())
	asHer := metadata.AppendToOutgoingContext(bearing(ctx, b.Token), keys.HeaderActing, res.GetToken())

	// Her own credential, for the account screen's calls.
	permits(t, ctx, b, b.Contoso, b.Who, "self", list, erase)
	hers := mintFor(t, ctx, b, b.Who, "laptop", []string{list, erase}, time.Time{})
	cl := app.NewDelegationServiceClient(b.Conn)

	var id []byte
	t.Run("she sees where she is signed in, and not the token", func(t *testing.T) {
		x := require.New(t)

		vs, err := cl.List(bearing(ctx, hers), app.DelegationListRequest_builder{
			Filters: []*app.DelegationFilter{app.DelegationFilter_builder{Holder: own}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)
		v := vs.GetItems()[0]
		x.Empty(v.GetSecret(), "the token's hash left the store")
		x.Equal([]string{meGet}, v.GetMethods())
		x.NotContains(vs.String(), res.GetToken())
		id = v.GetId()
	})

	t.Run("and ends one, after which the front door cannot act as her", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewMeServiceClient(b.Conn).Get(asHer, app.MeGetRequest_builder{}.Build())
		x.NoError(err, "the delegation did not work before it was ended")

		_, err = cl.Erase(bearing(ctx, hers), app.DelegationRef_builder{Id: id}.Build())
		x.NoError(err)

		_, err = app.NewMeServiceClient(b.Conn).Get(asHer, app.MeGetRequest_builder{}.Build())
		x.Equal(codes.Unauthenticated, status.Code(err), "an ended delegation still acts")

		vs, err := cl.List(bearing(ctx, hers), app.DelegationListRequest_builder{
			Filters: []*app.DelegationFilter{app.DelegationFilter_builder{Holder: own}.Build()},
		}.Build())
		x.NoError(err)
		x.Empty(vs.GetItems())
	})

	t.Run("and not somebody wider's", func(t *testing.T) {
		x := require.New(t)

		boss := addHolder(t, ctx, b.Server, b.Contoso, "boss")
		permits(t, ctx, b, b.Contoso, boss, "admin", "/roster.HolderService/Erase")

		_, err := cl.List(bearing(ctx, hers), app.DelegationListRequest_builder{
			Filters: []*app.DelegationFilter{app.DelegationFilter_builder{
				Holder: app.HolderRef_builder{Id: boss.Bytes()}.Build(),
			}.Build()},
		}.Build())
		x.Equal(codes.PermissionDenied, status.Code(err), "a plain person listed an administrator's sessions")
	})
}
