// Package sync is the stream an app holds open to hear that a decision it made
// has stopped being good.
//
// # What it is, in one sentence
//
// A `Holder` watch with almost everything taken out. Every fact it carries is a
// column on that row, and item 4 says why an app should not watch the row
// instead: *a `Holder` changes for reasons nobody needs to hear about.*
//
// # Why it is not HolderService
//
// It reads `Holder` rows, so *Overlay before service* asks whether it is a
// `Holder.Sync` overlay rather than a service of its own -- and the answer is
// the sentence above. The generated `Watch` carries the whole row and every
// change to it, because that is what a watch is; this carries three columns and
// only when one of them moved, with a rename or a re-label swallowed. That is
// not a narrowing an overlay in front of the watch could add: the machinery
// keeps state *per person* to say which of the three facts changed, and sends
// nothing to somebody nothing has happened to. It is a projection with its own
// memory, not a verb on the row -- which is the one case *Overlay before
// service* leaves to a service, and the paragraph it requires so that a service
// which could not write it is the smell caught instead of shipped.
//
// # How it knows what changed, without being told
//
// It does not read the RPC, and could not usefully: `watch.Next` answers with
// the method the **call** was dispatched under, so a stamp moved by
// `HolderService/Disable` and one moved by `MeService/SignOutEverywhere` arrive
// under different names, and a write made on the way to somewhere else -- the
// holder that comes with a new tenant -- under a third. A table of those names
// is a table that is wrong the day somebody adds an RPC, and wrong silently.
//
// So it compares state. The three facts are columns; the stream keeps what it
// last sent for each person and can then say which of them moved. That is the
// model the watch machinery is built on -- *a stream of state gets cheaper when
// it falls behind, which a stream of deltas cannot* -- applied one level up:
// what is kept is not the row but the three stamps on it.
//
// # And the first time it hears about somebody
//
// It sends the facts, with no reason -- unless there are no facts, in which
// case it remembers and says nothing.
//
// The reason is missing because there is nothing to compare against and so no
// movement to name. The facts go anyway: they are what the event carries, they
// are true, and an app diffs them against the session it is holding, which it
// has to do regardless.
//
// The first draft sent **nothing** on first sight, to keep a rename of a
// long-suspended person from waking anybody. That was a hole rather than a
// nicety: a suspension landing just after an app connects is the first thing
// this stream hears about that person, and so was the one event it swallowed.
//
// What replaces it is narrower than "always send": somebody with all three
// stamps unset has had nothing happen to them, so no copy anybody holds can be
// wrong, and there is nothing to say. That keeps a rename silent -- which is
// the sentence the whole service is argued from -- and still carries the write
// that mattered.
//
// # And what it costs
//
// One entry per person this stream has heard about, for as long as it is held.
// Bounded by how many people change while an app is connected, which is the
// same bound the generated `Watch` has on its own `Seen`.
package sync

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/watch"

	app "github.com/lesomnus/roster/rstr"
)

// Service is `SyncService` over the server it is handed.
//
// The **walled** one, so what an app hears is narrowed exactly as a read is: a
// deployment key hears every tenant and a credential resolving to a person
// hears theirs. That is the whole of the narrowing and there is no field beside
// it -- two answers to *what may this caller see* is the shape of the bug
// rather than a convenience.
func Service(s app.Server, w *watch.Watch) app.SyncServiceServer {
	return &server{s: s, w: w}
}

type server struct {
	app.UnimplementedSyncServiceServer

	s app.Server
	w *watch.Watch
}

// held is the three stamps, as this stream last sent them.
//
// `time.Time` and not the message, because what is compared is the instant and
// keeping the message would keep a row this stream deliberately does not send.
// holderService is the prefix of every Rpc of that service, which is how a
// change is known to be about a Holder.
//
// Through `watch.ServiceOf` and not `ServiceDesc.ServiceName`, which is the
// same name without the slashes -- and a prefix test against `/roster.Holder…`
// that is missing the leading one matches nothing, on a stream that stays open
// and looks healthy.
var holderService = watch.ServiceOf(app.HolderService_Get_FullMethodName)

type held struct {
	stamps

	// tenant, kept for the one event that cannot read it: a row that is gone
	// answers no `Get`, and an app serving several customers still has to be
	// told which of its sessions to drop.
	tenant string
}

// stamps is the three facts, and is its own type so that `==` compares those
// and only those.
//
// A holder does not move between tenants, so the tenant beside them can never
// be the thing that changed -- and a struct that compared it would make the
// first sight of an ordinary person look like news.
type stamps struct {
	invalidated time.Time
	disabled    time.Time
	erased      time.Time
}

func (s *server) Watch(_ *app.SyncWatchRequest, out grpc.ServerStreamingServer[app.SyncEvent]) error {
	ctx := out.Context()
	was := map[pdid.Id]held{}

	// No snapshot, and `sync.proto` says why at length: a stream that ended is
	// a stream that missed things, and the honest answer to a reconnect is to
	// stop trusting what you hold rather than to ask this for a catch-up it
	// cannot give. `TokenService/Introspect` is the authority; this is the
	// optimisation that saves asking.
	//
	// It is also what keeps the subscription affordable. A snapshot here is
	// every holder this caller can see -- for a deployment key, every holder of
	// every tenant -- which is the read the generated `Watch` refuses to do
	// without filters, and this has none to give it.
	return watch.Stream(ctx, s.w, holderService, nil,
		func(ks map[pdid.Id]string, _ watch.Seen) error {
			for k := range ks {
				if err := s.one(ctx, out, was, k); err != nil {
					return err
				}
			}

			return nil
		})
}

// one reads a person and sends the three facts, if any of them moved.
//
// A read that answers `NotFound` is not an error here and is not silence
// either: the wall narrows this exactly as it narrows any read, so a person in
// a tenant this caller cannot see simply is not their business -- and a person
// whose row is gone is `NotFound` to the same read, which is a fact this stream
// exists to carry. The two are told apart by whether this stream had sent
// anything about them before.
func (s *server) one(
	ctx context.Context,
	out grpc.ServerStreamingServer[app.SyncEvent],
	was map[pdid.Id]held,
	k pdid.Id,
) error {
	v, err := s.s.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: k.Bytes()}.Build(),
		Select: app.HolderSelect_builder{
			Tenant:          app.TenantSelect_builder{}.Build(),
			DateInvalidated: ptr(true),
			DateDisabled:    ptr(true),
			DateErased:      ptr(true),
		}.Build(),
	}.Build())
	if err != nil {
		prior, told := was[k]
		if !told || !prior.erased.IsZero() {
			// Never visible to this caller, or already known to be gone.
			return nil
		}

		delete(was, k)

		// The disappearance **is** the fact, and the row that would have said
		// when is what disappeared -- so this is the one event stamped from
		// here rather than read.
		now := timestamppb.New(time.Now())

		return out.Send(app.SyncEvent_builder{
			Holder:          k.Bytes(),
			Tenant:          []byte(prior.tenant),
			Reason:          app.SyncReason_SYNC_REASON_ERASED,
			At:              now,
			DateInvalidated: stamp(prior.invalidated),
			DateErased:      now,
		}.Build())
	}

	now := held{
		stamps: stamps{
			invalidated: at(v.GetDateInvalidated()),
			disabled:    at(v.GetDateDisabled()),
			erased:      at(v.GetDateErased()),
		},
		tenant: string(v.GetTenant().GetId()),
	}

	prior, told := was[k]
	if told && now.stamps == prior.stamps {
		// The write was about something nobody needs to hear about, which is
		// most writes: a rename, a profile, a label. This is the sentence item
		// 4 argues the whole service from, and it is one comparison.
		return nil
	}

	was[k] = now

	if !told && now.stamps == (stamps{}) {
		// Never heard of them, and nothing has ever happened to them: no
		// session anybody holds can be wrong. Remembered rather than sent, so
		// that the next write is a comparison and not another first sight.
		return nil
	}

	return out.Send(app.SyncEvent_builder{
		Holder:          k.Bytes(),
		Tenant:          []byte(now.tenant),
		Reason:          why(prior.stamps, now.stamps, told),
		At:              timestamppb.New(time.Now()),
		DateInvalidated: v.GetDateInvalidated(),
		DateDisabled:    v.GetDateDisabled(),
		DateErased:      v.GetDateErased(),
	}.Build())
}

// why names the movement, when there was one to see.
//
// Read in the order somebody would: gone, then shut, then void. Two of them
// moving in one write is not a thing any RPC does, and if it ever were, the
// timestamps on the event are all there either way -- this only decides which
// word goes on it.
func why(prior, now stamps, told bool) app.SyncReason {
	switch {
	case !told:
		// See the package comment: the facts without the movement, which is
		// what every event is immediately after a reconnect.
		return app.SyncReason_SYNC_REASON_UNSPECIFIED

	case now.erased.After(prior.erased):
		return app.SyncReason_SYNC_REASON_ERASED

	case now.disabled.IsZero() != prior.disabled.IsZero():
		if now.disabled.IsZero() {
			return app.SyncReason_SYNC_REASON_REINSTATED
		}

		return app.SyncReason_SYNC_REASON_SUSPENDED

	case now.invalidated.After(prior.invalidated):
		return app.SyncReason_SYNC_REASON_INVALIDATED

	default:
		// Something moved -- the caller checked -- and it was none of the
		// three going forward. A stamp being cleared or wound back is the only
		// way here, and no RPC does it; the facts are on the event regardless.
		return app.SyncReason_SYNC_REASON_UNSPECIFIED
	}
}

func at(v *timestamppb.Timestamp) time.Time {
	if v == nil {
		return time.Time{}
	}

	return v.AsTime()
}

func stamp(v time.Time) *timestamppb.Timestamp {
	if v.IsZero() {
		return nil
	}

	return timestamppb.New(v)
}

func ptr[T any](v T) *T { return &v }
