package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/frame"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/me"
)

// TestMeAnswersWhatTheCredentialLeavesOfWhatIsHeld.
//
// `Me.Get`'s `methods` is what a page draws buttons from, and it is what the
// person holds narrowed to what the credential in hand allows -- a delegation
// names its methods one by one, an api key may name a pattern. The first shape
// of that narrowing asked whether the credential allowed each **held pattern**,
// so a person bound to `/roster.*/*` and reached through a delegation naming
// twenty methods was told they held none of them; the account page then said
// "this needs a role naming …" under every heading, to somebody holding
// everything. Found by the browser (`ts/e2e/account.spec.ts`), with every
// other gate green.
func TestMeAnswersWhatTheCredentialLeavesOfWhatIsHeld(t *testing.T) {
	const (
		meGet    = "/roster.MeService/Get"
		emailAdd = "/roster.EmailService/Add"
		anyMe    = "/roster.MeService/*"
		unlink   = "/roster.MeService/Unlink"
		everyone = "/roster.*/*"
	)

	b, ctx := build(t)
	s := me.New(b.Ent, cmd.Everything(b.Ent))

	t.Run("a wildcard role through a delegation holds what the delegation names", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Contoso, "erin")
		b.mayCall(t, ctx, who, "everything", everyone)

		f := frame.New(who, b.Contoso, frame.Whole().To(meGet, emailAdd)).WithScope(frame.Only(b.Contoso))
		v, err := s.Get(frame.Into(ctx, f), app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.ElementsMatch([]string{meGet, emailAdd}, v.GetMethods())
	})

	t.Run("a narrow role through a wide key holds the role", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Contoso, "sam")
		b.mayCall(t, ctx, who, "reader", meGet)

		f := frame.New(who, b.Contoso, frame.Whole().To(anyMe)).WithScope(frame.Only(b.Contoso))
		v, err := s.Get(frame.Into(ctx, f), app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal([]string{meGet}, v.GetMethods())
	})

	t.Run("and what neither side reaches is not there", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Contoso, "pat")
		b.mayCall(t, ctx, who, "some", meGet, emailAdd)

		f := frame.New(who, b.Contoso, frame.Whole().To(unlink, emailAdd)).WithScope(frame.Only(b.Contoso))
		v, err := s.Get(frame.Into(ctx, f), app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal([]string{emailAdd}, v.GetMethods())
	})

	t.Run("and the ordinary way in sees no difference", func(t *testing.T) {
		x := require.New(t)

		who := b.holder(t, ctx, b.Contoso, "kim")
		b.mayCall(t, ctx, who, "all", everyone)

		v, err := s.Get(b.as(ctx, who, b.Contoso), app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Contains(v.GetMethods(), everyone)
	})
}
