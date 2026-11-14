package scim

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/arrives"
	rstr "github.com/lesomnus/roster/rstr"
)

// A directory's groups (#86): made by the directory's own holder, and so
// managed by it, the way anybody of a tenant manages a group they make
// (`server/core/group.go`).
//
// # Nothing here is a rule about a directory's group
//
// The key this endpoint forwards holds the writes that change a group -- who
// is in it, what it is called -- through the role its holder has **in** each
// group it made, and through no binding. So what it reaches is the groups it
// manages and no other, and that is roster's rule about every manager rather
// than one written for it. An administrator binds a role to such a group, or
// shows it to a client (`GroupExposure`), as to any other; who then holds
// that role is the directory's to say, and that is the administrator's
// decision when they do.
//
// # Which groups it speaks of
//
// The ones carrying [directoryLabel], which it wrote when it made them. A
// label and no more: no rule reads it. It is how the endpoint tells a group a
// directory made from one an operator did, so that a group already here under
// the same name is refused as taken rather than taken over.
//
// # And who is in one, as far as it says
//
// The plain members. Its own holder is in every group it made -- a manager is
// a member -- and is no member to the directory; and somebody an administrator
// appointed to manage one is theirs to take out, not the directory's
// (`GroupMembership.Erase` asks who could have appointed them).

// directoryLabel marks a group a directory made, and holds what the directory
// calls it: the `externalId`, which is Entra's `objectId`.
const directoryLabel = "roster.directory"

const groupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"

// byDisplayName is the other attribute a group is looked up by, beside
// [byExternalId]: Entra's, and Okta's.
const byDisplayName = "displayname"

// incomingGroup is a Group a directory sent whole: on a create, or a replace.
type incomingGroup struct {
	displayName string
	externalId  string

	// members is who it says is in it; nil is not said.
	members *[]string
}

func parseGroup(body []byte) (*incomingGroup, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, badRequest("invalidSyntax", "a Group, as JSON")
	}

	g := &incomingGroup{}
	for k, v := range all {
		switch strings.ToLower(k) {
		case "displayname":
			s, err := text(v)
			if err != nil {
				return nil, badRequest("invalidValue", "displayName: "+err.Error())
			}
			g.displayName = s
		case "externalid":
			s, err := text(v)
			if err != nil {
				return nil, badRequest("invalidValue", "externalId: "+err.Error())
			}
			g.externalId = s
		case "members":
			ids, err := memberIds(v)
			if err != nil {
				return nil, err
			}
			g.members = &ids
		}
	}

	return g, nil
}

// memberIds is the ids a list of members names: `[{"value": "…"}]`, or a null
// or an empty list for none.
func memberIds(raw json.RawMessage) ([]string, error) {
	var vs []struct {
		Value string `json:"value"`
	}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return []string{}, nil
	}
	if err := json.Unmarshal(raw, &vs); err != nil {
		return nil, badRequest("invalidValue", "members: a list of {value}")
	}

	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if v.Value != "" {
			out = append(out, v.Value)
		}
	}

	return out, nil
}

// groupChanges is what a PATCH says about a group, in the order it said it.
type groupChanges struct {
	name *string

	// Each in turn, since a directory may add and remove in one request.
	members []memberChange

	ignored []string
}

type memberChange struct {
	// add, remove, or replace: the whole of who is in it.
	op  string
	ids []string

	// everybody is a remove that names nobody, which is all of them.
	everybody bool
}

// parseGroupPatch reads RFC 7644 §3.5.2 the way [parsePatch] does: either
// dialect, `op` without regard to case, and an operation with no path taken as
// an object of attributes.
//
// Members are removed two ways and both arrive: Entra names them in the value
// (`"path": "members", "value": [{"value": "…"}]`), and the RFC's own form
// names one in the path (`members[value eq "…"]`).
func parseGroupPatch(body []byte) (*groupChanges, error) {
	var p patchOp
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, badRequest("invalidSyntax", "a PatchOp, as JSON")
	}
	if p.Operations == nil {
		return nil, badRequest("invalidSyntax", "Operations: a list")
	}

	c := &groupChanges{}
	set := func(op, path string, raw json.RawMessage) error {
		switch strings.ToLower(strings.TrimSpace(path)) {
		case "displayname":
			v, err := text(raw)
			if err != nil {
				return badRequest("invalidValue", "displayName: "+err.Error())
			}
			c.name = &v
		case "members":
			ids, err := memberIds(raw)
			if err != nil {
				return err
			}
			c.members = append(c.members, memberChange{op: op, ids: ids})
		case "schemas", "id", "meta":
		default:
			c.ignored = append(c.ignored, path)
		}

		return nil
	}

	for _, op := range p.Operations {
		kind := strings.ToLower(strings.TrimSpace(op.Op))
		path := strings.TrimSpace(op.Path)

		switch kind {
		case "add", "replace":
			if path != "" {
				if err := set(kind, path, op.Value); err != nil {
					return nil, err
				}

				continue
			}

			var all map[string]json.RawMessage
			if err := json.Unmarshal(op.Value, &all); err != nil {
				return nil, badRequest("invalidSyntax", "an operation with no path takes an object of attributes")
			}
			for k, v := range all {
				if err := set(kind, k, v); err != nil {
					return nil, err
				}
			}

		case "remove":
			lower := strings.ToLower(path)
			switch {
			case lower == "members":
				ids, err := memberIds(op.Value)
				if err != nil {
					return nil, err
				}
				c.members = append(c.members, memberChange{op: "remove", ids: ids, everybody: len(ids) == 0})
			case strings.HasPrefix(lower, "members["):
				id, ok := memberInPath(path)
				if !ok {
					return nil, badRequest("invalidPath", `remove: members[value eq "…"]`)
				}
				c.members = append(c.members, memberChange{op: "remove", ids: []string{id}})
			case path == "":
				return nil, badRequest("noTarget", "remove: which attribute")
			default:
				c.ignored = append(c.ignored, path)
			}

		default:
			return nil, badRequest("invalidSyntax", "op: add, replace or remove")
		}
	}

	return c, nil
}

// memberInPath is the id `members[value eq "id"]` names.
func memberInPath(path string) (string, bool) {
	open, shut := strings.Index(path, "["), strings.LastIndex(path, "]")
	if open < 0 || shut < open {
		return "", false
	}
	attr, value, err := splitEq(path[open+1 : shut])
	if err != nil || strings.ToLower(attr) != "value" {
		return "", false
	}

	return value, true
}

// everyGroup is the tenant's groups a directory made, in the order roster
// lists them. All of them, for [Server.everybody]'s reason and a smaller
// number: a tenant's groups are counted in tens.
func (s *Server) everyGroup(ctx context.Context, t *tenancy) ([]*rstr.Group, error) {
	var out []*rstr.Group

	after := ""
	for {
		vs, err := s.roster.Group().List(ctx, rstr.GroupListRequest_builder{
			Filters: []*rstr.GroupFilter{rstr.GroupFilter_builder{
				Tenant: rstr.TenantRef_builder{Id: t.tenant}.Build(),
			}.Build()},
			After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, v := range vs.GetItems() {
			if _, ok := v.GetLabels()[directoryLabel]; ok {
				out = append(out, v)
			}
		}

		after = vs.GetNext()
		if after == "" {
			return out, nil
		}
	}
}

// readGroup is a group this endpoint speaks of: one a directory made. Any
// other is "not here", which is what it is to the directory.
func (s *Server) readGroup(ctx context.Context, id []byte) (*rstr.Group, error) {
	v, err := s.roster.Group().Get(ctx, rstr.GroupGetRequest_builder{
		Ref:    rstr.GroupRef_builder{Id: id}.Build(),
		Select: rstr.GroupSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	switch status.Code(err) {
	case codes.OK:
	case codes.NotFound, codes.InvalidArgument:
		return nil, notFound("no group here by that id")
	default:
		return nil, err
	}
	if _, ok := v.GetLabels()[directoryLabel]; !ok {
		return nil, notFound("no group here by that id")
	}

	return v, nil
}

// membersOf is who is in a group as far as the directory is told: everybody in
// it who holds no role there, by their id.
func (s *Server) membersOf(ctx context.Context, group []byte) ([][]byte, error) {
	var out [][]byte

	after := ""
	for {
		vs, err := s.roster.GroupMembership().List(ctx, rstr.GroupMembershipListRequest_builder{
			Filters: []*rstr.GroupMembershipFilter{rstr.GroupMembershipFilter_builder{
				Group: rstr.GroupRef_builder{Id: group}.Build(),
			}.Build()},
			After: after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, v := range vs.GetItems() {
			if len(v.GetRole().GetId()) > 0 {
				continue
			}
			out = append(out, v.GetHolder().GetId())
		}

		after = vs.GetNext()
		if after == "" {
			return out, nil
		}
	}
}

// groupOf is a group as SCIM writes one. `members` nil leaves them out, which
// is what `excludedAttributes=members` asks -- Entra, on every read it makes.
func groupOf(v *rstr.Group, members [][]byte) map[string]any {
	id := idOf(v.GetId())
	name := v.GetName()
	if name == "" {
		name = v.GetAlias()
	}

	g := map[string]any{
		"schemas":     []string{groupSchema},
		"id":          id,
		"displayName": name,
		"meta": map[string]any{
			"resourceType": "Group",
			"created":      stamp(v.GetDateCreated().AsTime()),
			"lastModified": stamp(v.GetDateUpdated().AsTime()),
			"location":     Base + "/Groups/" + id,
		},
	}
	if x := v.GetLabels()[directoryLabel]; x != "" {
		g["externalId"] = x
	}
	if members != nil {
		ms := make([]map[string]any, 0, len(members))
		for _, m := range members {
			k := idOf(m)
			ms = append(ms, map[string]any{"value": k, "$ref": Base + "/Users/" + k, "type": "User"})
		}
		g["members"] = ms
	}

	return g
}

// withMembers is whether a read asked for a group's members, which it does
// unless it says otherwise.
func withMembers(r *http.Request) bool {
	for _, v := range strings.Split(r.URL.Query().Get("excludedAttributes"), ",") {
		if strings.EqualFold(strings.TrimSpace(v), "members") {
			return false
		}
	}

	return true
}

// shown is a group for an answer, with its members when they were asked for.
func (s *Server) shown(ctx context.Context, r *http.Request, v *rstr.Group) (map[string]any, error) {
	if !withMembers(r) {
		return groupOf(v, nil), nil
	}
	ms, err := s.membersOf(ctx, v.GetId())
	if err != nil {
		return nil, err
	}
	if ms == nil {
		ms = [][]byte{}
	}

	return groupOf(v, ms), nil
}

// listGroups is a lookup by one attribute, or every group a directory made, a
// page at a time.
func (s *Server) listGroups(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	start, count, err := page(r)
	if err != nil {
		return err
	}

	all, err := s.everyGroup(ctx, t)
	if err != nil {
		return err
	}
	if f := r.URL.Query().Get("filter"); f != "" {
		attr, value, err := parseGroupFilter(f)
		if err != nil {
			return err
		}

		var found []*rstr.Group
		for _, v := range all {
			if groupIs(v, attr, value) {
				found = append(found, v)
			}
		}
		all = found
	}

	items := []any{}
	for i := start - 1; i < len(all) && len(items) < count; i++ {
		g, err := s.shown(ctx, r, all[i])
		if err != nil {
			return err
		}
		items = append(items, g)
	}
	answer(w, http.StatusOK, list(items, len(all), start))

	return nil
}

// groupIs is whether a group is the one an attribute names: by what it is
// called, without regard to case, as a directory compares names; or by what
// the directory calls it, exactly.
func groupIs(v *rstr.Group, attr, value string) bool {
	switch attr {
	case byDisplayName:
		return strings.EqualFold(v.GetName(), value)
	case byExternalId:
		x, ok := v.GetLabels()[directoryLabel]

		return ok && x != "" && x == value
	case byId:
		return idOf(v.GetId()) == value
	}

	return false
}

func (s *Server) getGroup(ctx context.Context, w http.ResponseWriter, r *http.Request, _ *tenancy) error {
	id, ok := parseId(r.PathValue("id"))
	if !ok {
		return notFound("no group here by that id")
	}
	v, err := s.readGroup(ctx, id)
	if err != nil {
		return err
	}
	g, err := s.shown(ctx, r, v)
	if err != nil {
		return err
	}
	answer(w, http.StatusOK, g)

	return nil
}

// createGroup makes a group as the directory's own holder, which is what makes
// it the directory's to change: whoever makes a group manages it.
//
// One a directory already made under that name is refused as taken, since it
// looked the name up first and did not find it. One an **operator** made
// under the alias the name becomes is refused the same way by roster -- an
// alias is one group's within a tenant -- and is not taken over: handing a
// group to a directory is deciding who says what its bindings reach, and that
// is not decided by two names being alike.
func (s *Server) createGroup(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	b, err := body(r)
	if err != nil {
		return err
	}
	g, err := parseGroup(b)
	if err != nil {
		return err
	}
	if g.displayName == "" {
		return badRequest("invalidValue", "displayName: required")
	}

	all, err := s.everyGroup(ctx, t)
	if err != nil {
		return err
	}
	for _, v := range all {
		if groupIs(v, byDisplayName, g.displayName) || (g.externalId != "" && groupIs(v, byExternalId, g.externalId)) {
			return &problem{status: http.StatusConflict, scimType: "uniqueness", detail: "a group here is already called that"}
		}
	}

	// The alias is made from the name once, and stays: it is what a slug names
	// the group by and what a token carries for a client it is shown to, so a
	// rename in the directory changes what the group is called and never what
	// a relying party grants on.
	v, err := s.roster.Group().Add(ctx, rstr.GroupAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: t.tenant}.Build(),
		Alias:  arrives.AliasOf(g.displayName),
		Name:   g.displayName,
		Labels: map[string]string{directoryLabel: g.externalId},
	}.Build())
	if err != nil {
		return err
	}

	// Entra makes a group empty and adds to it; a directory that says who is
	// in it as it makes it is taken at its word.
	if g.members != nil {
		if err := s.put(ctx, t, v.GetId(), *g.members); err != nil {
			return err
		}
	}

	out, err := s.shown(ctx, r, v)
	if err != nil {
		return err
	}
	w.Header().Set("Location", Base+"/Groups/"+idOf(v.GetId()))
	answer(w, http.StatusCreated, out)

	return nil
}

// patchGroup changes what a group is called, or who is in it. Answered with no
// body, which is what Entra asks of a group's PATCH and the RFC allows: an
// answer listing a large group's members is the expensive half of the request.
func (s *Server) patchGroup(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	id, ok := parseId(r.PathValue("id"))
	if !ok {
		return notFound("no group here by that id")
	}
	b, err := body(r)
	if err != nil {
		return err
	}
	c, err := parseGroupPatch(b)
	if err != nil {
		return err
	}
	v, err := s.readGroup(ctx, id)
	if err != nil {
		return err
	}

	if c.name != nil {
		if err := s.rename(ctx, v, *c.name); err != nil {
			return err
		}
	}
	for _, m := range c.members {
		switch {
		case m.op == "add":
			err = s.join(ctx, t, id, m.ids)
		case m.op == "replace":
			err = s.put(ctx, t, id, m.ids)
		case m.everybody:
			err = s.put(ctx, t, id, nil)
		default:
			err = s.leave(ctx, t, id, m.ids)
		}
		if err != nil {
			return err
		}
	}
	s.said(r, c.ignored)
	w.WriteHeader(http.StatusNoContent)

	return nil
}

// replaceGroup writes what a whole Group says: its name, and who is in it when
// it says who.
func (s *Server) replaceGroup(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	id, ok := parseId(r.PathValue("id"))
	if !ok {
		return notFound("no group here by that id")
	}
	b, err := body(r)
	if err != nil {
		return err
	}
	g, err := parseGroup(b)
	if err != nil {
		return err
	}
	v, err := s.readGroup(ctx, id)
	if err != nil {
		return err
	}

	if g.displayName != "" {
		if err := s.rename(ctx, v, g.displayName); err != nil {
			return err
		}
	}
	if g.members != nil {
		if err := s.put(ctx, t, id, *g.members); err != nil {
			return err
		}
	}

	v, err = s.readGroup(ctx, id)
	if err != nil {
		return err
	}
	out, err := s.shown(ctx, r, v)
	if err != nil {
		return err
	}
	answer(w, http.StatusOK, out)

	return nil
}

// deleteGroup is the directory saying a group is gone. Everybody it says is in
// it is taken out, and the row is kept -- what becomes of it, and of whatever
// was bound to it, is an administrator's.
//
// Not an erase, for what the directory's key would have to hold to make one: a
// manager changes who is in their group and what it is called, and
// `Group.Erase` held through a binding reaches every group of the tenant
// nothing is bound to. So the group is still answered afterwards, with nobody
// in it, and a directory that makes one of the same name again finds it.
func (s *Server) deleteGroup(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	id, ok := parseId(r.PathValue("id"))
	if !ok {
		return notFound("no group here by that id")
	}
	if _, err := s.readGroup(ctx, id); err != nil {
		return err
	}
	if err := s.put(ctx, t, id, nil); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)

	return nil
}

// rename says what a group is called, under the version read. The alias stays
// ([Server.createGroup]).
func (s *Server) rename(ctx context.Context, v *rstr.Group, name string) error {
	if name == v.GetName() {
		return nil
	}
	_, err := s.roster.Group().Update(ctx, rstr.GroupUpdateRequest_builder{
		Ref:         rstr.GroupRef_builder{Id: v.GetId()}.Build(),
		DateUpdated: v.GetDateUpdated(),
		Name:        proto.String(name),
	}.Build())

	return err
}

// member is a person a directory names as in a group: one this endpoint
// speaks of, which the directory's own holder is not.
func (s *Server) member(ctx context.Context, t *tenancy, v string) ([]byte, error) {
	id, ok := parseId(v)
	if !ok {
		return nil, badRequest("invalidValue", "members: "+v+" is nobody here")
	}
	if _, err := s.read(ctx, t, id); err != nil {
		var nf *problem
		if asProblem(err, &nf) && nf.status == http.StatusNotFound {
			return nil, badRequest("invalidValue", "members: "+v+" is nobody here")
		}

		return nil, err
	}

	return id, nil
}

// join puts people into a group. Somebody already in it is left as they are.
func (s *Server) join(ctx context.Context, t *tenancy, group []byte, ids []string) error {
	for _, v := range ids {
		who, err := s.member(ctx, t, v)
		if err != nil {
			return err
		}
		_, err = s.roster.GroupMembership().Add(ctx, rstr.GroupMembershipAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: who}.Build(),
			Group:  rstr.GroupRef_builder{Id: group}.Build(),
		}.Build())
		if err != nil && status.Code(err) != codes.AlreadyExists {
			return err
		}
	}

	return nil
}

// leave takes people out of a group. Somebody not in it is nobody to take
// out, and the directory's own holder is not the directory's to name.
func (s *Server) leave(ctx context.Context, t *tenancy, group []byte, ids []string) error {
	for _, v := range ids {
		who, ok := parseId(v)
		if !ok || bytes.Equal(who, t.self) {
			continue
		}
		if err := s.out(ctx, group, who); err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) out(ctx context.Context, group, who []byte) error {
	_, err := s.roster.GroupMembership().Erase(ctx, rstr.GroupMembershipRef_builder{
		Member: rstr.GroupMembershipRefByMember_builder{
			Holder: rstr.HolderRef_builder{Id: who}.Build(),
			Group:  rstr.GroupRef_builder{Id: group}.Build(),
		}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return nil
	}

	return err
}

// put makes who is in a group exactly `ids`: those not named are taken out,
// and those not in it are put in.
func (s *Server) put(ctx context.Context, t *tenancy, group []byte, ids []string) error {
	want := map[string]bool{}
	for _, v := range ids {
		if id, ok := parseId(v); ok {
			want[string(id)] = true
		}
	}

	was, err := s.membersOf(ctx, group)
	if err != nil {
		return err
	}
	for _, who := range was {
		if want[string(who)] {
			continue
		}
		if err := s.out(ctx, group, who); err != nil {
			return err
		}
	}

	return s.join(ctx, t, group, ids)
}
