package cmd_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/frame"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// A continuation is single-use, and the whole of the second factor rests on it.
//
// `server/vouch/delegate.go` says so where it mints: *it is safe here because a
// continuation is single-use*. `step.go` says it where it spends: *single use,
// and used is not there*.
//
// It was not. Spending is an `Erase`, `Erase` answers `Empty`, and nothing
// downstream could tell whether **this** call was the one that did it -- so N
// callers presenting one continuation all read the live row, all compared, and
// all went on to mint.
//
// # Why no test caught it
//
// Because the suite runs on SQLite, where a second writer gets `database is
// locked` and dies, leaving exactly one winner. `pdtest.DB` is SQLite unless
// `PDTEST_POSTGRES` names a server -- and its own sibling comment warns about
// this: it is the direction that hides a mistake.

// TestOneContinuationMintsOneCredential.
//
// Run it against Postgres to mean anything:
//
//	PDTEST_POSTGRES=postgres://... go test ./cmd/ -run TestOneContinuationMints
//
// On SQLite it passes for the wrong reason, which is said here rather than
// discovered later.
func TestOneContinuationMintsOneCredential(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, delegate, "/roster.VouchService/Continue", listHolders)
	ctx := t.Context()

	mayList(t, ctx, b, b.Who, listHolders)

	v := b.keyed(t)
	seed := enrolled(t, ctx, v, b.Who)

	// Called directly rather than over the connection: a shared HTTP/2 stream
	// serialises enough that the window closes before anybody reaches it, and
	// what is being asked about is the database and not the transport.
	as := frame.Into(ctx, frame.New(b.Who, b.Acme, frame.Whole()).WithScope(frame.Only(b.Acme)))

	first, err := v.Delegate(as, app.VouchDelegateRequest_builder{
		Who:     app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
		Secret:  []byte("correct horse battery staple"),
		Methods: []string{listHolders},
	}.Build())
	x.NoError(err)

	handle := first.GetVerified().GetContinuation()
	x.NotEmpty(handle)

	// One code, so every caller is presenting the same correct second factor --
	// which is the shape a browser retrying, or somebody with the handle,
	// actually has.
	code := []byte(vouch.CodeAt(seed, time.Now().Unix()/30))

	const racers = 32

	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		mu    sync.Mutex
		won   []string
	)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			res, err := v.Delegate(as, app.VouchDelegateRequest_builder{
				Continuation: handle,
				Kind:         vouch.KindTotp,
				Secret:       code,
				Methods:      []string{listHolders},
			}.Build())
			if err != nil || res.GetToken() == "" {
				return
			}

			mu.Lock()
			won = append(won, res.GetToken())
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	// One. Not "at least one" and not "at most a few": each of these is an
	// independently revocable credential for that person, and revoking the one
	// somebody knows about leaves the others.
	x.Len(won, 1, "one continuation minted %d credentials", len(won))
}

// TestOneLinkMintsOneCredential is the same property one entity over.
//
// A link is a first factor with nothing behind it, so two winners here are two
// credentials from one mail -- and the interleaving is not exotic. A mail
// client that fetches a link to preview it, and a person who clicks it while
// that fetch is in flight, is the shape this is most likely to meet.
func TestOneLinkMintsOneCredential(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, link, redeem, listHolders)
	ctx := t.Context()

	mayList(t, ctx, b, b.Who, listHolders)

	v := b.keyed(t)
	as := frame.Into(ctx, frame.New(b.Who, b.Acme, frame.Whole()).WithScope(frame.Only(b.Acme)))

	made, err := v.Link(as, app.VouchLinkRequest_builder{
		Who: app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.NotEmpty(made.GetToken())

	const racers = 32

	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		mu    sync.Mutex
		won   []string
	)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			res, err := v.Redeem(as, app.VouchRedeemRequest_builder{
				Token:   made.GetToken(),
				Methods: []string{listHolders},
			}.Build())
			if err != nil || res.GetToken() == "" {
				return
			}

			mu.Lock()
			won = append(won, res.GetToken())
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	x.Len(won, 1, "one link minted %d credentials", len(won))
}
