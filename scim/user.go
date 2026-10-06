package scim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// declared is the label a row a file declared carries (`cmd.Declared`), named
// here because a consumer does not import `cmd`. Those rows are a front
// door's, never a person a directory provisioned.
const declared = "roster.declared"

// directoryDeleted is `Holder.directory` for somebody the directory deleted:
// gone, as far as it is concerned, and so as far as this endpoint answers.
const directoryDeleted = "deleted"

// changes is what a write says about a person, attribute by attribute: nil is
// not said, which leaves the attribute as it is.
//
// # Why absent is not cleared
//
// The directory owns what it sends. A tenant that keeps names in another place
// -- Slack, say -- drops `displayName` from its directory's mapping, and a
// replace that cleared what it did not mention would wipe those names at
// every cycle. So an attribute is written when it is sent and left otherwise,
// and taking a value away is a `remove`.
type changes struct {
	display    *string
	department *string
	employeeNo *string
	locale     *string
	active     *bool

	// ignored is what was said and is not kept here, said in the log.
	ignored []string
}

func (c *changes) profiled() bool {
	return c.display != nil || c.department != nil || c.employeeNo != nil || c.locale != nil
}

// set is one attribute, by its path as a directory writes it.
func (c *changes) set(path string, raw json.RawMessage) error {
	p := strings.ToLower(strings.TrimSpace(path))
	ext := strings.ToLower(enterpriseSchema)

	switch {
	case p == "displayname", p == "name.formatted":
		v, err := text(raw)
		if err != nil {
			return badRequest("invalidValue", path+": "+err.Error())
		}
		c.display = &v

	case p == "name":
		var name map[string]json.RawMessage
		if err := json.Unmarshal(raw, &name); err != nil {
			return badRequest("invalidValue", "name: an object")
		}
		for k, v := range name {
			if err := c.set("name."+k, v); err != nil {
				return err
			}
		}

	case p == "active":
		v, err := truth(raw)
		if err != nil {
			return badRequest("invalidValue", "active: "+err.Error())
		}
		c.active = &v

	case p == "preferredlanguage", p == "locale":
		v, err := text(raw)
		if err != nil {
			return badRequest("invalidValue", path+": "+err.Error())
		}
		c.locale = &v

	case p == ext:
		var e map[string]json.RawMessage
		if err := json.Unmarshal(raw, &e); err != nil {
			return badRequest("invalidValue", enterpriseSchema+": an object")
		}
		for k, v := range e {
			if err := c.set(enterpriseSchema+":"+k, v); err != nil {
				return err
			}
		}

	case p == ext+":department":
		v, err := text(raw)
		if err != nil {
			return badRequest("invalidValue", "department: "+err.Error())
		}
		c.department = &v

	case p == ext+":employeenumber":
		v, err := text(raw)
		if err != nil {
			return badRequest("invalidValue", "employeeNumber: "+err.Error())
		}
		c.employeeNo = &v

	case p == "schemas", p == "id", p == "meta":
		// The envelope, not an attribute of anybody.

	default:
		c.ignored = append(c.ignored, path)
	}

	return nil
}

// clear is a `remove` of one attribute.
func (c *changes) clear(path string) {
	empty := ""
	p := strings.ToLower(strings.TrimSpace(path))
	ext := strings.ToLower(enterpriseSchema)

	switch p {
	case "displayname", "name.formatted", "name":
		c.display = &empty
	case "preferredlanguage", "locale":
		c.locale = &empty
	case ext + ":department":
		c.department = &empty
	case ext + ":employeenumber":
		c.employeeNo = &empty
	default:
		c.ignored = append(c.ignored, path)
	}
}

// text is a value a directory sent as a string, or as the one thing a string
// is often sent inside: a number for an employee number, an empty list or a
// null for nothing.
func text(raw json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("not a value")
	}
	switch v := v.(type) {
	case nil:
		return "", nil
	case string:
		return strings.TrimSpace(v), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	case []any:
		if len(v) == 0 {
			return "", nil
		}
		b, _ := json.Marshal(v[0])

		return text(b)
	case map[string]any:
		if inner, ok := v["value"]; ok {
			b, _ := json.Marshal(inner)

			return text(b)
		}
	}

	return "", fmt.Errorf("not a string")
}

// truth is a boolean, as RFC 7644 sends one or as Entra did for years:
// `"True"` and `"False"`, strings.
func truth(raw json.RawMessage) (bool, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("not a value")
	}
	switch v := v.(type) {
	case bool:
		return v, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}

	return false, fmt.Errorf("true or false")
}

// incoming is a User a directory sent whole: on a create, or a replace.
type incoming struct {
	userName   string
	externalId string
	emails     []string // the primary first
	changes
}

func parseUser(body []byte) (*incoming, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, badRequest("invalidSyntax", "a User, as JSON")
	}

	u := &incoming{}
	for k, v := range all {
		switch strings.ToLower(k) {
		case "username":
			s, err := text(v)
			if err != nil {
				return nil, badRequest("invalidValue", "userName: "+err.Error())
			}
			u.userName = s
		case "externalid":
			s, err := text(v)
			if err != nil {
				return nil, badRequest("invalidValue", "externalId: "+err.Error())
			}
			u.externalId = s
		case "emails":
			var es []struct {
				Value   string `json:"value"`
				Primary any    `json:"primary"`
			}
			if err := json.Unmarshal(v, &es); err != nil {
				return nil, badRequest("invalidValue", "emails: a list of {value, primary}")
			}
			for _, e := range es {
				primary, _ := truth(mustJSON(e.Primary))
				if primary {
					u.emails = append([]string{e.Value}, u.emails...)
				} else {
					u.emails = append(u.emails, e.Value)
				}
			}
		default:
			if err := u.set(k, v); err != nil {
				return nil, err
			}
		}
	}

	return u, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)

	return b
}

// address is where to reach them: the primary address, or the user name when
// that is one.
func (u *incoming) address() string {
	for _, e := range u.emails {
		if strings.Contains(e, "@") {
			return e
		}
	}
	if strings.Contains(u.userName, "@") {
		return u.userName
	}

	return ""
}

// person is somebody, read for an answer.
type person struct {
	holder     *rstr.Holder
	emails     []string
	externalId string
}

// read is a person this endpoint speaks of: never a row a file declared,
// never the directory's own holder, never somebody it deleted. Any of those is
// "not here", which is what it is to the directory.
func (s *Server) read(ctx context.Context, t *tenancy, id []byte) (*person, error) {
	h, err := s.roster.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Id: id}.Build(),
		Select: rstr.HolderSelect_builder{
			Alias:        proto.Bool(true),
			Name:         proto.Bool(true),
			Labels:       proto.Bool(true),
			Profile:      proto.Bool(true),
			DateDisabled: proto.Bool(true),
			Directory:    proto.Bool(true),
			DateCreated:  proto.Bool(true),
			DateUpdated:  proto.Bool(true),
		}.Build(),
	}.Build())
	switch status.Code(err) {
	case codes.OK:
	case codes.NotFound, codes.InvalidArgument:
		return nil, notFound("nobody here by that id")
	default:
		return nil, err
	}
	if h.GetLabels()[declared] != "" || bytes.Equal(h.GetId(), t.self) || h.GetDirectory() == directoryDeleted {
		return nil, notFound("nobody here by that id")
	}

	p := &person{holder: h}

	es, err := s.roster.Email().List(ctx, rstr.EmailListRequest_builder{
		Filters: []*rstr.EmailFilter{rstr.EmailFilter_builder{
			Holder: rstr.HolderRef_builder{Id: id}.Build(),
		}.Build()},
	}.Build())
	if err != nil {
		return nil, err
	}
	for _, e := range es.GetItems() {
		p.emails = append(p.emails, e.GetAddress())
	}

	ids, err := s.roster.Identity().List(ctx, rstr.IdentityListRequest_builder{
		Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{
			Holder: rstr.HolderRef_builder{Id: id}.Build(),
		}.Build()},
	}.Build())
	if err != nil {
		return nil, err
	}
	for _, v := range ids.GetItems() {
		if v.GetProvider() == t.provider {
			p.externalId = v.GetSubject()
		}
	}

	return p, nil
}

// idOf is a holder's id as the endpoint writes it.
func idOf(b []byte) string {
	id, err := pdid.From(b)
	if err != nil {
		return ""
	}

	return id.String()
}

// parseId is the id a directory sent back, or nothing.
func parseId(v string) ([]byte, bool) {
	id, err := pdid.Parse(v)
	if err != nil {
		return nil, false
	}

	return id.Bytes(), true
}

// user is a person as SCIM writes one.
func (p *person) user() map[string]any {
	h := p.holder
	id := idOf(h.GetId())

	name := h.GetProfile().GetDisplayName()
	if name == "" {
		name = h.GetName()
	}
	userName := h.GetAlias()
	if len(p.emails) > 0 {
		userName = p.emails[0]
	}

	u := map[string]any{
		"schemas":  []string{userSchema, enterpriseSchema},
		"id":       id,
		"userName": userName,
		"active":   h.GetDateDisabled() == nil,
		"meta": map[string]any{
			"resourceType": "User",
			"created":      stamp(h.GetDateCreated().AsTime()),
			"lastModified": stamp(h.GetDateUpdated().AsTime()),
			"location":     Base + "/Users/" + id,
		},
	}
	if p.externalId != "" {
		u["externalId"] = p.externalId
	}
	if name != "" {
		u["displayName"] = name
		u["name"] = map[string]any{"formatted": name}
	}
	if v := h.GetProfile().GetLocale(); v != "" {
		u["preferredLanguage"] = v
	}
	if len(p.emails) > 0 {
		es := make([]map[string]any, 0, len(p.emails))
		for i, e := range p.emails {
			es = append(es, map[string]any{"value": e, "type": "work", "primary": i == 0})
		}
		u["emails"] = es
	}

	ent := map[string]any{}
	if v := h.GetProfile().GetDepartment(); v != "" {
		ent["department"] = v
	}
	if v := h.GetProfile().GetEmployeeNo(); v != "" {
		ent["employeeNumber"] = v
	}
	if len(ent) > 0 {
		u[enterpriseSchema] = ent
	}

	return u
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }
