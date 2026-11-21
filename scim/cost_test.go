package scim_test

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// tally is the calls roster was asked, by method.
type tally struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *tally) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	c.mu.Lock()
	c.n[strings.TrimPrefix(info.FullMethodName, "/roster.")]++
	c.mu.Unlock()

	return ctx
}
func (c *tally) HandleRPC(context.Context, stats.RPCStats)                         {}
func (c *tally) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context { return ctx }
func (c *tally) HandleConn(context.Context, stats.ConnStats)                       {}

// since is the calls asked since the last time it was read.
func (c *tally) since() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.n
	c.n = map[string]int{}

	return out
}

func sum(n map[string]int) int {
	total := 0
	for _, v := range n {
		total += v
	}

	return total
}

func tallied(n map[string]int) string {
	keys := make([]string, 0, len(n))
	for k := range n {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, n[k]))
	}

	return fmt.Sprintf("%d [%s]", sum(n), strings.Join(parts, " "))
}

// counted is a second endpoint, in front of a second listener of the same
// roster that counts what it is asked.
func (d *deployment) counted(t *testing.T) (string, *tally) {
	t.Helper()
	c := &tally{n: map[string]int{}}
	g, err := d.s.Grpc(t.Context(), cmd.Config{}, grpc.StatsHandler(c))
	require.NoError(t, err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(g.Stop)

	e, err := scim.New(scim.Config{Roster: l.Addr().String(), Insecure: true, Log: slog.New(slog.NewTextHandler(&said{}, nil))})
	require.NoError(t, err)
	t.Cleanup(func() { e.Close() })
	front := httptest.NewServer(e)
	t.Cleanup(front.Close)

	return front.URL, c
}

// recorded is the rows the access record has gained since it was last read,
// by method.
func (d *deployment) recorded(t *testing.T, seen *int) map[string]int {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, d.s.Access.Flush(ctx))
	rs, err := d.s.Ent.Access.Query().All(ctx)
	require.NoError(t, err)
	sort.Slice(rs, func(i, j int) bool { return rs[i].DateCreated.Before(rs[j].DateCreated) })

	rows := map[string]int{}
	for _, r := range rs[min(*seen, len(rs)):] {
		rows[strings.TrimPrefix(r.Action, "/roster.")]++
	}
	*seen = len(rs)

	return rows
}

// TestWhatAnUnfilteredListCosts: an unfiltered list counts the people the
// directory provisions by one call a person that reads nothing personal, and
// so leaves no row of the access record for any of them -- what is on the
// record is the pages of their accounts at the connection, a row a page, as
// it was; and one person named many times in a list of members is asked
// about once.
//
// It read each person whole -- the row and their two lists, three calls and
// three rows of the access record -- to decide whether they were counted, on
// every page: among 300, a page of a hundred was 1,415 calls and 1,265 rows,
// and `count=0` 915 and 915. One person named a thousand times in `members`
// was 4,001 calls. A read of a group's members left a row for each member.
//
// The bounds below are of what it costs now, with room; they are not the
// exact figures, which the next change to a select would move.
func TestWhatAnUnfilteredListCosts(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	bound := assert.New(t) // so that every figure is seen where one is past its bound
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	const crowd = 200
	ids := make([]string, 0, crowd)
	for i := range crowd {
		h, err := d.s.Ungated.Holder().Provision(ctx, rstr.HolderProvisionRequest_builder{
			Tenant: at, Alias: fmt.Sprintf("c%05d", i), Name: fmt.Sprintf("Crowd %05d", i),
			Provider: "entra", Subject: guid(t), Address: fmt.Sprintf("c%05d@contoso.example", i),
		}.Build())
		x.NoError(err)
		k, err := pdid.From(h.GetId())
		x.NoError(err)
		ids = append(ids, k.String())
	}
	// And some who are not the directory's: entered by an operator.
	for i := range 20 {
		_, err := d.s.Ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{Tenant: at, Alias: fmt.Sprintf("o%05d", i)}.Build())
		x.NoError(err)
	}
	// The members of the group carry a label, which is personal: what a read
	// of a group would put on the record if it asked for labels.
	for _, id := range ids[:20] {
		_, err := d.s.Ungated.Holder().Patch(ctx, rstr.HolderPatchRequest_builder{
			Ref: rstr.HolderRef_builder{Id: mustId(t, id)}.Build(), Labels: map[string]string{"team": "twenty"}, DateUpdatedForce: proto.Bool(true),
		}.Build())
		x.NoError(err)
	}
	code, g := d.group(t, http.MethodPost, "", map[string]any{"displayName": "Twenty", "externalId": guid(t), "members": list(ids[:20]...)})
	x.Equal(http.StatusCreated, code, "%v", g)
	gid := g["id"].(string)

	front, calls := d.counted(t)
	seen := 0
	ask := func(what, method, path string, body any) (map[string]any, map[string]int, map[string]int) {
		t.Helper()
		d.recorded(t, &seen)
		calls.since()
		// The same deployment, asked at the endpoint that counts.
		at := *d
		at.scim = &httptest.Server{URL: front}
		code, got, _ := at.call(t, d.key, method, path, body)
		x.Less(code, 300, "%s: %v", what, got)
		n, rows := calls.since(), d.recorded(t, &seen)
		t.Logf("%-40s roster %s; access record %s", what, tallied(n), tallied(rows))

		return got, n, rows
	}
	ask("the key's first request", http.MethodGet, scim.Base+"/Users/"+ids[0], nil)

	got, n, rows := ask("count=0", http.MethodGet, scim.Base+"/Users?count=0", nil)
	x.EqualValues(crowd, got["totalResults"])
	bound.LessOrEqual(sum(n), crowd+crowd/10, "one call a person, and the pages of their accounts")
	bound.Equal(n["HolderService/Get"], crowd)
	bound.Zero(rows["HolderService/Get"], "and asking of a person is no row of the access record")
	bound.Equal(sum(rows), rows["IdentityService/List"], "the rows are the pages of accounts at the connection, a row a page as before")

	got, n, rows = ask("a page of ten", http.MethodGet, scim.Base+"/Users?count=10", nil)
	x.Len(got["Resources"], 10)
	bound.LessOrEqual(sum(n), crowd+10+10*6)
	bound.LessOrEqual(sum(rows), 10*4+crowd/10, "rows for the ten it shows, and none a person for the count")

	got, n, rows = ask("a page of a hundred", http.MethodGet, scim.Base+"/Users?count=100", nil)
	x.Len(got["Resources"], 100)
	x.EqualValues(crowd, got["totalResults"])
	bound.LessOrEqual(sum(n), crowd+10+100*6)
	bound.LessOrEqual(sum(rows), 100*4+crowd/10)

	same := []map[string]any{}
	for range 100 {
		same = append(same, map[string]any{"value": ids[60]})
	}
	_, n, _ = ask("one person a hundred times in members", http.MethodPatch, scim.Base+"/Groups/"+gid, patchOf(
		map[string]any{"op": "add", "path": "members", "value": same},
	))
	bound.LessOrEqual(sum(n), 8, "asked about once, and put in once")
	_, n, _ = ask("and again, who is in already", http.MethodPatch, scim.Base+"/Groups/"+gid, patchOf(
		map[string]any{"op": "add", "path": "members", "value": same},
	))
	bound.Zero(n["GroupMembershipService/Add"], "somebody already in is not added again")

	got, n, rows = ask("a group of twenty-one, with its members", http.MethodGet, scim.Base+"/Groups/"+gid, nil)
	x.Len(got["members"], 21)
	bound.LessOrEqual(sum(n), 21+6)
	bound.Zero(sum(rows), "who is in a group is read with nothing personal")
}

func mustId(t *testing.T, id string) []byte {
	t.Helper()
	k, err := pdid.Parse(id)
	require.NoError(t, err)

	return k.Bytes()
}
