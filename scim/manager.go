package scim

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rstr "github.com/lesomnus/roster/rstr"
)

// Who somebody reports to (#253): the enterprise schema's `manager`, kept as a
// `Relation` from the person to their manager.
//
// # The one word this endpoint and the LDAP front share
//
// roster does not say what a relation is: it keeps `holder`, `other` and
// labels, and reads none of them (`relation.proto`). So what makes one *who
// they report to* is [managerLabel] among its labels, written here and read
// by `ldap/` as `manager`, and by nothing in the server.
//
// # One manager, as a directory says it
//
// `manager` is single-valued, so the one a directory says is theirs replaces
// whoever was. A relation is one row a pair and may say more than one thing,
// so the word is taken out of the row it was in -- and the row with it when
// that was all it said -- rather than the row erased for what somebody else
// wrote into it.

// managerLabel is the label that says `other` is who `holder` reports to.
const managerLabel = "manager"

// managerPath is where the enterprise schema keeps it, lowercased as [changes]
// compares paths.
var managerPath = strings.ToLower(enterpriseSchema) + ":manager"

// relationsOf is what somebody is to anybody, as roster lists it.
//
// A key that may not list them answers none, and says it could not, for
// [Server.phonesOf]'s reason: a key minted before relations were kept names
// none of this, and what it does name should go on working.
func (s *Server) relationsOf(ctx context.Context, holder []byte) ([]*rstr.Relation, bool, error) {
	var out []*rstr.Relation

	after := ""
	for {
		vs, err := s.roster.Relation().List(ctx, rstr.RelationListRequest_builder{
			Filters: []*rstr.RelationFilter{rstr.RelationFilter_builder{
				Holder: rstr.HolderRef_builder{Id: holder}.Build(),
			}.Build()},
			After: after,
		}.Build())
		if status.Code(err) == codes.PermissionDenied {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		out = append(out, vs.GetItems()...)

		after = vs.GetNext()
		if after == "" {
			return out, true, nil
		}
	}
}

// managerOf is who somebody reports to, by their id, or nil.
func (s *Server) managerOf(ctx context.Context, holder []byte) ([]byte, error) {
	vs, _, err := s.relationsOf(ctx, holder)
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		if _, ok := v.GetLabels()[managerLabel]; ok {
			return v.GetOther().GetId(), nil
		}
	}

	return nil, nil
}

// reports writes who a write says somebody reports to: nobody, or one person
// this endpoint speaks of.
//
// Somebody it does not speak of -- not provisioned yet, or not a person -- is
// said in the log and changes nothing, rather than refusing the write: a
// directory sends a manager with everything else it knows of somebody, and
// the rest of that is not wrong.
func (s *Server) reports(ctx context.Context, r *http.Request, t *tenancy, holder []byte, c *changes) error {
	if c.manager == nil {
		return nil
	}

	var want []byte
	if *c.manager != "" {
		id, ok := parseId(*c.manager)
		if ok {
			_, err := s.read(ctx, t, id)
			var nf *problem
			switch {
			case err == nil:
			case asProblem(err, &nf) && nf.status == http.StatusNotFound:
				ok = false
			default:
				return err
			}
		}
		if !ok || bytes.Equal(id, holder) {
			s.c.Log.InfoContext(r.Context(), "scim: a manager who is nobody here, so not kept", "path", r.URL.Path)

			return nil
		}
		want = id
	}

	have, may, err := s.relationsOf(ctx, holder)
	if err != nil {
		return err
	}
	if !may {
		s.c.Log.InfoContext(r.Context(), "scim: this key keeps no relations, so a manager was not kept", "path", r.URL.Path)

		return nil
	}

	ref := rstr.HolderRef_builder{Id: holder}.Build()
	put := func(other []byte, labels map[string]string) error {
		_, err := s.roster.Relation().Add(ctx, rstr.RelationAddRequest_builder{
			Holder: ref,
			Other:  rstr.HolderRef_builder{Id: other}.Build(),
			Labels: labels,
		}.Build())

		return err
	}
	drop := func(v *rstr.Relation) error {
		_, err := s.roster.Relation().Erase(ctx, rstr.RelationRef_builder{Id: v.GetId()}.Build())
		if status.Code(err) == codes.NotFound {
			return nil
		}

		return err
	}

	said := false
	for _, v := range have {
		_, is := v.GetLabels()[managerLabel]
		same := want != nil && bytes.Equal(v.GetOther().GetId(), want)
		switch {
		case is && same:
			said = true

		case is:
			// Who they reported to, and do not now. The word comes out of the
			// row, and the row goes when that was all it said: there is no
			// verb that changes one, so it is erased and made again.
			rest := maps.Clone(v.GetLabels())
			delete(rest, managerLabel)
			if err := drop(v); err != nil {
				return err
			}
			if len(rest) > 0 {
				if err := put(v.GetOther().GetId(), rest); err != nil {
					return err
				}
			}

		case same:
			// Already something to them, by somebody else's word: that row
			// says this as well now.
			with := maps.Clone(v.GetLabels())
			if with == nil {
				with = map[string]string{}
			}
			with[managerLabel] = ""
			if err := drop(v); err != nil {
				return err
			}
			if err := put(want, with); err != nil {
				return err
			}
			said = true
		}
	}
	if want != nil && !said {
		return put(want, map[string]string{managerLabel: ""})
	}

	return nil
}
