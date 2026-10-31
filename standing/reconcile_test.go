package standing_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/standing"
)

// refusing is roster unable to answer about some accounts, and answering about
// the rest.
func refusing(ids ...pdid.Id) grpc.DialOption {
	return grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, res any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if v, ok := req.(*rstr.HolderGetRequest); ok {
			for _, id := range ids {
				if string(v.GetRef().GetId()) == string(id.Bytes()) {
					return status.Error(codes.Unavailable, "connection refused")
				}
			}
		}
		return invoke(ctx, method, req, res, cc, opts...)
	})
}

// erase is roster's operator erasing somebody, which the reads leave out from
// then on.
func (e *external) erase(t *testing.T, who ...pdid.Id) {
	t.Helper()
	for _, v := range who {
		_, err := e.s.Ungated.Holder().Erase(t.Context(), rstr.HolderRef_builder{Id: v.Bytes()}.Build())
		require.NoError(t, err)
	}
}

// restore is `roster restore`: an erasure undone past roster's servers, where
// no stream is published from.
func (e *external) restore(t *testing.T, who pdid.Id) {
	t.Helper()
	cs, err := e.c.Audit.Contents()
	require.NoError(t, err)
	require.NoError(t, forget.Restore(t.Context(), e.s.Ent, who, cs))
}

// TestAReconcileFindsWhatTheStreamMissed: every account the app holds is asked
// about once, in the order given, and sorted against the app's marks -- an
// account roster has nobody by that is not marked is a mark the stream should
// have made, and one marked that roster has again is a mark to take back.
func TestAReconcileFindsWhatTheStreamMissed(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)

	lee := addPerson(t, e.s, e.acme, "lee")     // marked, and roster has them: restored
	quiet := addPerson(t, e.s, e.acme, "quiet") // not marked, and roster has nobody: missed
	gone := addPerson(t, e.s, e.acme, "gone")   // marked, and roster has nobody: as it was
	e.erase(t, quiet, gone)
	marks := map[pdid.Id]bool{lee: true, gone: true}

	a := accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")})
	r, err := a.Reconcile(ctx, []pdid.Id{e.kim, lee, quiet, gone, e.kim}, func(v pdid.Id) bool { return marks[v] })
	x.NoError(err)
	x.Equal([]pdid.Id{e.kim, lee, quiet, gone}, r.Asked, "each once, in the order given")
	x.Equal(2, r.Found)
	x.Equal([]pdid.Id{quiet}, r.Gone)
	x.Len(r.Back, 1)
	x.Equal(lee, r.Back[0].Holder)
	x.Equal(e.acme, r.Back[0].Tenant)
	x.True(r.Back[0].Erased.IsZero())
}

// TestAReconcileThatFindsNobodyIsNotBelieved: roster having none of an app's
// accounts is far more often the wrong directory than all of them leaving, so
// it is answered with what was found and refused -- here, the app asking in a
// tenant that nominated it and holds none of its people.
func TestAReconcileThatFindsNobodyIsNotBelieved(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	e := newExternal(t)
	lee := addPerson(t, e.s, e.acme, "lee")
	x.NoError(cli.NewCmdApp(&e.c).Run(ctx, []string{"install", "--tenant", "beta", "--role", strings.Join(methods, ","), app}))

	wrong := accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "beta")})
	r, err := wrong.Reconcile(ctx, []pdid.Id{e.kim, lee}, nil)
	var nobody *standing.NobodyError
	x.ErrorAs(err, &nobody)
	x.Equal(2, nobody.Accounts)
	x.NotNil(r, "what roster said, for a report")
	x.Equal(0, r.Found)
	x.Equal([]pdid.Id{e.kim, lee}, r.Gone)

	r, err = wrong.Reconcile(ctx, []pdid.Id{e.kim, lee}, func(pdid.Id) bool { return true })
	x.NoError(err, "nobody, and every one of them marked already: nothing to mark")
	x.Empty(r.Gone)
	x.Empty(r.Back)

	r, err = accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")}).Reconcile(ctx, []pdid.Id{e.kim, lee}, nil)
	x.NoError(err, "the right directory")
	x.Equal(2, r.Found)
	x.Empty(r.Gone)
}

// TestAReconcileThatCannotAskDecidesNothing: everything is asked before
// anything is answered, so a roster that stops answering half way answers no
// marks at all rather than the half it got to.
func TestAReconcileThatCannotAskDecidesNothing(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	quiet := addPerson(t, e.s, e.acme, "quiet")
	e.erase(t, quiet)

	a := accounts(t, standing.Config{Conn: e.dial(t, refusing(e.kim)), As: at(e.key, "acme")})
	r, err := a.Reconcile(t.Context(), []pdid.Id{quiet, e.kim}, nil)
	x.Nil(r)
	x.ErrorContains(err, "nothing was decided")
	x.Equal(codes.Unavailable, status.Code(err))
}

// TestAMarkIsActedOnOnlyOnceRosterIsAskedAgain is the rule restore and forget
// make: neither is on the stream, so an account marked as gone is asked about
// again before the mark is acted on. One roster still has nobody by is gone;
// one it has again was restored, and its mark is taken back; and about one
// roster cannot answer for, nothing is decided until the next look.
func TestAMarkIsActedOnOnlyOnceRosterIsAskedAgain(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	still := addPerson(t, e.s, e.acme, "still")
	back := addPerson(t, e.s, e.acme, "back")
	unanswered := addPerson(t, e.s, e.acme, "unanswered")
	failing := addPerson(t, e.s, e.acme, "failing")
	e.erase(t, still, back, unanswered, failing)
	e.restore(t, back)

	l := &logs{}
	a := accounts(t, standing.Config{
		Conn: e.dial(t, refusing(unanswered)), As: at(e.key, "acme"),
		Log: slog.New(slog.NewTextHandler(l, nil)),
	})
	var departed []pdid.Id
	var restored []standing.Standing
	err := a.Settle(t.Context(), []pdid.Id{still, back, unanswered, failing, still},
		func(_ context.Context, v pdid.Id) error {
			departed = append(departed, v)
			if v == failing {
				return errors.New("could not leave")
			}
			return nil
		},
		func(_ context.Context, v standing.Standing) error {
			restored = append(restored, v)
			return nil
		})
	x.ErrorContains(err, "could not leave")
	x.ErrorContains(err, failing.String())
	x.Equal([]pdid.Id{still, failing}, departed, "each once, and not the one roster could not answer for")
	x.Len(restored, 1)
	x.Equal(back, restored[0].Holder)
	x.True(restored[0].Erased.IsZero())
	x.Contains(l.String(), "asked again next time")
	x.Contains(l.String(), unanswered.String())
}

// TestARestoreIsNotHeardUntilSomethingElseIs is why: the stream is published
// from roster's servers, and a restore writes past them. So it is heard as the
// next event about the account that is not an erasure -- whenever there is
// one -- and not when it happens.
func TestARestoreIsNotHeardUntilSomethingElseIs(t *testing.T) {
	x := require.New(t)
	e := newExternal(t)
	got := follow(t, accounts(t, standing.Config{Conn: e.dial(t), As: at(e.key, "acme")}))
	op, as := e.asOperator(t)
	ref := rstr.HolderRef_builder{Id: e.kim.Bytes()}.Build()

	invalidate := func() error {
		_, err := op.Invalidate(as, rstr.HolderInvalidateRequest_builder{Ref: ref}.Build())
		return err
	}

	// Invalidated first, for the stream to be known to be open: an erasure is
	// made once, and one made before the stream is open is heard by nobody.
	heard(t, got, e.kim, invalidate, func(v standing.Standing) bool { return !v.Invalidated.IsZero() })
	_, err := op.Erase(as, ref)
	x.NoError(err)
	heard(t, got, e.kim, func() error { return nil }, func(v standing.Standing) bool { return !v.Erased.IsZero() })

	e.restore(t, e.kim)
	select {
	case v := <-got:
		t.Fatalf("a restore was heard: %+v", v)
	case <-time.After(time.Second):
	}

	v := heard(t, got, e.kim, invalidate, func(v standing.Standing) bool { return v.Erased.IsZero() })
	x.False(v.Invalidated.IsZero(), "the next thing heard of them, which says they are back")
}
