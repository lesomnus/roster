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
	"github.com/lesomnus/roster/server/front"
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

	// What the directory calls them: its id for them, and the two places it
	// says an address. None of it moves anything -- an identity and an address
	// are ways in, and the key this endpoint forwards writes neither onto
	// somebody who exists -- so it is kept beside the person and believed by
	// nothing (`HolderService.Tell`, [Server.told]).
	externalId *string
	userName   *string
	email      *string

	// Their numbers (`phones.go`): one said for a kind, where empty takes
	// that kind's away; and every one of them, when a write said the whole
	// list, which a replaced User does.
	phones    map[string]string
	allPhones *[]phone

	// Who they report to, by their id (`manager.go`); empty is nobody.
	manager *string

	// ignored is what was said and is not kept here, said in the log.
	ignored []string
}

// address is the address the directory says is theirs, by the rule a person
// is made under ([incoming.address]): the one it calls primary, or the user
// name when that is one. Empty when this write said neither.
func (c *changes) address() string {
	for _, v := range []*string{c.email, c.userName} {
		if v != nil && strings.Contains(*v, "@") {
			return front.Address(*v)
		}
	}

	return ""
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

	case p == managerPath, p == managerPath+".value":
		// Their id, as a directory writes one: bare, which is Entra's, or in
		// the RFC's `{value}`.
		v, err := text(raw)
		if err != nil {
			return badRequest("invalidValue", "manager: "+err.Error())
		}
		c.manager = &v

	case p == "externalid":
		v, err := text(raw)
		if err != nil {
			return badRequest("invalidValue", "externalId: "+err.Error())
		}
		c.externalId = &v

	case p == "username":
		v, err := text(raw)
		if err != nil {
			return badRequest("invalidValue", "userName: "+err.Error())
		}
		c.userName = &v

	case p == "emails":
		v, err := primary(raw)
		if err != nil {
			return err
		}
		if v != "" {
			c.email = &v
		}

	case p == "emails.value", p == `emails[type eq "work"].value`, p == "emails[primary eq true].value":
		v, err := text(raw)
		if err != nil {
			return badRequest("invalidValue", path+": "+err.Error())
		}
		c.email = &v

	case p == "phonenumbers":
		vs, err := phonesIn(raw)
		if err != nil {
			return err
		}
		for _, v := range vs {
			c.say(v.kind, v.number)
		}

	case p == "schemas", p == "id", p == "meta":
		// The envelope, not an attribute of anybody.

	default:
		if kind, ok := phonePath(path); ok {
			v, err := text(raw)
			if err != nil {
				return badRequest("invalidValue", path+": "+err.Error())
			}
			c.say(kind, v)

			break
		}
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
	case managerPath, managerPath + ".value":
		c.manager = &empty
	case "phonenumbers":
		c.allPhones = &[]phone{}
	default:
		if kind, ok := phonePath(path); ok {
			c.say(kind, "")

			break
		}
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

// primary is the address a list of `emails` puts first: the one marked
// primary, or the one that leads it.
func primary(raw json.RawMessage) (string, error) {
	var es []struct {
		Value   string `json:"value"`
		Primary any    `json:"primary"`
	}
	if err := json.Unmarshal(raw, &es); err != nil {
		return "", badRequest("invalidValue", "emails: a list of {value, primary}")
	}

	first := ""
	for _, e := range es {
		if is, _ := truth(mustJSON(e.Primary)); is {
			return e.Value, nil
		}
		if first == "" {
			first = e.Value
		}
	}

	return first, nil
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

	// And as [changes] reads them, which is what a replace is handed: a whole
	// User says what the directory calls somebody as much as a PATCH does.
	if u.userName != "" {
		u.changes.userName = &u.userName
	}
	if u.externalId != "" {
		u.changes.externalId = &u.externalId
	}
	if len(u.emails) > 0 {
		u.changes.email = &u.emails[0]
	}
	// And every number they have: what a whole User does not name, it says is
	// not theirs. Only where it speaks of numbers at all, since a directory
	// that maps none sends none, and must not take away what somebody else
	// wrote.
	if u.changes.phones != nil {
		all := make([]phone, 0, len(u.changes.phones))
		for k, v := range u.changes.phones {
			all = append(all, phone{kind: k, number: v})
		}
		u.changes.allPhones, u.changes.phones = &all, nil
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
	holder *rstr.Holder
	emails []string

	// linked is the subject of their identity at the connection the directory
	// provisions through: who a sign-in there is. Empty for somebody matched
	// by their address whom nothing has linked, and then what the directory
	// calls them is on the holder (`directory_subject`), believed by nothing.
	linked string
}

// externalId is what the directory is answered they are called: how roster
// knows them where it does, and what the directory said where it does not.
func (p *person) externalId() string {
	if p.linked != "" {
		return p.linked
	}

	return p.holder.GetDirectorySubject()
}

// read is a person this endpoint speaks of: never a row a file declared,
// never the directory's own holder, never somebody it deleted. Any of those is
// "not here", which is what it is to the directory.
func (s *Server) read(ctx context.Context, t *tenancy, id []byte) (*person, error) {
	h, err := s.roster.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref: rstr.HolderRef_builder{Id: id}.Build(),
		Select: rstr.HolderSelect_builder{
			Alias:            proto.Bool(true),
			Name:             proto.Bool(true),
			Labels:           proto.Bool(true),
			Profile:          proto.Bool(true),
			DateDisabled:     proto.Bool(true),
			Directory:        proto.Bool(true),
			DirectorySubject: proto.Bool(true),
			DirectoryAddress: proto.Bool(true),
			DateCreated:      proto.Bool(true),
			DateUpdated:      proto.Bool(true),
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
			p.linked = v.GetSubject()
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
	// Their addresses, led by the one the directory last said where that is
	// none of theirs here: it is answered with what it said, so that a
	// `userName` it changed reads back changed, while the address a link is
	// mailed to stays where the person or an operator put it.
	emails := p.emails
	if v := h.GetDirectoryAddress(); v != "" {
		emails = []string{v}
		for _, e := range p.emails {
			if e != v {
				emails = append(emails, e)
			}
		}
	}
	userName := h.GetAlias()
	if len(emails) > 0 {
		userName = emails[0]
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
	if v := p.externalId(); v != "" {
		u["externalId"] = v
	}
	if name != "" {
		u["displayName"] = name
		u["name"] = map[string]any{"formatted": name}
	}
	if v := h.GetProfile().GetLocale(); v != "" {
		u["preferredLanguage"] = v
	}
	if len(emails) > 0 {
		es := make([]map[string]any, 0, len(emails))
		for i, e := range emails {
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
