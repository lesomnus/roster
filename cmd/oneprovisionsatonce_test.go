package cmd_test

import (
	"sync"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestTwoConnectionsAreNotMarkedAtOnce.
//
// The rule is *a tenant's directory provisions through one connection*
// (`TestATenantProvisionsThroughOneConnection`). It was a read and then a
// write: the layer listed the tenant's connections, found none marked, and
// handed the write on. Two connections marked at the same moment each found
// none, and both stayed marked -- after which roster and the endpoint
// disagreed which is the one, and every create by the directory was refused.
// Thirty rounds, on SQLite, which serialises writers and so only leaves the
// gap between the read and the write: twenty-three and twenty-seven left both
// marked, on two runs.
//
// It is a count of a tenant's rows, held the way *a tenant keeps somebody
// administering it* is (`server/core/only.go`): the count and the write in
// one transaction, after a write on the tenant's own row that both callers
// make first. So whichever is second counts what the first wrote, and is
// told the rule in the rule's words.
func TestTwoConnectionsAreNotMarkedAtOnce(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	names := []string{"okta", "onelogin"}
	for _, name := range names {
		_, err := b.Walled.Connection().Add(admin, app.ConnectionAddRequest_builder{
			Tenant: at, Name: name, Issuer: "https://" + name + ".contoso.example", ClientId: "the-app",
		}.Build())
		x.NoError(err)
	}
	mark := func(name string, provisions bool) error {
		v := b.connectionOf(t, ctx, b.Contoso, name)
		_, err := b.Walled.Connection().Update(admin, app.ConnectionUpdateRequest_builder{
			Ref:         app.ConnectionRef_builder{Id: v.GetId()}.Build(),
			DateUpdated: v.GetDateUpdated(),
			Provisions:  z.Ptr(provisions),
		}.Build())

		return err
	}

	const rounds = 30

	both := 0
	for i := range rounds {
		for _, name := range names {
			x.NoError(mark(name, false))
		}

		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, len(names))
		for j, name := range names {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start

				errs[j] = mark(name, true)
			}()
		}
		close(start)
		wg.Wait()

		marked := 0
		for _, name := range names {
			if b.connectionOf(t, ctx, b.Contoso, name).GetProvisions() {
				marked++
			}
		}
		if marked > 1 {
			both++

			continue
		}
		x.Equal(1, marked, "round %d: neither was marked: %v, %v", i, errs[0], errs[1])

		// One was marked and one refused, and the one refused was told why.
		refused := errs[0]
		if refused == nil {
			refused = errs[1]
		}
		x.Equal(codes.FailedPrecondition, status.Code(refused), "round %d: %v", i, refused)
		x.ErrorContains(refused, "a directory provisions through one")
	}

	x.Zero(both, "%d of %d rounds left both connections marked", both, rounds)
}
