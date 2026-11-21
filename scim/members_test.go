package scim_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
)

const (
	hankOid = "3c4d5e6f-7081-92a3-b4c5-d6e7f8091a2b"
)

// list is a list of members as a directory writes one.
func list(ids ...string) []map[string]any {
	vs := []map[string]any{}
	for _, v := range ids {
		vs = append(vs, map[string]any{"value": v})
	}

	return vs
}

// patchOf is a PatchOp of the operations given.
func patchOf(ops ...map[string]any) map[string]any {
	return map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": ops}
}

// places is whose a place in a group is, by their id at the endpoint, as roster
// has it: every membership, read whether or not its holder can still be.
func (d *deployment) places(t *testing.T, group string) []string {
	t.Helper()
	k, err := pdid.Parse(group)
	require.NoError(t, err)

	out := []string{}
	for after := ""; ; {
		vs, err := d.s.Ungated.GroupMembership().List(t.Context(), rstr.GroupMembershipListRequest_builder{
			Filters: []*rstr.GroupMembershipFilter{rstr.GroupMembershipFilter_builder{
				Group: rstr.GroupRef_builder{Id: k.Bytes()}.Build(),
			}.Build()},
			After: after,
		}.Build())
		require.NoError(t, err)
		for _, v := range vs.GetItems() {
			who, err := pdid.From(v.GetHolder().GetId())
			require.NoError(t, err)
			out = append(out, who.String())
		}
		if after = vs.GetNext(); after == "" {
			return out
		}
	}
}

// answered is who the endpoint says is in a group.
func (d *deployment) answered(t *testing.T, group string) []string {
	t.Helper()
	code, got := d.group(t, http.MethodGet, "/"+group, nil)
	require.Equal(t, http.StatusOK, code, "%v", got)

	out := []string{}
	ms, _ := got["members"].([]any)
	for _, m := range ms {
		out = append(out, m.(map[string]any)["value"].(string))
	}

	return out
}

// erase is an operator erasing somebody, at the box.
func (d *deployment) erase(t *testing.T, id string) pdid.Id {
	t.Helper()
	k, err := pdid.Parse(id)
	require.NoError(t, err)
	_, err = d.s.Ungated.Holder().Erase(t.Context(), rstr.HolderRef_builder{Id: k.Bytes()}.Build())
	require.NoError(t, err)

	return k
}

// TestARefusedListOfMembersChangesNothing: a list of members that names
// somebody the endpoint does not answer is refused whole, and the group is as
// it was.
//
// It was half written. An `add` was read and written a member at a time, so
// `[erin, somebody erased, fred]` was `400` with erin in and fred not, every
// time it was sent; a whole list took out everybody it did not name before it
// put anybody in, so `[somebody erased, fred]` over a group of two was `400`
// and the group was empty. A directory sends the same list again at its next
// cycle and is refused again, so what a refusal leaves is what the group is
// until somebody reads the log. Every id is resolved before the first write.
func TestARefusedListOfMembersChangesNothing(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	erin, fred, gina := d.someone(t, "erin", erinOid), d.someone(t, "fred", fredOid), d.someone(t, "gina", ginaOid)
	hank := d.someone(t, "hank", hankOid)
	d.erase(t, hank)

	code, got := d.group(t, http.MethodPost, "", map[string]any{"displayName": "Ops Team", "externalId": opsOid})
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)

	code, got = d.group(t, http.MethodPatch, "/"+id, patchOf(map[string]any{"op": "add", "path": "members", "value": list(erin, hank, fred)}))
	x.Equal(http.StatusBadRequest, code, "%v", got)
	x.Equal("invalidValue", got["scimType"])
	x.Equal(map[string]string{"scim": "group-manager"}, d.membersOf(t, id), "an add refused puts nobody in")

	code, got = d.group(t, http.MethodPatch, "/"+id, patchOf(map[string]any{"op": "add", "path": "members", "value": list(erin, gina)}))
	x.Equal(http.StatusNoContent, code, "%v", got)

	code, got = d.group(t, http.MethodPut, "/"+id, map[string]any{"displayName": "Ops Team", "members": list(hank, fred)})
	x.Equal(http.StatusBadRequest, code, "%v", got)
	x.Equal("invalidValue", got["scimType"])
	x.Equal(map[string]string{"scim": "group-manager", "erin": "", "gina": ""}, d.membersOf(t, id), "a whole list refused takes nobody out")

	code, got = d.group(t, http.MethodPatch, "/"+id, patchOf(map[string]any{"op": "replace", "path": "members", "value": list(hank, fred)}))
	x.Equal(http.StatusBadRequest, code, "%v", got)
	x.Equal(map[string]string{"scim": "group-manager", "erin": "", "gina": ""}, d.membersOf(t, id), "nor does a replace")
}

// TestMembersNamesWhomTheEndpointAnswers: a group's `members` names the people
// the endpoint answers, by the question `GET /Users/{id}` asks.
//
// It named every plain membership's holder. Somebody erased since, and
// somebody the directory itself deleted, were among `members` while a read of
// each was `404`: a directory that follows a reference finds nobody, and one
// that compares the list with its own sees somebody it does not have. Their
// place in roster is left as an erase and a delete leave it; it is not spoken
// of.
func TestMembersNamesWhomTheEndpointAnswers(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	erin, fred, gina := d.someone(t, "erin", erinOid), d.someone(t, "fred", fredOid), d.someone(t, "gina", ginaOid)
	code, got := d.group(t, http.MethodPost, "", map[string]any{
		"displayName": "Ops Team", "externalId": opsOid, "members": list(erin, fred, gina),
	})
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)

	code, _, _ = d.call(t, d.key, http.MethodDelete, "/scim/v2/Users/"+erin, nil)
	x.Equal(http.StatusNoContent, code)
	d.erase(t, fred)
	for _, who := range []string{erin, fred} {
		code, _, _ = d.call(t, d.key, http.MethodGet, "/scim/v2/Users/"+who, nil)
		x.Equal(http.StatusNotFound, code)
	}

	x.Equal([]string{gina}, d.answered(t, id))
	code, got = d.group(t, http.MethodGet, "", nil)
	x.Equal(http.StatusOK, code, "%v", got)
	x.Len(got["Resources"], 1)
	x.Len(got["Resources"].([]any)[0].(map[string]any)["members"], 1, "and in a list of groups")

	x.ElementsMatch([]string{erin, fred, gina, d.self(t)}, d.places(t, id), "their places are as an erase and a delete leave them")
}

// TestSomebodyErasedIsTakenOutOfAGroup: taking somebody out of a group is by
// their place in it, and so reaches somebody who cannot be read.
//
// It went by the person, and read *nobody by that id* as nobody to take out: a
// `remove` of somebody erased since was `204` and their place stayed, as it did
// through a whole list without them. A restore then gave them the place back,
// which the directory had taken away twice.
func TestSomebodyErasedIsTakenOutOfAGroup(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	ctx := t.Context()

	erin, fred, gina := d.someone(t, "erin", erinOid), d.someone(t, "fred", fredOid), d.someone(t, "gina", ginaOid)
	code, got := d.group(t, http.MethodPost, "", map[string]any{
		"displayName": "Ops Team", "externalId": opsOid, "members": list(erin, fred, gina),
	})
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)
	gone := []pdid.Id{d.erase(t, erin), d.erase(t, fred)}

	code, got = d.group(t, http.MethodPatch, "/"+id, patchOf(map[string]any{"op": "remove", "path": "members", "value": list(erin)}))
	x.Equal(http.StatusNoContent, code, "%v", got)
	x.NotContains(d.places(t, id), erin, "by a remove")

	code, got = d.group(t, http.MethodPut, "/"+id, map[string]any{"displayName": "Ops Team", "members": list(gina)})
	x.Equal(http.StatusOK, code, "%v", got)
	x.NotContains(d.places(t, id), fred, "by a whole list without them")

	for _, k := range gone {
		x.NoError(forget.Restore(ctx, d.s.Ent, k, audit.Contents{}, nil))
	}
	x.ElementsMatch([]string{gina, d.self(t)}, d.places(t, id), "restored, and in no group the directory took them out of")
	x.Equal([]string{gina}, d.answered(t, id))
}

// self is the directory's own holder, by its id.
func (d *deployment) self(t *testing.T) string {
	t.Helper()
	v, err := d.s.Ungated.Holder().Get(t.Context(), rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Slug: rstr.HolderRefBySlug_builder{
			Alias: proto.String("scim"), Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	require.NoError(t, err)
	k, err := pdid.From(v.GetId())
	require.NoError(t, err)

	return k.String()
}
