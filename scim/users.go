package scim

import (
	"context"
	"io"
	"net/http"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/arrives"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// maxBody is the most a request may carry. A User is a few hundred bytes; a
// megabyte is a directory sending something that is not one.
const maxBody = 1 << 20

func body(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return nil, badRequest("invalidSyntax", "a body of at most a megabyte")
	}

	return b, nil
}

// page is which of a list a request asks for: where it starts, counted from
// one, and how many, at most [maxResults].
func page(r *http.Request) (int, int, error) {
	q := r.URL.Query()

	start, count := 1, maxResults
	if v := q.Get("startIndex"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, 0, badRequest("invalidValue", "startIndex: a number")
		}
		start = max(n, 1)
	}
	if v := q.Get("count"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, 0, badRequest("invalidValue", "count: a number")
		}
		count = min(max(n, 0), maxResults)
	}

	return start, count, nil
}

// listUsers is a lookup by one attribute, or every person the directory signs
// in, a page at a time.
func (s *Server) listUsers(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	q := r.URL.Query()

	start, count, err := page(r)
	if err != nil {
		return err
	}

	if f := q.Get("filter"); f != "" {
		attr, value, err := parseFilter(f)
		if err != nil {
			return err
		}
		p, err := s.lookup(ctx, t, attr, value)
		if err != nil {
			return err
		}
		items := []any{}
		if p != nil && start == 1 && count > 0 {
			items = append(items, p.user())
		}
		total := 0
		if p != nil {
			total = 1
		}
		answer(w, http.StatusOK, list(items, total, start))

		return nil
	}

	ids, err := s.everybody(ctx, t)
	if err != nil {
		return err
	}
	items := []any{}
	for i := start - 1; i < len(ids) && len(items) < count; i++ {
		p, err := s.read(ctx, t, ids[i])
		if err != nil {
			return err
		}
		items = append(items, p.user())
	}
	answer(w, http.StatusOK, list(items, len(ids), start))

	return nil
}

// lookup is the person one attribute names, or nil.
func (s *Server) lookup(ctx context.Context, t *tenancy, attr, value string) (*person, error) {
	var id []byte
	switch attr {
	case byUserName, byEmail:
		v, err := s.roster.Email().Get(ctx, rstr.EmailGetRequest_builder{
			Ref: rstr.EmailRef_builder{At: rstr.EmailRefByAt_builder{
				TenantId: t.tenant, Address: proto.String(front.Address(value)),
			}.Build()}.Build(),
			Select: rstr.EmailSelect_builder{Holder: rstr.HolderSelect_builder{}.Build()}.Build(),
		}.Build())
		switch status.Code(err) {
		case codes.OK:
			id = v.GetHolder().GetId()
		case codes.NotFound:
			// Nobody's address here. Then whoever the directory last said it
			// of, which is how it finds again somebody whose `userName` it
			// changed: the address did not move, and what it said was kept.
			id, err = s.told(ctx, rstr.HolderRef_builder{Said: rstr.HolderRefBySaid_builder{
				DirectoryAddress: proto.String(front.Address(value)),
				Tenant:           rstr.TenantRef_builder{Id: t.tenant}.Build(),
			}.Build()}.Build())
			if err != nil || id == nil {
				return nil, err
			}
		default:
			return nil, err
		}

	case byExternalId:
		v, err := s.roster.Identity().Get(ctx, rstr.IdentityGetRequest_builder{
			Ref: rstr.IdentityRef_builder{Subject: rstr.IdentityRefBySubject_builder{
				TenantId: t.tenant, Provider: proto.String(t.provider), Subject: proto.String(value),
			}.Build()}.Build(),
			Select: rstr.IdentitySelect_builder{Holder: rstr.HolderSelect_builder{}.Build()}.Build(),
		}.Build())
		switch status.Code(err) {
		case codes.OK:
			id = v.GetHolder().GetId()
		case codes.NotFound:
			// Nobody signs in as that. Then whoever the directory calls it:
			// somebody it matched by their address, whom nothing has linked.
			id, err = s.told(ctx, rstr.HolderRef_builder{Known: rstr.HolderRefByKnown_builder{
				DirectorySubject: proto.String(value),
				Tenant:           rstr.TenantRef_builder{Id: t.tenant}.Build(),
			}.Build()}.Build())
			if err != nil || id == nil {
				return nil, err
			}
		default:
			return nil, err
		}

	case byId:
		v, ok := parseId(value)
		if !ok {
			return nil, nil
		}
		id = v
	}

	p, err := s.read(ctx, t, id)
	if err != nil {
		var nf *problem
		if asProblem(err, &nf) && nf.status == http.StatusNotFound {
			return nil, nil
		}

		return nil, err
	}

	return p, nil
}

// told is the person a directory's word names -- the address it last said, or
// what it calls them -- or nil. roster believes neither, and the endpoint
// reads both: it is the directory that is being answered.
func (s *Server) told(ctx context.Context, ref *rstr.HolderRef) ([]byte, error) {
	v, err := s.roster.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref:    ref,
		Select: rstr.HolderSelect_builder{}.Build(),
	}.Build())
	switch status.Code(err) {
	case codes.OK:
		return v.GetId(), nil
	case codes.NotFound, codes.InvalidArgument:
		return nil, nil
	default:
		return nil, err
	}
}

// tell keeps what this write says the directory calls somebody, where it is
// not how roster knows them, and takes away what was kept where it now is.
//
// # What is kept, and why nothing moves
//
// A directory that matched somebody by their address writes its own id onto
// them, and one whose `userName` changed writes the new one. Following either
// is a way in written onto a person who exists -- an identity, an address a
// link is mailed to -- which is what this endpoint's key is not given
// (`docs/scim.md` § *What its key may do*). Dropping them is how the two
// drift apart unseen. So they are kept as said (`HolderService.Tell`): the
// directory reads back what it wrote, an operator sees where they differ, and
// the one who believes an `externalId` links it (`roster scim adopt`).
//
// # And said once, where it matters
//
// Somebody matched and not linked is somebody their directory cannot suspend
// (`HolderService.Deactivate`), which is the reason there is a directory at
// all. That is a warning in the log the first time it is said of them, since
// the refusal itself arrives only on the day they leave.
func (s *Server) tell(ctx context.Context, r *http.Request, t *tenancy, p *person, c *changes) error {
	h := p.holder
	req := rstr.HolderTellRequest_builder{Ref: rstr.HolderRef_builder{Id: h.GetId()}.Build()}
	said := false

	if c.externalId != nil {
		want := *c.externalId
		if want == p.linked {
			want = ""
		}
		if want != h.GetDirectorySubject() {
			req.Subject, said = proto.String(want), true
			if want != "" && p.linked == "" {
				s.c.Log.WarnContext(r.Context(),
					"scim: matched by their address and not linked, so the directory cannot suspend them; "+
						"they are linked when they next sign in through the connection, or by `roster scim adopt`",
					"path", r.URL.Path, "connection", t.provider)
			}
		}
	}
	if want := c.address(); want != "" {
		// Kept unless it is what they would be answered with anyway: the
		// first of their addresses. One of theirs that is not the first is
		// still kept, so that the directory reads back the one it said.
		if len(p.emails) > 0 && p.emails[0] == want {
			want = ""
		}
		if want != h.GetDirectoryAddress() {
			req.Address, said = proto.String(want), true
		}
	}
	if !said {
		return nil
	}

	_, err := s.roster.Holder().Tell(ctx, req.Build())

	return err
}

func asProblem(err error, p **problem) bool {
	v, ok := err.(*problem)
	if ok {
		*p = v
	}

	return ok
}

// everybody is the people who sign in through the connection the directory
// provisions through, in the order roster lists them. The whole list, for a
// `totalResults` that is true: a tenant is people a directory provisions, a
// count measured in thousands at the most, and a cycle that lists them is not
// a request somebody waits on.
func (s *Server) everybody(ctx context.Context, t *tenancy) ([][]byte, error) {
	var out [][]byte
	seen := map[string]bool{}

	after := ""
	for {
		vs, err := s.roster.Identity().List(ctx, rstr.IdentityListRequest_builder{
			Filters: []*rstr.IdentityFilter{rstr.IdentityFilter_builder{TenantId: t.tenant}.Build()},
			After:   after,
		}.Build())
		if err != nil {
			return nil, err
		}
		for _, v := range vs.GetItems() {
			if v.GetProvider() != t.provider {
				continue
			}
			id := v.GetHolder().GetId()
			if seen[string(id)] {
				continue
			}
			seen[string(id)] = true

			if _, err := s.read(ctx, t, id); err != nil {
				var nf *problem
				if asProblem(err, &nf) && nf.status == http.StatusNotFound {
					continue
				}

				return nil, err
			}
			out = append(out, id)
		}

		after = vs.GetNext()
		if after == "" {
			return out, nil
		}
	}
}

func (s *Server) getUser(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	id, ok := parseId(r.PathValue("id"))
	if !ok {
		return notFound("nobody here by that id")
	}
	p, err := s.read(ctx, t, id)
	if err != nil {
		return err
	}
	answer(w, http.StatusOK, p.user())

	return nil
}

// createUser makes somebody, through the one verb that writes their ways in
// only into a person it is making (`HolderService.Provision`).
//
// Somebody already here by their address or their `externalId` is refused as
// taken: the directory looked them up first and did not find them, so the row
// it is about to make would be a second of them. A directory answered
// `uniqueness` matches instead.
func (s *Server) createUser(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	b, err := body(r)
	if err != nil {
		return err
	}
	u, err := parseUser(b)
	if err != nil {
		return err
	}
	if u.userName == "" {
		return badRequest("invalidValue", "userName: required")
	}
	if u.externalId == "" {
		return badRequest("invalidValue",
			"externalId: what a sign-in through "+t.provider+" calls them; map it to the directory's immutable id (Entra: objectId)")
	}

	address := front.Address(u.address())
	for attr, value := range map[string]string{byEmail: address, byExternalId: u.externalId} {
		if value == "" {
			continue
		}
		p, err := s.lookup(ctx, t, attr, value)
		if err != nil {
			return err
		}
		if p != nil {
			return &problem{status: http.StatusConflict, scimType: "uniqueness",
				detail: "somebody here already has that " + map[string]string{byEmail: "address", byExternalId: "externalId"}[attr]}
		}
	}

	profile := rstr.Profile_builder{}
	if u.display != nil {
		profile.DisplayName = *u.display
	}
	if u.department != nil {
		profile.Department = *u.department
	}
	if u.employeeNo != nil {
		profile.EmployeeNo = *u.employeeNo
	}
	if u.locale != nil {
		profile.Locale = *u.locale
	}
	name := ""
	if u.display != nil {
		name = *u.display
	}

	alias := address
	if alias == "" {
		alias = u.userName
	}
	h, err := s.roster.Holder().Provision(ctx, rstr.HolderProvisionRequest_builder{
		Tenant:   rstr.TenantRef_builder{Id: t.tenant}.Build(),
		Alias:    arrives.AliasOf(alias),
		Name:     name,
		Profile:  profile.Build(),
		Provider: t.provider,
		Subject:  u.externalId,
		Address:  address,
	}.Build())
	if err != nil {
		return err
	}
	if u.active != nil && !*u.active {
		if _, err := s.roster.Holder().Deactivate(ctx, rstr.HolderDeactivateRequest_builder{
			Ref: rstr.HolderRef_builder{Id: h.GetId()}.Build(),
		}.Build()); err != nil {
			return err
		}
	}
	s.said(r, u.ignored)

	p, err := s.read(ctx, t, h.GetId())
	if err != nil {
		return err
	}
	w.Header().Set("Location", Base+"/Users/"+idOf(h.GetId()))
	answer(w, http.StatusCreated, p.user())

	return nil
}

// replaceUser writes what a whole User says, attribute by attribute -- what it
// does not say is left, for [changes]'s reason.
func (s *Server) replaceUser(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	b, err := body(r)
	if err != nil {
		return err
	}
	u, err := parseUser(b)
	if err != nil {
		return err
	}

	return s.change(ctx, w, r, t, &u.changes)
}

func (s *Server) patchUser(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	b, err := body(r)
	if err != nil {
		return err
	}
	c, err := parsePatch(b)
	if err != nil {
		return err
	}

	return s.change(ctx, w, r, t, c)
}

// change writes [changes] to somebody and answers with them as they are now.
//
// The profile is read and written whole, under the version read, because
// that is how `Holder.Update` takes it -- with the picture carried through
// untouched, since a different one would take their portrait away. A version
// that moved in between is read again, a few times, rather than handed to the
// directory as a failure it would retry anyway.
func (s *Server) change(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy, c *changes) error {
	id, ok := parseId(r.PathValue("id"))
	if !ok {
		return notFound("nobody here by that id")
	}
	was, err := s.read(ctx, t, id)
	if err != nil {
		return err
	}
	if err := s.tell(ctx, r, t, was, c); err != nil {
		return err
	}
	ref := rstr.HolderRef_builder{Id: id}.Build()

	if c.profiled() {
		for try := 0; ; try++ {
			err := s.reprofile(ctx, ref, c)
			if err == nil {
				break
			}
			code := status.Code(err)
			if (code == codes.Aborted || code == codes.FailedPrecondition) && try < 3 {
				continue
			}

			return err
		}
	}

	if c.active != nil {
		var err error
		if *c.active {
			_, err = s.roster.Holder().Activate(ctx, rstr.HolderActivateRequest_builder{Ref: ref}.Build())
		} else {
			_, err = s.roster.Holder().Deactivate(ctx, rstr.HolderDeactivateRequest_builder{Ref: ref}.Build())
		}
		if err != nil {
			return err
		}
	}
	s.said(r, c.ignored)

	p, err := s.read(ctx, t, id)
	if err != nil {
		return err
	}
	answer(w, http.StatusOK, p.user())

	return nil
}

// reprofile is one read and one write of somebody's profile.
func (s *Server) reprofile(ctx context.Context, ref *rstr.HolderRef, c *changes) error {
	h, err := s.roster.Holder().Get(ctx, rstr.HolderGetRequest_builder{
		Ref:    ref,
		Select: rstr.HolderSelect_builder{Profile: proto.Bool(true), DateUpdated: proto.Bool(true)}.Build(),
	}.Build())
	if err != nil {
		return err
	}

	was := h.GetProfile()
	now := proto.Clone(was).(*rstr.Profile)
	if now == nil {
		now = &rstr.Profile{}
	}
	if c.display != nil {
		now.SetDisplayName(*c.display)
	}
	if c.department != nil {
		now.SetDepartment(*c.department)
	}
	if c.employeeNo != nil {
		now.SetEmployeeNo(*c.employeeNo)
	}
	if c.locale != nil {
		now.SetLocale(*c.locale)
	}
	if proto.Equal(was, now) {
		return nil
	}

	_, err = s.roster.Holder().Update(ctx, rstr.HolderUpdateRequest_builder{
		Ref:         ref,
		DateUpdated: h.GetDateUpdated(),
		Profile:     now,
	}.Build())

	return err
}

// deleteUser is the directory saying somebody is gone. They are suspended and
// kept -- an operator decides what becomes of the row -- and the endpoint
// speaks of them no more.
func (s *Server) deleteUser(ctx context.Context, w http.ResponseWriter, r *http.Request, t *tenancy) error {
	id, ok := parseId(r.PathValue("id"))
	if !ok {
		return notFound("nobody here by that id")
	}
	if _, err := s.read(ctx, t, id); err != nil {
		return err
	}
	if _, err := s.roster.Holder().Deactivate(ctx, rstr.HolderDeactivateRequest_builder{
		Ref:     rstr.HolderRef_builder{Id: id}.Build(),
		Deleted: true,
	}.Build()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)

	return nil
}

// said logs what a directory sent that is not kept here, once per request, so
// an attribute mapped and silently dropped is something an operator can find.
func (s *Server) said(r *http.Request, ignored []string) {
	if len(ignored) == 0 {
		return
	}
	s.c.Log.InfoContext(r.Context(), "scim: not kept here", "method", r.Method, "path", r.URL.Path, "attributes", ignored)
}
