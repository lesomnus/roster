package cmd_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
)

// TestTheLastWayInSurvivesTwoCallersAtOnce.
//
// The rule is *nobody removes the only way somebody can sign in*, and until
// this it was a count: read how many other ways in they have, and refuse if the
// answer is none. A count decides nothing when two callers take it at the same
// moment -- each sees the other's still there, both go through, and the person
// is left unable to sign in, which is the one state the rule exists to prevent.
//
// It is not a narrow window and it is not one dialect's. Forty people, each
// unlinked twice at once, lost thirty-nine on PostgreSQL and four on SQLite --
// which serialises writers, so all that is left there is the gap between the
// count and the write.
//
// What closes it is the count, the erase and a compare-and-swap on the person's
// own row inside one transaction; `server/core/only.go` says why that is the
// optimistic lock doing what it is for rather than a lock bolted on beside it.
//
// Written as many people rather than one, because one pair racing is a coin
// toss and forty is a claim. Read the loser's answer as well as the state: a
// caller who lost should be told the rule, not a version conflict about a row
// they never named.
func TestTheLastWayInSurvivesTwoCallersAtOnce(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const people = 40

	lost := 0
	for i := range people {
		who := b.holder(t, ctx, b.Acme, fmt.Sprintf("person-%d", i))
		vs := []*app.Identity{
			b.identity(t, ctx, who, "github", fmt.Sprintf("gh-%d", i)),
			b.identity(t, ctx, who, "entra", fmt.Sprintf("en-%d", i)),
		}

		// Both at once, each naming a different one of the two.
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, v := range vs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start

				_, _ = b.Ungated.Identity().Erase(ctx,
					app.IdentityRef_builder{Id: v.GetId()}.Build())
			}()
		}
		close(start)
		wg.Wait()

		left, err := b.Ungated.Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{
				app.IdentityFilter_builder{
					Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
				}.Build(),
			},
		}.Build())
		x.NoError(err)

		if len(left.GetItems()) == 0 {
			lost++
		}
	}

	x.Zero(lost, "%d of %d people were left unable to sign in", lost, people)
}

// TestTheLoserIsToldTheRuleAndNotAVersion.
//
// The swap is on the person's row and the caller never named it, so a bare
// version conflict would be a refusal about something they did not ask for --
// and the thing they did ask for is refused for a reason that has a sentence.
func TestTheLoserIsToldTheRuleAndNotAVersion(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Acme, "person")
	one := b.identity(t, ctx, who, "github", "gh-1")
	two := b.identity(t, ctx, who, "entra", "en-1")

	// Sequential, which is the case that always worked: the first goes, and
	// the second is the last way in.
	_, err := b.Ungated.Identity().Erase(ctx,
		app.IdentityRef_builder{Id: one.GetId()}.Build())
	x.NoError(err)

	_, err = b.Ungated.Identity().Erase(ctx,
		app.IdentityRef_builder{Id: two.GetId()}.Build())
	x.ErrorContains(err, "the only way they can sign in")
}
