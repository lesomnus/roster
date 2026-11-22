package cmd_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestAPersonHasOneAccountAtAProviderWhenTwoAreAddedAtOnce.
//
// The rule is *a person has one account at a provider*
// (`TestASecondAccountAtOneProviderIsRefused`): a second is a link that found
// the wrong row, and is refused rather than joining two people into one. It
// was a read and then a write. Two accounts added for one person at the same
// moment each listed what they have, each found nothing at that provider, and
// both were written -- so the state the rule exists to refuse was reached by
// asking for it twice at once, and the layer's own comment said so. Forty
// people, each given two accounts at one provider at once: thirty-eight held
// both on SQLite, which serialises writers and so only leaves the gap between
// the read and the write.
//
// It is `TestTheLastWayInSurvivesTwoCallersAtOnce`'s case from the other
// side, and is held the way that is: the count and the write in one
// transaction, after a write on the person's own row that both callers make
// first (`server/core/only.go`).
//
// Written as many people rather than one, because one pair racing is a coin
// toss and forty is a claim. And read the loser's answer as well as the
// state: it is told the rule, in the rule's words, and not a conflict about a
// row it never named.
func TestAPersonHasOneAccountAtAProviderWhenTwoAreAddedAtOnce(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const people = 40

	both := 0
	for i := range people {
		who := b.holder(t, ctx, b.Contoso, fmt.Sprintf("person-%d", i))
		ref := app.HolderRef_builder{Id: who.Bytes()}.Build()

		// Both at once, each a different account at the one provider.
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, 2)
		for j := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start

				_, errs[j] = b.Ungated.Identity().Add(ctx, app.IdentityAddRequest_builder{
					Holder: ref, Provider: "github", Subject: fmt.Sprintf("gh-%d-%d", i, j),
				}.Build())
			}()
		}
		close(start)
		wg.Wait()

		left, err := b.Ungated.Identity().List(ctx, app.IdentityListRequest_builder{
			Filters: []*app.IdentityFilter{app.IdentityFilter_builder{Holder: ref}.Build()},
		}.Build())
		x.NoError(err)
		x.NotEmpty(left.GetItems(), "person %d: neither account was written: %v, %v", i, errs[0], errs[1])

		if len(left.GetItems()) > 1 {
			both++

			continue
		}

		// One was written and one refused, and the one refused was told why.
		refused := errs[0]
		if refused == nil {
			refused = errs[1]
		}
		x.Equal(codes.AlreadyExists, status.Code(refused), "person %d: %v", i, refused)
		x.ErrorContains(refused, "already has a github identity")
	}

	x.Zero(both, "%d of %d people hold two accounts at one provider", both, people)
}
