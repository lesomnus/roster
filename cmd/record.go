// Package cmd, this file: what roster hands payday's access record, where
// what payday reads for itself is not what a row should say (#280).
//
// The record is payday's machine: its interceptor decides what a call
// carried, and its writer when a row is written. What roster wires is whose
// data a message carries ([Server.accessed]) and where the rows go
// ([Build]). Each thing here is one of those two, wrapped.
package cmd

import (
	"context"
	"log/slog"
	"slices"
	"time"
	"uuid"

	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	entemail "github.com/lesomnus/roster/internal/ent/email"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	entidentity "github.com/lesomnus/roster/internal/ent/identity"
	entphone "github.com/lesomnus/roster/internal/ent/phone"
	"github.com/lesomnus/roster/server/pd"
)

// filing is how long the tenants of the rows one message names are looked
// for, before the rows are filed as they were.
//
// A deadline of its own because payday hands [filed] the message and not the
// call: there is no context to take one from. It is in front of a read's
// answer, so it is short; a database that does not answer a lookup by key in
// this long is not answering the read either.
const filing = 5 * time.Second

// filed is `read` -- whose data a message carries, `pd.Subjects`, or whose
// rows a request names, `pd.SubjectRefs` -- with each row that came back with
// no tenant given the tenant it is in (#280).
//
// # What it was
//
// payday reads a row's tenant off the message that carried it, and files one
// that says none under the caller's. A request's references say none at all.
// For somebody walled to one tenant, the caller's is the row's. It is not for
// the two callers the record is most for: an operator at `admin.addr` is held
// by the control plane's tenant, which is no tenant of this plane, and a
// deployment key by none. Either one reading a customer's person with a
// `select` that leaves the tenant out, or refused whatever it asked for, left
// a row that the review by customer does not show and a hold on the customer
// does not keep.
//
// # What it does
//
// The tenant is read off the row, by its key: one statement for each kind of
// row the message names, however many of them, so a page of a hundred is one
// lookup and not a hundred. A message that said every tenant asks nothing.
//
// A key that names no row stays as it was, the caller's: a refused request
// names whatever it was sent, and there is no row to say whose an identifier
// nobody has would have been. So does every row of a message whose lookup
// failed, said in the log: the read is answered and recorded either way, as a
// read at a listener is, and a row under the caller is better than none.
//
// And so does what a message names past the first thousand, which is as many
// as a row names (`access.MaxSubjects`). No answer carries so many, a page
// being a hundred; a request may, since a refused one is read as it was sent,
// and what one caller sends is not what decides how much the database is
// asked.
func filed(db *ent.Client, l *slog.Logger, read func(proto.Message) []access.Subject) func(proto.Message) []access.Subject {
	return func(m proto.Message) []access.Subject {
		vs := read(m)

		var ids []pdid.Id
		asked := map[pdid.Id]bool{}
		for _, v := range vs {
			if len(ids) == access.MaxSubjects {
				break
			}
			if v.Tenant.IsZero() && !asked[v.Id] {
				asked[v.Id] = true
				ids = append(ids, v.Id)
			}
		}
		if len(ids) == 0 {
			return vs
		}

		ctx, cancel := context.WithTimeout(context.Background(), filing)
		defer cancel()

		in, err := tenantsOf(ctx, db, ids)
		if err != nil {
			l.Error("access: whose tenant a row is in could not be read, so what named it is filed under the caller's",
				"rows", len(ids), "err", err)
			return vs
		}
		for i, v := range vs {
			if t, ok := in[v.Id]; ok && v.Tenant.IsZero() {
				vs[i].Tenant = t
			}
		}

		return vs
	}
}

// tenantsOf is the tenant each of `ids` is in, for those that are the key of
// a row holding somebody's personal data: a person, an address, an account at
// a provider, a number. Which it is the identifier says, and one that is none
// of them, or is nobody's, is left out.
//
// Read past the wall and past an erase, since it is asked for the record and
// not for a caller: somebody erased is still somebody of their tenant.
func tenantsOf(ctx context.Context, db *ent.Client, ids []pdid.Id) (map[pdid.Id]pdid.Id, error) {
	by := map[pdid.Domain][]uuid.UUID{}
	for _, id := range ids {
		by[id.Domain()] = append(by[id.Domain()], id.Uuid())
	}

	out := make(map[pdid.Id]pdid.Id, len(ids))
	for domain, keys := range by {
		// A statement for every few hundred: a message names at most a page,
		// and what a refused request names is whatever it was sent.
		for ks := range slices.Chunk(keys, 500) {
			var err error
			switch domain {
			case pd.HolderDomain:
				var rs []*ent.Holder
				rs, err = db.Holder.Query().Where(entholder.IdIn(ks...)).Select(entholder.FieldTenantId).All(ctx)
				for _, r := range rs {
					out[pdid.Id(r.Id)] = pdid.Id(r.TenantId)
				}
			case pd.EmailDomain:
				var rs []*ent.Email
				rs, err = db.Email.Query().Where(entemail.IdIn(ks...)).Select(entemail.FieldTenantId).All(ctx)
				for _, r := range rs {
					out[pdid.Id(r.Id)] = pdid.Id(r.TenantId)
				}
			case pd.IdentityDomain:
				var rs []*ent.Identity
				rs, err = db.Identity.Query().Where(entidentity.IdIn(ks...)).Select(entidentity.FieldTenantId).All(ctx)
				for _, r := range rs {
					out[pdid.Id(r.Id)] = pdid.Id(r.TenantId)
				}
			case pd.PhoneDomain:
				var rs []*ent.Phone
				rs, err = db.Phone.Query().Where(entphone.IdIn(ks...)).Select(entphone.FieldTenantId).All(ctx)
				for _, r := range rs {
					out[pdid.Id(r.Id)] = pdid.Id(r.TenantId)
				}
			}
			if err != nil {
				return nil, err
			}
		}
	}

	return out, nil
}
