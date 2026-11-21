package login_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/login"
	rstr "github.com/lesomnus/roster/rstr"
)

// listening is a deployment's watcher, running until the test ends, and two
// people of contoso to speak down its stream with.
//
// The stream replays nothing and says nothing as it opens. So that it is open
// is learnt by poking until a poke lands (`open`), with one person; and that
// it has read past a write is learnt with a mark (`mark`), about the other,
// whose count is then exact. Events arrive in order on one stream, and what
// the app owes Hydra it carries in the order it heard it, so once a mark has
// reached Hydra everything heard before the mark has reached it too.
type listening struct {
	t *testing.T
	d *deployment
	h rstr.HolderServiceClient

	opener, marker pdid.Id
	marks          int
}

// listen starts the watcher, and stops it before the deployment under it goes.
func listen(t *testing.T, d *deployment) *listening {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		_ = d.a.Watch(ctx)
	}()

	return &listening{
		t: t, d: d, h: rstr.NewHolderServiceClient(d.conn),
		opener: addPerson(t, t.Context(), d, "opener"),
		marker: addPerson(t, t.Context(), d, "marker"),
	}
}

// open pokes until a poke lands: a stream is open, now.
func (l *listening) open() {
	l.t.Helper()

	was := l.d.hydra.times(l.opener.String())
	require.Eventually(l.t, func() bool {
		_, err := l.h.Invalidate(l.d.ops, rstr.HolderInvalidateRequest_builder{
			Ref: rstr.HolderRef_builder{Id: l.opener.Bytes()}.Build(),
		}.Build())
		require.NoError(l.t, err)

		return l.d.hydra.times(l.opener.String()) > was
	}, 20*time.Second, 100*time.Millisecond, "the stream never opened")
}

// mark is one event the stream is seen to have carried.
func (l *listening) mark() {
	l.t.Helper()

	_, err := l.h.Invalidate(l.d.ops, rstr.HolderInvalidateRequest_builder{
		Ref: rstr.HolderRef_builder{Id: l.marker.Bytes()}.Build(),
	}.Build())
	require.NoError(l.t, err)
	l.marks++
	require.Eventually(l.t, func() bool { return l.d.hydra.times(l.marker.String()) >= l.marks },
		10*time.Second, 20*time.Millisecond, "the stream stopped carrying anything")
}

// past is the stream having read past whatever was written before this was
// called. Three marks, because an event after a write is not the stream having
// read past it: what it took at once it reads in no order.
func (l *listening) past() {
	l.t.Helper()
	for range 3 {
		l.mark()
	}
}

// signedOut is `HolderService/Invalidate` over the wire, which is published.
func (l *listening) signedOut(who pdid.Id) {
	l.t.Helper()

	_, err := l.h.Invalidate(l.d.ops, rstr.HolderInvalidateRequest_builder{
		Ref: rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	require.NoError(l.t, err)
}

// renamed is a write to somebody's row that moves none of its stamps: a
// display name, over the wire, so that it is published. What a profile edit
// is, and what the `Fill` this app's own callback makes and a directory's sync
// are to a stream.
func (l *listening) renamed(who pdid.Id, name string) {
	l.t.Helper()
	x := require.New(l.t)

	ref := rstr.HolderRef_builder{Id: who.Bytes()}.Build()
	was, err := l.h.Get(l.d.ops, rstr.HolderGetRequest_builder{
		Ref:    ref,
		Select: rstr.HolderSelect_builder{DateUpdated: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	_, err = l.h.Update(l.d.ops, rstr.HolderUpdateRequest_builder{
		Ref:         ref,
		DateUpdated: was.GetDateUpdated(),
		Profile:     rstr.Profile_builder{DisplayName: name}.Build(),
	}.Build())
	x.NoError(err)
}

// signedOutAt is somebody signed out everywhere at a moment gone, written
// through the server in this process -- which publishes nothing, so no stream
// hears it. What is left of a sign-out made before a process was there to hear
// it is exactly this: the stamp on the row.
func signedOutAt(t *testing.T, d *deployment, who pdid.Id, at time.Time) {
	t.Helper()

	_, err := d.s.Ungated.Holder().Patch(t.Context(), rstr.HolderPatchRequest_builder{
		Ref:              rstr.HolderRef_builder{Id: who.Bytes()}.Build(),
		DateInvalidated:  timestamppb.New(at),
		DateUpdatedForce: proto.Bool(true),
	}.Build())
	require.NoError(t, err)
}

// TestASignOutActedOnLongAgoIsNotActedOnAgain: what the watcher had acted on
// was one connection's to remember, and roster's stream sends the facts as they
// stand the first time a connection hears of somebody. So after a restart or a
// dropped stream, the first write of any kind to a person's row carried the
// stamp of a sign-out made months before, and Hydra was told to forget them:
// every client's grant and the browser, a sign-in in progress included.
// Answered a display name with two DELETEs; right is none.
//
// A stamp first heard is news only when it is from a time this process may
// have missed, and one that moves is news whenever it does: the same person
// signed out everywhere now reaches Hydra, once.
func TestASignOutActedOnLongAgoIsNotActedOnAgain(t *testing.T) {
	x := require.New(t)
	d := serve(t)

	quiet := addPerson(t, t.Context(), d, "quiet")
	signedOutAt(t, d, quiet, time.Now().Add(-time.Hour))

	l := listen(t, d)
	l.open()

	l.renamed(quiet, "Quiet Person")
	l.past()
	x.Zero(d.hydra.times(quiet.String()), "hydra forgot a browser for a sign-out made an hour before this process started")
	x.NotContains(d.hydra.ungrants(), quiet.String(), "and every client's grant with it")

	l.signedOut(quiet)
	x.Eventually(func() bool { return d.hydra.times(quiet.String()) > 0 },
		10*time.Second, 20*time.Millisecond, "a sign-out made now did not reach hydra, of somebody whose old one was passed by")
	l.past()
	x.Equal(1, d.hydra.times(quiet.String()), "one sign-out is one forgetting")
	x.Contains(d.hydra.ungrants(), quiet.String())
}

// TestASignOutMadeWhileNobodyListenedIsCarried is the other side of the rule
// above, and what keeps it from being *a stamp first heard is never news*.
//
// A sign-out made while one instance was stopping and the next had not yet
// subscribed was heard by nobody: its stamp is all there is. So one from
// within the grace before this process started (`watchGrace`, ten minutes) is
// acted on when it is first heard, which is when their row is next written.
func TestASignOutMadeWhileNobodyListenedIsCarried(t *testing.T) {
	x := require.New(t)
	d := serve(t)

	missed := addPerson(t, t.Context(), d, "missed")
	signedOutAt(t, d, missed, time.Now().Add(-5*time.Minute))

	l := listen(t, d)
	l.open()

	l.renamed(missed, "Missed Person")
	x.Eventually(func() bool { return d.hydra.times(missed.String()) > 0 },
		10*time.Second, 20*time.Millisecond, "a sign-out made five minutes before this process started was passed by")
	x.Contains(d.hydra.ungrants(), missed.String())
}

// hand is a hand on Hydra's admin API: what the app says to Hydra goes through
// it, in the order it says it, and a test can have Hydra refuse to forget
// somebody or take its time over it.
type hand struct {
	*httptest.Server

	mu    sync.Mutex
	down  map[string]bool          // subject -> forgetting them answers 503
	hold  map[string]chan struct{} // subject -> forgetting them waits for this
	tried map[string]int           // subject -> how often forgetting them was asked
	told  []string                 // what reached Hydra, in the order it did
}

// onHydra puts a hand between the app being configured and its Hydra.
func onHydra(t *testing.T, c *login.Config) *hand {
	t.Helper()

	to, err := url.Parse(c.Hydra)
	require.NoError(t, err)
	next := httputil.NewSingleHostReverseProxy(to)

	h := &hand{down: map[string]bool{}, hold: map[string]chan struct{}{}, tried: map[string]int{}}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			who := r.URL.Query().Get("subject")

			h.mu.Lock()
			h.tried[who]++
			down, hold := h.down[who], h.hold[who]
			h.mu.Unlock()

			if hold != nil {
				select {
				case <-hold:
				case <-r.Context().Done():
					return
				}
			}
			if down {
				http.Error(w, "down", http.StatusServiceUnavailable)

				return
			}
			h.say("forget " + r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:] + " " + who)

		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/login/accept"):
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(b))

			var body struct {
				Subject string `json:"subject"`
			}
			_ = json.Unmarshal(b, &body)
			h.say("accept " + body.Subject)
		}

		next.ServeHTTP(w, r)
	}))
	t.Cleanup(h.Close)
	c.Hydra = h.URL

	return h
}

func (h *hand) say(v string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.told = append(h.told, v)
}

func (h *hand) said() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return slices.Clone(h.told)
}

func (h *hand) asked(subject string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.tried[subject]
}

func (h *hand) refuses(subject string, down bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.down[subject] = down
}

// holds has Hydra take its time over forgetting somebody, until the channel it
// answers with is closed.
func (h *hand) holds(subject string) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hold[subject] = make(chan struct{})

	return h.hold[subject]
}

// TestASignOutHydraWouldNotTakeIsTriedUntilItDoes: a revocation Hydra refused
// was logged and left. The comment beside it called the reconnect *the retry
// this design has*, and a reconnect replays nothing -- so a Hydra that was
// restarting when somebody was signed out everywhere went on remembering them,
// and a product's refresh token went on refreshing, until something else
// happened to move their stamp. Tried once; right is until Hydra takes it.
func TestASignOutHydraWouldNotTakeIsTriedUntilItDoes(t *testing.T) {
	x := require.New(t)
	var h *hand
	d := serveAs(t, login.Skip, func(c *login.Config) { h = onHydra(t, c) })
	erin := d.who["contoso"]

	l := listen(t, d)
	l.open()

	h.refuses(erin.String(), true)
	l.signedOut(erin)
	x.Eventually(func() bool { return h.asked(erin.String()) > 0 },
		10*time.Second, 20*time.Millisecond, "the app never tried")

	// Hydra answers again, and nothing more is said about her: no event, no
	// stream opened again. What was owed is what carries it.
	h.refuses(erin.String(), false)
	x.Eventually(func() bool { return d.hydra.times(erin.String()) > 0 },
		10*time.Second, 20*time.Millisecond, "hydra answered again and was never told to forget her")

	// Asked until it is so, and not once the browser is forgotten. A try is
	// two requests, her grants and then her browser, and Hydra began answering
	// again wherever the app was in one: where that was between the two, the
	// browser's went through on a try whose first had been refused, and her
	// grants went with the try after it. Read at that moment, this said her
	// grants had outlived the sign-out, five runs in a hundred on a loaded
	// machine, of an app that was a second from ending them.
	x.Eventually(func() bool { return slices.Contains(d.hydra.ungrants(), erin.String()) },
		10*time.Second, 20*time.Millisecond, "her grants outlived the sign-out: a refresh token goes on refreshing")
}

// wire is the connection between the app and roster, which a test can cut as a
// network does.
type wire struct {
	net.Listener
	to string

	mu   sync.Mutex
	open []net.Conn
}

// onRoster puts a wire between the app being configured and its roster.
func onRoster(t *testing.T, c *login.Config) *wire {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	w := &wire{Listener: l, to: c.Roster}
	t.Cleanup(func() {
		_ = l.Close()
		w.cut()
	})
	go func() {
		for {
			in, err := l.Accept()
			if err != nil {
				return
			}
			out, err := net.Dial("tcp", w.to)
			if err != nil {
				_ = in.Close()

				continue
			}

			w.mu.Lock()
			w.open = append(w.open, in, out)
			w.mu.Unlock()

			go func() { _, _ = io.Copy(out, in); _ = out.Close() }()
			go func() { _, _ = io.Copy(in, out); _ = in.Close() }()
		}
	}()
	c.Roster = l.Addr().String()

	return w
}

// cut drops every connection there is. The next one is carried.
func (w *wire) cut() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.open {
		_ = c.Close()
	}
	w.open = nil
}

// TestAStreamThatCameBackSignsNobodyOutAgain: a stream that drops and is opened
// again is a new connection to roster, which has told it nothing yet -- so the
// next write to anybody's row is sent with the facts as they stand, a sign-out
// this app carried an hour ago among them. Kept per connection, what the app
// had acted on went with the stream, and she was signed out a second time for
// a display name. Answered two more DELETEs; right is none.
func TestAStreamThatCameBackSignsNobodyOutAgain(t *testing.T) {
	x := require.New(t)
	var w *wire
	d := serveAs(t, login.Skip, func(c *login.Config) { w = onRoster(t, c) })
	erin := d.who["contoso"]

	l := listen(t, d)
	l.open()

	l.signedOut(erin)
	x.Eventually(func() bool { return d.hydra.times(erin.String()) > 0 },
		10*time.Second, 20*time.Millisecond, "hydra was never told to forget her")
	l.past()
	was := d.hydra.times(erin.String())

	w.cut()
	l.open()

	l.renamed(erin, "Erin Again")
	l.past()
	x.Equal(was, d.hydra.times(erin.String()), "she was signed out again because a stream came back")
}

// said is what the app logged, kept where a test can read it while the app is
// still writing.
type said struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *said) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.Write(p)
}

func (s *said) count(v string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return strings.Count(s.b.String(), v)
}

// logged is the default logger's lines until the test ends, which is where
// this app says what became of its stream.
func logged(t *testing.T) *said {
	t.Helper()

	// `slog.SetDefault` points the `log` package at the handler it is given
	// too, and putting the old logger back does not undo that.
	was, out, flags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(was)
		log.SetOutput(out)
		log.SetFlags(flags)
	})

	s := &said{}
	slog.SetDefault(slog.New(slog.NewTextHandler(s, nil)))

	return s
}

// TestAStreamRosterRefusesIsAskedForAgain: a key whose list does not name
// `SyncService/Watch` is refused the stream, and a roster with no broker has
// none to serve. Either was said once and the watcher returned -- for the life
// of the process, while the listener went on answering: the role put right an
// hour later was noticed at the next restart, and until then nobody signed out
// in roster was forgotten by Hydra. Asked once; right is again, every ten
// minutes, as `standing.Follow` asks.
//
// And said when it begins and when it ends, not at every ask between: a line
// every ten minutes is a line nobody reads by the second day.
func TestAStreamRosterRefusesIsAskedForAgain(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	d := serveAs(t, login.Skip, func(c *login.Config) { c.Refused = 100 * time.Millisecond })
	lines := logged(t)

	// The app's key, with the stream taken off its list and then put back.
	keys, err := d.s.Control.Ungated.ApiKey().List(ctx, rstr.ApiKeyListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(keys.GetItems(), 1, "the control plane holds the app's key and no other")
	may := func(methods ...string) {
		t.Helper()
		_, err := d.s.Control.Ungated.ApiKey().Patch(ctx, rstr.ApiKeyPatchRequest_builder{
			Ref:              rstr.ApiKeyRef_builder{Id: keys.GetItems()[0].GetId()}.Build(),
			Methods:          methods,
			DateUpdatedForce: proto.Bool(true),
		}.Build())
		x.NoError(err)
	}
	may(rstr.FrontService_WhoseHost_FullMethodName, rstr.TenantService_Get_FullMethodName)

	const refused, again = "roster will not say when somebody is signed out", "roster serves the sync stream again"

	l := listen(t, d)
	x.Eventually(func() bool { return lines.count(refused) > 0 },
		10*time.Second, 20*time.Millisecond, "a refused stream was not said")

	// Asked for several times more, and refused each time.
	time.Sleep(time.Second)
	x.Equal(1, lines.count(refused), "a stream still refused was said again")
	x.Zero(lines.count(again))

	may(rstr.FrontService_WhoseHost_FullMethodName, rstr.TenantService_Get_FullMethodName, rstr.SyncService_Watch_FullMethodName)
	l.open()
	x.Eventually(func() bool { return lines.count(again) > 0 },
		10*time.Second, 20*time.Millisecond, "a stream served again was not said")
	x.Equal(1, lines.count(again))
	x.Equal(1, lines.count(refused))
}

// TestASignInIsToldToHydraAfterTheForgettingItFollows: Hydra is told to forget
// somebody by subject -- every grant, every browser -- and nothing ordered that
// against the same person signing in again. Somebody who signs in straight
// after *sign out everywhere* answers the form while the forgetting is still on
// its way: Hydra is told who, and then the forgetting lands and takes the
// sign-in just made -- on Hydra v26.2.0 the code their product was handed
// answers `invalid_grant`. Hydra heard the sign-in first; right is the
// forgetting, then the sign-in.
//
// So a sign-in for somebody Hydra is still owed a forgetting about waits for
// it, where every one of them is told to Hydra.
func TestASignInIsToldToHydraAfterTheForgettingItFollows(t *testing.T) {
	x := require.New(t)
	var h *hand
	d := serveAs(t, login.Skip, func(c *login.Config) { h = onHydra(t, c) })
	erin := d.who["contoso"]
	b := d.browser(t)

	l := listen(t, d)
	l.open()

	// Signed out everywhere, heard, and on its way to a Hydra that is taking
	// its time over it.
	release := h.holds(erin.String())
	l.signedOut(erin)
	x.Eventually(func() bool { return h.asked(erin.String()) > 0 },
		10*time.Second, 20*time.Millisecond, "the app never tried")

	// And she signs in again at once, which roster takes: this sign-in is
	// after the sign-out. Hydra lets go a moment into it.
	time.AfterFunc(500*time.Millisecond, func() { close(release) })
	d.hydra.raise("c1", "contoso-web")
	_, code := d.signIn(t, b, "c1", "erin", password)
	x.Equal(http.StatusOK, code)

	x.Eventually(func() bool { return d.hydra.times(erin.String()) > 0 },
		10*time.Second, 20*time.Millisecond, "hydra was never told to forget her")
	x.Equal([]string{
		"forget consent " + erin.String(),
		"forget login " + erin.String(),
		"accept " + erin.String(),
	}, slices.DeleteFunc(h.said(), func(v string) bool { return !strings.HasSuffix(v, erin.String()) }),
		"hydra was told she signed in before it had forgotten her, and forgot the sign-in with her")
}
