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

	"github.com/lesomnus/payday/slug"

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
// is in it, what it is called, whether there is one -- through the role its
// holder has **in** each group it made, and through no binding. So what it
// reaches is the groups it manages and no other, and that is roster's rule
// about every manager rather than one written for it. An administrator binds a role to such a group, or
// shows it to a client (`GroupExposure`), as to any other; who then holds
// that role is the directory's to say, and that is the administrator's
// decision when they do.
//
// # Which groups it speaks of
//
// The ones carrying [directoryLabel], which it wrote when it made them. A
// label and no more: no rule reads it. It is how the endpoint tells a group a
// directory made from one an operator did, so that an operator's group is
// never taken over: a directory naming a group alike makes another, under
// another alias ([Server.createGroup]).
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
//
// A member is named by its `value`, and an element that has none -- `$ref`
// alone, `display` alone, an empty `value` -- refuses the request. It was
// dropped, and a `remove` whose list came out empty was everybody: a member
// named some other way emptied the group.
func memberIds(raw json.RawMessage) ([]string, error) {
	var vs []struct {
		Value string `json:"value"`
	}
	if absent(raw) {
		return []string{}, nil
	}
	if err := json.Unmarshal(raw, &vs); err != nil {
		return nil, badRequest("invalidValue", "members: a list of {value}")
	}

	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if strings.TrimSpace(v.Value) == "" {
			return nil, badRequest("invalidValue", "members: a member is named by its value, the id it is read by")
		}
		out = append(out, v.Value)
	}

	return out, nil
}

// absent is whether an operation said no value: none at all, or a null.
func absent(raw json.RawMessage) bool {
	v := bytes.TrimSpace(raw)

	return len(v) == 0 || string(v) == "null"
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

	// everybody is a remove of `members` with no value at all, which is all
	// of them (RFC 7644 §3.5.2.2). One whose list is empty takes nobody out.
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
	var set func(op, path string, raw json.RawMessage) error
	set = func(op, path string, raw json.RawMessage) error {
		path = bare(path, groupSchema)
		switch strings.ToLower(path) {
		case strings.ToLower(groupSchema):
			// The URN as a key of an operation with no path, holding the
			// attributes behind it.
			var all map[string]json.RawMessage
			if err := json.Unmarshal(raw, &all); err != nil {
				return badRequest("invalidValue", groupSchema+": an object")
			}
			for k, v := range all {
				if err := set(op, k, v); err != nil {
					return err
				}
			}
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
		path := bare(op.Path, groupSchema)

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
				if absent(op.Value) {
					c.members = append(c.members, memberChange{op: "remove", everybody: true})

					break
				}
				ids, err := memberIds(op.Value)
				if err != nil {
					return nil, err
				}
				c.members = append(c.members, memberChange{op: "remove", ids: ids})
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
// it who holds no role there and whom the endpoint speaks of, by their id.
//
// Somebody erased since, or deleted by the directory, still has their place in
// roster -- an erase and a delete leave it -- and is not named: a directory
// that follows a member to `GET /Users/{id}` would find nobody there. Asked by
// [Server.present], which reads nothing personal, so that a read of a group
// leaves no row of the access record for each member; and so a front door's
// row or an app's, put in the group by somebody else, would be named.
func (s *Server) membersOf(ctx context.Context, t *tenancy, group []byte) ([][]byte, error) {
	ps, err := s.places(ctx, group)
	if err != nil {
		return nil, err
	}

	var out [][]byte
	for _, p := range ps {
		ok, err := s.present(ctx, t, p.holder)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, p.holder)
		}
	}

	return out, nil
}

// place is somebody's plain membership of a group: its own id, and whose it is.
type place struct {
	id     []byte
	holder []byte
}

// places is a group's plain memberships, whether or not their holders can
// still be read. One that carries a role is never the directory's: its own
// holder's, which manages the group, and anybody's an administrator appointed.
func (s *Server) places(ctx context.Context, group []byte) ([]place, error) {
	var out []place

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
			out = append(out, place{id: v.GetId(), holder: v.GetHolder().GetId()})
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
		ms := make([]reference, 0, len(members))
		for _, m := range members {
			ms = append(ms, referTo(idOf(m), "User"))
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
func (s *Server) shown(ctx context.Context, r *http.Request, t *tenancy, v *rstr.Group) (map[string]any, error) {
	if !withMembers(r) {
		return groupOf(v, nil), nil
	}
	ms, err := s.membersOf(ctx, t, v.GetId())
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
	// A `filter` that says nothing is refused, as of people.
	if q := r.URL.Query(); q.Has("filter") {
		attr, value, err := parseGroupFilter(q.Get("filter"))
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
		g, err := s.shown(ctx, r, t, all[i])
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

func (s *Server) getGroup(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	id, ok := parseId(r.PathValue("id"))
	if !ok {
		return notFound("no group here by that id")
	}
	v, err := s.readGroup(ctx, id)
	if err != nil {
		return err
	}
	g, err := s.shown(ctx, r, t, v)
	if err != nil {
		return err
	}
	answer(w, http.StatusOK, g)

	return nil
}

// createGroup makes a group as the directory's own holder, which is what makes
// it the directory's to change: whoever makes a group manages it.
//
// One a directory already made under that name, or under that `externalId`,
// is refused as taken, since it looked first and did not find it
// ([Server.taken]).
//
// # An alias somebody else has
//
// The alias is made from the name by the rule written for an address, so two
// names a directory keeps apart -- `R&D` and `R/D`, `Sales@EU` and
// `Sales@US`, `2024 Interns` and `2025 Interns` -- make one, and a group an
// operator made may have it already. Where roster answers that the alias is
// taken and no group of the directory's is called that, the group is made
// under the alias and a suffix, as a person whose alias is taken is
// (`server/core/directory.go`, `freeAlias`). An operator's group under the
// plain alias is not touched and not taken over: handing a group to a
// directory is deciding who says what its bindings reach, and that is not
// decided by two names being alike. So the alias a relying party grants on is
// read in roster, not guessed from the name.
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

	if err := s.taken(ctx, t, nil, g.displayName, g.externalId); err != nil {
		return err
	}

	// The alias is made from the name once, and stays: it is what a slug names
	// the group by and what a token carries for a client it is shown to, so a
	// rename in the directory changes what the group is called and never what
	// a relying party grants on.
	want := arrives.AliasOf(g.displayName)
	alias := want
	var v *rstr.Group
	for try := 0; ; try++ {
		v, err = s.roster.Group().Add(ctx, rstr.GroupAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: t.tenant}.Build(),
			Alias:  alias,
			Name:   g.displayName,
			Labels: map[string]string{directoryLabel: g.externalId},
		}.Build())
		if status.Code(err) != codes.AlreadyExists || try == 7 {
			break
		}
		// Asked again: a group of its own made under that name since -- two
		// creates at once -- is the refusal above, and anybody else's alias is
		// one to go round.
		if err := s.taken(ctx, t, nil, g.displayName, g.externalId); err != nil {
			return err
		}
		alias = suffixed(want)
	}
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

	out, err := s.shown(ctx, r, t, v)
	if err != nil {
		return err
	}
	w.Header().Set("Location", Base+"/Groups/"+idOf(v.GetId()))
	answer(w, http.StatusCreated, out)

	return nil
}

// taken is the refusal of a name, or an `externalId`, that one of the groups a
// directory made already has, other than `self`; nil where none has.
func (s *Server) taken(ctx context.Context, t *tenancy, self []byte, name, externalId string) error {
	all, err := s.everyGroup(ctx, t)
	if err != nil {
		return err
	}
	for _, v := range all {
		if self != nil && bytes.Equal(v.GetId(), self) {
			continue
		}
		if groupIs(v, byDisplayName, name) || (externalId != "" && groupIs(v, byExternalId, externalId)) {
			return &problem{status: http.StatusConflict, scimType: "uniqueness", detail: "a group here is already called that"}
		}
	}

	return nil
}

// suffixed is an alias with a hyphen and four letters after it, within the
// length an alias may be.
func suffixed(alias string) string {
	const tail = 5 // a hyphen and four
	if len(alias) > slug.AliasMaxLen-tail {
		alias = strings.TrimRight(alias[:slug.AliasMaxLen-tail], "-")
	}

	return alias + "-" + slug.RandomAliasN(4)
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
		if err := s.rename(ctx, t, v, *c.name); err != nil {
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
		if err := s.rename(ctx, t, v, g.displayName); err != nil {
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
	out, err := s.shown(ctx, r, t, v)
	if err != nil {
		return err
	}
	answer(w, http.StatusOK, out)

	return nil
}

// deleteGroup is the directory saying a group is gone, and it is: erased, as
// its manager may (`server/core/group.go`, `managing`), which the directory's
// holder is of each group it made. What was bound to it and who it was shown
// to go with it, as with any group erased -- held to what every erase of one
// is, so not a group that hands out what its eraser does not hold, and not
// the one a tenant is administered through.
//
// So it is `404` afterwards -- read, changed or deleted again -- and in no
// list, which is what RFC 7644 §3.6 asks and Microsoft's validator checks;
// and a group made under that name again is another group, with the alias
// the first had.
//
// # Where its role does not erase
//
// A tenant says what its managers do by the role they hold (`core.Manages`),
// and one whose managers do not erase groups keeps them so; a key minted
// before a manager could names no such method either. There the directory
// does the most a manager of that tenant may: every plain place in the group
// is taken away, the places of people it no longer speaks of among them, the
// row is kept and still answered, and the log says which of the two it was.
func (s *Server) deleteGroup(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	id, ok := parseId(r.PathValue("id"))
	if !ok {
		return notFound("no group here by that id")
	}
	if _, err := s.readGroup(ctx, id); err != nil {
		return err
	}

	_, err := s.roster.Group().Erase(ctx, rstr.GroupRef_builder{Id: id}.Build())
	switch status.Code(err) {
	case codes.OK:
	case codes.PermissionDenied:
		s.c.Log.WarnContext(r.Context(), "scim: this key does not erase the group, so everybody was taken out of it and it was kept; "+
			"`roster scim provision` mints a key that does, where the tenant's managers may",
			"path", r.URL.Path, "err", status.Convert(err).Message())
		if err := s.put(ctx, t, id, nil); err != nil {
			return err
		}
	default:
		return err
	}
	w.WriteHeader(http.StatusNoContent)

	return nil
}

// rename says what a group is called, under the version read. The alias stays
// ([Server.createGroup]).
//
// A name another of the directory's groups has is refused as a create under
// it is ([Server.taken]). It was written, after which a lookup by that name
// found two groups, and a directory cannot say which of two it means.
func (s *Server) rename(ctx context.Context, t *tenancy, v *rstr.Group, name string) error {
	if name == v.GetName() {
		return nil
	}
	if err := s.taken(ctx, t, v.GetId(), name, ""); err != nil {
		return err
	}
	_, err := s.roster.Group().Update(ctx, rstr.GroupUpdateRequest_builder{
		Ref:         rstr.GroupRef_builder{Id: v.GetId()}.Build(),
		DateUpdated: v.GetDateUpdated(),
		Name:        proto.String(name),
	}.Build())

	return err
}

// member is a person a directory names as in a group: one this endpoint
// speaks of ([speaksOf]), which the directory's own holder is not.
func (s *Server) member(ctx context.Context, t *tenancy, v string) ([]byte, error) {
	id, ok := parseId(v)
	if !ok {
		return nil, badRequest("invalidValue", "members: "+v+" is nobody here")
	}
	ok, err := s.answers(ctx, t, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, badRequest("invalidValue", "members: "+v+" is nobody here")
	}

	return id, nil
}

// resolve is the people a list of members names, every one of them, before
// anything is written: an id that is nobody the endpoint speaks of refuses the
// whole list, and the group is as it was.
//
// A list was read and written a member at a time, so one refused halfway was
// half written, and was again each time the directory sent it.
//
// Each id is asked about once, however often it is named.
func (s *Server) resolve(ctx context.Context, t *tenancy, ids []string) ([][]byte, error) {
	out := make([][]byte, 0, len(ids))
	asked := map[string]bool{}
	for _, v := range ids {
		if asked[v] {
			continue
		}
		asked[v] = true
		who, err := s.member(ctx, t, v)
		if err != nil {
			return nil, err
		}
		out = append(out, who)
	}

	return out, nil
}

// join puts people into a group. Somebody already in it is left as they are.
func (s *Server) join(ctx context.Context, t *tenancy, group []byte, ids []string) error {
	who, err := s.resolve(ctx, t, ids)
	if err != nil {
		return err
	}
	ps, err := s.places(ctx, group)
	if err != nil {
		return err
	}

	return s.in(ctx, group, who, ps)
}

// in puts people already resolved into a group, but for whoever has a place
// in it already, which is not written again.
func (s *Server) in(ctx context.Context, group []byte, who [][]byte, ps []place) error {
	there := map[string]bool{}
	for _, p := range ps {
		there[string(p.holder)] = true
	}
	for _, h := range who {
		if there[string(h)] {
			continue
		}
		there[string(h)] = true
		_, err := s.roster.GroupMembership().Add(ctx, rstr.GroupMembershipAddRequest_builder{
			Holder: rstr.HolderRef_builder{Id: h}.Build(),
			Group:  rstr.GroupRef_builder{Id: group}.Build(),
		}.Build())
		if err != nil && status.Code(err) != codes.AlreadyExists {
			return err
		}
	}

	return nil
}

// leave takes people out of a group, by their places in it ([Server.out]).
// Somebody not in it is nobody to take out, and the directory's own holder is
// not the directory's to name.
func (s *Server) leave(ctx context.Context, t *tenancy, group []byte, ids []string) error {
	named := map[string]bool{}
	for _, v := range ids {
		who, ok := parseId(v)
		if !ok || bytes.Equal(who, t.self) {
			continue
		}
		named[string(who)] = true
	}
	if len(named) == 0 {
		return nil
	}

	ps, err := s.places(ctx, group)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if !named[string(p.holder)] {
			continue
		}
		if err := s.out(ctx, p); err != nil {
			return err
		}
	}

	return nil
}

// out erases a place in a group by its own id, and not by the person: somebody
// erased since cannot be read, and their place is the directory's to take all
// the same. Went by the person, it read *nobody by that id* as nobody to take
// out, and a restore gave them back a place the directory had taken away.
func (s *Server) out(ctx context.Context, p place) error {
	_, err := s.roster.GroupMembership().Erase(ctx, rstr.GroupMembershipRef_builder{Id: p.id}.Build())
	if status.Code(err) == codes.NotFound {
		return nil
	}

	return err
}

// put makes who is in a group exactly `ids`: every one of them resolved first
// ([Server.resolve]); then every plain place whose holder is not named is
// taken out, the places of people erased since among them; then those not in
// it are put in.
func (s *Server) put(ctx context.Context, t *tenancy, group []byte, ids []string) error {
	who, err := s.resolve(ctx, t, ids)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, h := range who {
		want[string(h)] = true
	}

	ps, err := s.places(ctx, group)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if want[string(p.holder)] {
			continue
		}
		if err := s.out(ctx, p); err != nil {
			return err
		}
	}

	return s.in(ctx, group, who, ps)
}
