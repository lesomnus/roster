package account_test

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/arrives"
	rstr "github.com/lesomnus/roster/rstr"
)

// aliases is what contoso's people are called, in order: who a sign-in made,
// and who it left behind.
func (d *deployment) aliases(t *testing.T) []string {
	t.Helper()

	vs, err := d.ungated.Holder().List(t.Context(), rstr.HolderListRequest_builder{
		Filters: []*rstr.HolderFilter{rstr.HolderFilter_builder{
			Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		}.Build()},
		Size: 100,
	}.Build())
	require.NoError(t, err)

	out := []string{}
	for _, v := range vs.GetItems() {
		out = append(out, v.GetAlias())
	}
	sort.Strings(out)

	return out
}

// TestAFirstSignInThatCannotBeLinkedMakesNobodyHereEither is the Login App's
// `TestAFirstSignInThatCannotBeLinkedMakesNobody` at the other front door,
// which enrols the same way (`arrives.Known`). Three tries through a
// connection whose subject is an address answered `500`, *cannot sign in*,
// three times, and left three people with no way in, `yun` and two called
// `yun-` and four letters nobody chose. The answer is the one it was, and
// nobody is made for it.
func TestAFirstSignInThatCannotBeLinkedMakesNobodyHereEither(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Enrolling())

	d.idp.Subject = "yun@contoso.com"
	d.idp.Claims = map[string]any{"email": "yun@contoso.com", "email_verified": true, "name": "Yun"}

	before := d.aliases(t)
	for i := range 3 {
		code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/login?connection=example", "", nil)
		x.Equal(http.StatusInternalServerError, code, "try %d: the connection's failure is this app's to answer: %s", i+1, body)
	}
	x.Equal(before, d.aliases(t), "a sign-in that was refused left somebody behind")

	// Mended, they arrive once, and by their own name.
	d.idp.Subject = "yun-at-the-directory"
	b := d.browser(t, "contoso.test")
	code, body := b.do(t, http.MethodGet, "/login?connection=example", "", nil)
	x.Equal(http.StatusOK, code, body)
	code, body = b.rpc(t, "/roster.MeService/Get", `{}`)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, `"yun"`)

	after := append(append([]string{}, before...), "yun")
	sort.Strings(after)
	x.Equal(after, d.aliases(t))
}

// TestSeveralArrivalsOfOneNewPersonAtOnceAreOnePerson: a first sign-in looks
// for the account, finds none, makes the person and links them -- a read and
// two writes, with nothing making them one. Arrivals of one new person at
// once each found none and each made somebody. One link landed and the others
// were refused as taken, which is the account of the very person arriving,
// written a moment ago: of four arrivals at once three were answered *cannot
// sign in*, and three people were left with a name nobody chose and no way
// in. Twenty people arriving so left sixty.
//
// At one front door the arrivals of one account take turns, so the second
// finds what the first made: every arrival is the one person, and nobody else
// is made.
//
// At two -- the Login App and the account app, or two replicas -- there is
// nothing to take turns on. The arrival that lost is refused as taken, looks
// again, and is answered as the person who has the account. Whoever it had
// made is left, with no way in, and said in the log for an administrator to
// erase: a front door erases nobody, since `HolderService/Erase` is not held
// to what the erased person holds, and a role that could take this one back
// would reach everybody in the tenant.
//
// Written as many people rather than one, because one pair racing is a coin
// toss. Asked of `arrives.Known` as this app asks it, with the key and the
// role the app has: the provider these tests sign in through issues one code
// at a time, so two round trips cannot be finished at once through it.
func TestSeveralArrivalsOfOneNewPersonAtOnceAreOnePerson(t *testing.T) {
	const people, arrivals = 20, 4

	// door is `arrives` as one process of this app holds it.
	door := func(t *testing.T, d *deployment) *arrives.Providers {
		t.Helper()

		conn, err := grpc.NewClient(d.grpc, grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })

		return arrives.New(rstr.NewClient(conn), nil)
	}

	// arrive is every person arriving `arrivals` times at once, each arrival
	// at the door `at` says, and answers how many arrivals were refused and
	// how many people were made beside the one who has the account.
	arrive := func(t *testing.T, d *deployment, at func(arrival int) *arrives.Providers) (refused, left int) {
		t.Helper()
		x := require.New(t)
		ctx := t.Context()
		as := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+d.tokens["contoso"])

		for i := range people {
			who := arrives.Caller{
				Tenant: d.contoso, TenantAlias: "contoso", Provider: "example",
				Subject: fmt.Sprintf("newcomer-%d-at-the-directory", i),
				Email:   fmt.Sprintf("newcomer-%d@contoso.com", i), Verified: true, Name: "New Comer",
			}

			var wg sync.WaitGroup
			start := make(chan struct{})
			ids, errs := make([]pdid.Id, arrivals), make([]error, arrivals)
			for j := range arrivals {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start

					arriving := who
					ids[j], errs[j] = at(j).Known(as, arrives.Enrolling(), &arriving)
				}()
			}
			close(start)
			wg.Wait()

			// The account is one row, and one person's.
			linked, err := d.ungated.Identity().Get(ctx, rstr.IdentityGetRequest_builder{
				Ref: rstr.IdentityRef_builder{Subject: rstr.IdentityRefBySubject_builder{
					TenantId: d.contoso.Bytes(), Provider: &who.Provider, Subject: &who.Subject,
				}.Build()}.Build(),
			}.Build())
			x.NoError(err)

			for j := range arrivals {
				if errs[j] != nil {
					refused++
					t.Logf("person %d, arrival %d: %v", i, j, errs[j])

					continue
				}
				x.Equal(linked.GetHolder().GetId(), ids[j].Bytes(),
					"person %d, arrival %d: answered as somebody who does not have the account", i, j)
			}

			// Whoever else was made is called what they are, and four
			// characters nobody chose.
			for _, alias := range d.aliases(t) {
				if strings.HasPrefix(alias, fmt.Sprintf("newcomer-%d-", i)) {
					left++
				}
			}
		}

		return refused, left
	}

	t.Run("at one front door, and nobody else is made", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Enrolling())
		one := door(t, d)

		refused, left := arrive(t, d, func(int) *arrives.Providers { return one })
		x.Zero(refused, "%d of %d arrivals were refused the account their own person had just been given", refused, people*arrivals)
		x.Zero(left, "%d people were made beside the %d who arrived, and left with no way in", left, people)
	})

	t.Run("at two, where whoever lost is told who they are", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Enrolling())
		doors := []*arrives.Providers{door(t, d), door(t, d)}

		refused, left := arrive(t, d, func(arrival int) *arrives.Providers { return doors[arrival%len(doors)] })
		x.Zero(refused, "%d of %d arrivals were refused the account their own person had just been given", refused, people*arrivals)

		// Not asserted, because it is not prevented: one for each arrival
		// that looked before the other door had written.
		t.Logf("%d people arriving at two front doors at once: %d others were made and left with no way in", people, left)
	})
}
