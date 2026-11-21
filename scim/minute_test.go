package scim_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// unmark takes `provisions` off contoso's connection.
func (d *deployment) unmark(t *testing.T) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()
	at := rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build()

	conn, err := d.s.Ungated.Connection().Get(ctx, rstr.ConnectionGetRequest_builder{
		Ref:    rstr.ConnectionRef_builder{At: rstr.ConnectionRefByAt_builder{Tenant: at, Name: proto.String("entra")}.Build()}.Build(),
		Select: rstr.ConnectionSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	x.NoError(err)
	_, err = d.s.Ungated.Connection().Patch(ctx, rstr.ConnectionPatchRequest_builder{
		Ref: rstr.ConnectionRef_builder{Id: conn.GetId()}.Build(), Provisions: proto.Bool(false), DateUpdated: conn.GetDateUpdated(),
	}.Build())
	x.NoError(err)
}

// TestAWriteAsksAgainWhichConnectionProvisions: with `provisions` taken off
// the connection, every write is `409` at once, at an endpoint that answered
// the key a moment before as much as at one that never saw it.
//
// Which tenant a key is and which connection provisions were remembered for a
// minute, and only making, suspending and telling asked roster about the mark
// again: a rename, a number, and a group made, renamed and deleted landed for
// that minute, where the manual says a write is refused at once. The minute
// is for reads. A request that is not a read asks again, and what it learns
// is remembered for the reads after it.
func TestAWriteAsksAgainWhichConnectionProvisions(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	erinId := d.someone(t, "erin", erinOid)
	code, g := d.group(t, http.MethodPost, "", map[string]any{"displayName": "Ops", "externalId": opsOid})
	x.Equal(http.StatusCreated, code, "%v", g)
	gid := g["id"].(string)

	d.unmark(t)

	patch := func(path string, value any) map[string]any {
		return map[string]any{
			"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
			"Operations": []map[string]any{{"op": "replace", "path": path, "value": value}},
		}
	}
	for _, tc := range []struct {
		desc, method, path string
		body               any
	}{
		{"a rename", http.MethodPatch, "/Users/" + erinId, patch("displayName", "Erin Written After")},
		{"a number", http.MethodPatch, "/Users/" + erinId, patch(`phoneNumbers[type eq "work"].value`, "+82 2 1111 1111")},
		{"a group made", http.MethodPost, "/Groups", map[string]any{"displayName": "Made After", "externalId": guid(t)}},
		{"a group renamed", http.MethodPatch, "/Groups/" + gid, patch("displayName", "Ops Renamed After")},
		{"a group deleted", http.MethodDelete, "/Groups/" + gid, nil},
	} {
		code, got, _ := d.call(t, d.key, tc.method, scim.Base+tc.path, tc.body)
		x.Equal(http.StatusConflict, code, "%s: %v", tc.desc, got)
	}

	h := d.holder(t, erinId)
	x.NotEqual("Erin Written After", h.GetProfile().GetDisplayName())
	x.Empty(d.numbersOf(t, erinId))
	code, g = d.group(t, http.MethodGet, "/"+gid, nil)
	x.Equal(http.StatusOK, code, "%v", g)
	x.Equal("Ops", g["displayName"], "a group's read is still answered within the minute, and was not renamed")
}

// TestAReadInsideTheMinuteAsksRosterNothingAboutTheKey: what the minute is
// for. A read of a key the endpoint answered within it asks roster neither
// who the key is nor which connection provisions; a write asks both.
func TestAReadInsideTheMinuteAsksRosterNothingAboutTheKey(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	erinId := d.someone(t, "erin", erinOid)

	g := newGate()
	base := d.gated(t, g)
	me := "/" + rstr.MeService_ServiceDesc.ServiceName + "/Get"
	conns := "/" + rstr.ConnectionService_ServiceDesc.ServiceName + "/List"

	x.Equal(http.StatusOK, ask(base, d.key, http.MethodGet, scim.Base+"/Users/"+erinId))
	x.Equal(1, g.asked(me), "the first read asks who the key is")
	g.asked(conns)

	x.Equal(http.StatusOK, ask(base, d.key, http.MethodGet, scim.Base+"/Users/"+erinId))
	x.Equal(http.StatusOK, ask(base, d.key, http.MethodGet, scim.Base+"/Users"))
	x.Zero(g.asked(me), "a read within the minute")
	x.Zero(g.asked(conns), "a read within the minute")

	x.Equal(http.StatusNoContent, ask(base, d.key, http.MethodDelete, scim.Base+"/Users/"+erinId))
	x.Equal(1, g.asked(me), "a write asks again")
	x.Equal(1, g.asked(conns), "a write asks again")
}
