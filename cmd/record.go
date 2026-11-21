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
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/roster/internal/ent"
	entapikey "github.com/lesomnus/roster/internal/ent/apikey"
	entbinding "github.com/lesomnus/roster/internal/ent/binding"
	entcredential "github.com/lesomnus/roster/internal/ent/credential"
	entemail "github.com/lesomnus/roster/internal/ent/email"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	entidentity "github.com/lesomnus/roster/internal/ent/identity"
	entnomination "github.com/lesomnus/roster/internal/ent/nomination"
	entphone "github.com/lesomnus/roster/internal/ent/phone"
	entrelation "github.com/lesomnus/roster/internal/ent/relation"
	entrole "github.com/lesomnus/roster/internal/ent/role"
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
// a provider, a number -- and what was written *about* somebody, which the
// schema declares personal too (#280): a key and a credential, a binding, a
// relation, a nomination. Which it is the identifier says, and one that is
// none of them, or is nobody's, is left out.
//
// The first four, a relation and a nomination say their tenant in a column. A
// key and a credential say it by whose they are, and a binding by its role,
// which is of one tenant whether it is bound to a person or to a group: one
// statement for the rows and one for what they name.
//
// # Why the second five are here
//
// They were declared in the same part this was written in, by a change that
// did not know of this one, and so a read of a customer's key by an operator
// was a row of the record from then on and was filed under the operators' own
// tenant: the misfiling this ends, back for exactly the kinds that had just
// begun to be recorded. `TestEveryKindOnTheRecordSaysWhoseTenantItIsIn` holds
// [whoseTenant] to the schema, so that the next kind declared is asked here.
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
		whose, ok := whoseTenant[domain]
		if !ok {
			continue
		}
		// A statement for every few hundred: a message names at most a page,
		// and what a refused request names is whatever it was sent.
		for ks := range slices.Chunk(keys, 500) {
			in, err := whose(ctx, db, ks)
			if err != nil {
				return nil, err
			}
			for k, t := range in {
				out[pdid.Id(k)] = pdid.Id(t)
			}
		}
	}

	return out, nil
}

// whoseTenant is how each kind on the record says which tenant a row of it is
// in: the list [tenantsOf] reads by, and the one a test holds to the schema.
var whoseTenant = map[pdid.Domain]func(context.Context, *ent.Client, []uuid.UUID) (map[uuid.UUID]uuid.UUID, error){
	pd.HolderDomain: func(ctx context.Context, db *ent.Client, ks []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
		rs, err := db.Holder.Query().Where(entholder.IdIn(ks...)).Select(entholder.FieldTenantId).All(ctx)
		out := make(map[uuid.UUID]uuid.UUID, len(rs))
		for _, r := range rs {
			out[r.Id] = r.TenantId
		}

		return out, err
	},
	pd.EmailDomain: func(ctx context.Context, db *ent.Client, ks []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
		rs, err := db.Email.Query().Where(entemail.IdIn(ks...)).Select(entemail.FieldTenantId).All(ctx)
		out := make(map[uuid.UUID]uuid.UUID, len(rs))
		for _, r := range rs {
			out[r.Id] = r.TenantId
		}

		return out, err
	},
	pd.IdentityDomain: func(ctx context.Context, db *ent.Client, ks []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
		rs, err := db.Identity.Query().Where(entidentity.IdIn(ks...)).Select(entidentity.FieldTenantId).All(ctx)
		out := make(map[uuid.UUID]uuid.UUID, len(rs))
		for _, r := range rs {
			out[r.Id] = r.TenantId
		}

		return out, err
	},
	pd.PhoneDomain: func(ctx context.Context, db *ent.Client, ks []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
		rs, err := db.Phone.Query().Where(entphone.IdIn(ks...)).Select(entphone.FieldTenantId).All(ctx)
		out := make(map[uuid.UUID]uuid.UUID, len(rs))
		for _, r := range rs {
			out[r.Id] = r.TenantId
		}

		return out, err
	},
	pd.RelationDomain: func(ctx context.Context, db *ent.Client, ks []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
		rs, err := db.Relation.Query().Where(entrelation.IdIn(ks...)).Select(entrelation.FieldTenantId).All(ctx)
		out := make(map[uuid.UUID]uuid.UUID, len(rs))
		for _, r := range rs {
			out[r.Id] = r.TenantId
		}

		return out, err
	},
	pd.NominationDomain: func(ctx context.Context, db *ent.Client, ks []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
		rs, err := db.Nomination.Query().Where(entnomination.IdIn(ks...)).Select(entnomination.FieldTenantId).All(ctx)
		out := make(map[uuid.UUID]uuid.UUID, len(rs))
		for _, r := range rs {
			out[r.Id] = r.TenantId
		}

		return out, err
	},
	pd.ApiKeyDomain: func(ctx context.Context, db *ent.Client, ks []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
		rs, err := db.ApiKey.Query().Where(entapikey.IdIn(ks...)).
			WithHolder(func(q *ent.HolderQuery) { q.Select(entholder.FieldTenantId) }).All(ctx)
		out := make(map[uuid.UUID]uuid.UUID, len(rs))
		for _, r := range rs {
			if h := r.Edges.Holder; h != nil {
				out[r.Id] = h.TenantId
			}
		}

		return out, err
	},
	pd.CredentialDomain: func(ctx context.Context, db *ent.Client, ks []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
		rs, err := db.Credential.Query().Where(entcredential.IdIn(ks...)).
			WithHolder(func(q *ent.HolderQuery) { q.Select(entholder.FieldTenantId) }).All(ctx)
		out := make(map[uuid.UUID]uuid.UUID, len(rs))
		for _, r := range rs {
			if h := r.Edges.Holder; h != nil {
				out[r.Id] = h.TenantId
			}
		}

		return out, err
	},
	pd.BindingDomain: func(ctx context.Context, db *ent.Client, ks []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
		rs, err := db.Binding.Query().Where(entbinding.IdIn(ks...)).
			WithRole(func(q *ent.RoleQuery) { q.Select(entrole.FieldTenantId) }).All(ctx)
		out := make(map[uuid.UUID]uuid.UUID, len(rs))
		for _, r := range rs {
			if v := r.Edges.Role; v != nil {
				out[r.Id] = v.TenantId
			}
		}

		return out, err
	},
}

// wordless is `sink` with what each row keeps of its request kept without the
// words it was asked by (#280).
//
// # What it was
//
// For a read that answered, payday keeps the request in the row, whole, up to
// 8 KiB: "what says how a page of people came to be shown". So a row held the
// alias a `Get` named somebody by, the fragment a `Search` looked for, the
// address of an `EmailService/Get`, somebody's subject at a provider -- and,
// for an `ApiKeyService/Get` by the `secret` arm of its reference, the key's
// verifier, which no port answers and the trail does not hold. The row is
// archived with the trail, and it is not reached when somebody is forgotten:
// their alias, their address and the name they were searched by stayed for
// the record's window, which is for ever unless a deployment says. The manual
// said a row holds identifiers.
//
// # What a row keeps
//
// How it was asked, and not what by. The request as its own message, so that
// a review reads it as it always did, with each value that is a word taken
// out and the field it was in left where the message can say a field was
// given at all:
//
//   - an identifier stays: the key of a row, which is not a word and is what
//     `subjects` holds already;
//   - a boolean, a number and an enum stay: what was selected, a page's size,
//     which way round it was read;
//   - every string goes, and every run of bytes that is no identifier: a
//     name, an address, a subject, a number, a fragment, a cursor, a secret;
//   - a map keyed by words goes whole, which is a filter by labels;
//   - and a field the schema declares `secret` or `personal` goes whatever it
//     looks like ([wordsDeclared]).
//
// By its shape where the schema does not say, because nothing declares that
// a string is *not* somebody's: `HolderSearchRequest.q` carries no
// declaration and is a fragment of a name. So a string added to a request
// tomorrow is out until somebody argues it in.
//
// A request this cannot read as the message its method takes is not kept at
// all.
func wordless(sink func(context.Context, []access.Record) error) func(context.Context, []access.Record) error {
	return func(ctx context.Context, rs []access.Record) error {
		// A call's rows share what they keep, one row a tenant; each is read
		// once, and none is changed where the writer still holds it to write
		// again.
		kept := map[*pdpb.AccessDetail]*pdpb.AccessDetail{}

		out := make([]access.Record, len(rs))
		for i, r := range rs {
			out[i] = r
			if len(r.Detail.GetRequest()) == 0 {
				continue
			}

			d, ok := kept[r.Detail]
			if !ok {
				d = pdpb.AccessDetail_builder{
					Address: r.Detail.GetAddress(),
					Reason:  r.Detail.GetReason(),
					Request: asked(r.Action, r.Detail.GetRequest()),
				}.Build()
				kept[r.Detail] = d
			}
			out[i].Detail = d
		}

		return sink(ctx, out)
	}
}

// asked is the request `b` of the RPC `method` without its words, as a row
// keeps it ([wordless]); nil for one that is not a request of that RPC.
func asked(method string, b []byte) []byte {
	name, rpc, ok := strings.Cut(strings.TrimPrefix(method, "/"), "/")
	if !ok {
		return nil
	}
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		return nil
	}
	sd, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil
	}
	md := sd.Methods().ByName(protoreflect.Name(rpc))
	if md == nil {
		return nil
	}
	t, err := protoregistry.GlobalTypes.FindMessageByName(md.Input().FullName())
	if err != nil {
		return nil
	}

	m := t.New()
	if err := proto.Unmarshal(b, m.Interface()); err != nil {
		return nil
	}
	unword(m)

	out, err := proto.Marshal(m.Interface())
	if err != nil {
		return nil
	}

	return out
}

// unword takes the words out of `m`, all the way down; see [wordless] for
// which values those are. A field that had a word keeps that it was given,
// where its message can say so: an arm of a reference, a field with presence.
func unword(m protoreflect.Message) {
	// What this build does not know of a message it cannot tell a word in.
	m.SetUnknown(nil)

	words := wordsDeclared()
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case words[fd.FullName()]:
			// Empty, and still given: a new value of the field's own kind.
			m.Set(fd, blank(m, fd))

		case fd.IsMap():
			if fd.MapKey().Kind() == protoreflect.StringKind {
				m.Clear(fd)
				break
			}
			v.Map().Range(func(k protoreflect.MapKey, e protoreflect.Value) bool {
				if w, ok := unworded(fd.MapValue(), e); ok {
					v.Map().Set(k, w)
				}
				return true
			})

		case fd.IsList():
			l := v.List()
			for i := range l.Len() {
				if w, ok := unworded(fd, l.Get(i)); ok {
					l.Set(i, w)
				}
			}

		default:
			if w, ok := unworded(fd, v); ok {
				m.Set(fd, w)
			}
		}

		return true
	})
}

// unworded is one value of the field `fd` without its words, and whether it
// is another value than `v`: a message is emptied of them where it is.
func unworded(fd protoreflect.FieldDescriptor, v protoreflect.Value) (protoreflect.Value, bool) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(""), true
	case protoreflect.BytesKind:
		if _, err := pdid.From(v.Bytes()); err == nil {
			return v, false
		}
		return protoreflect.ValueOfBytes([]byte{}), true
	case protoreflect.MessageKind, protoreflect.GroupKind:
		unword(v.Message())
	}

	return v, false
}

// blank is the field `fd` of `m` with nothing in it, for one the schema
// declares a word whatever it holds.
func blank(m protoreflect.Message, fd protoreflect.FieldDescriptor) protoreflect.Value {
	if fd.Kind() == protoreflect.BytesKind && !fd.IsList() {
		return protoreflect.ValueOfBytes([]byte{})
	}

	return m.NewField(fd)
}

// wordsDeclared is every field the schema says is a word, by its full name: one
// declared `secret` or `personal`, and one that stands for such a field in a
// message made for its row.
//
// The declaration is on the row (`ApiKey.secret`, `Holder.alias`), and a
// request names a row by the same fields in messages of its own, which the
// generator writes with the row's name in front and no declaration:
// `ApiKeyRef.secret`, `HolderRefBySlug.alias`, `HolderFilter.labels`. So a
// field of a message named after a row that is called what a declared field
// of that row is called, and holds what it holds, is that field. `ApiKeySelect.secret`
// is not: it is a boolean that asks for the column.
var wordsDeclared = sync.OnceValue(func() map[protoreflect.FullName]bool {
	out := map[protoreflect.FullName]bool{}

	var all []protoreflect.MessageDescriptor
	rows := map[protoreflect.FullName]map[protoreflect.Name]protoreflect.FieldDescriptor{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		ms := fd.Messages()
		for i := range ms.Len() {
			md := ms.Get(i)
			all = append(all, md)

			fs := md.Fields()
			for j := range fs.Len() {
				v, _ := proto.GetExtension(fs.Get(j).Options(), pdpb.E_Field).(*pdpb.Field)
				if !v.GetSecret() && !v.GetPersonal() {
					continue
				}
				if rows[md.FullName()] == nil {
					rows[md.FullName()] = map[protoreflect.Name]protoreflect.FieldDescriptor{}
				}
				rows[md.FullName()][fs.Get(j).Name()] = fs.Get(j)
				out[fs.Get(j).FullName()] = true
			}
		}
		return true
	})

	for _, md := range all {
		// The row a message is made for: the longest name of one that its own
		// begins with, in its package.
		var of protoreflect.FullName
		for row := range rows {
			if row == md.FullName() || row.Parent() != md.FullName().Parent() {
				continue
			}
			if strings.HasPrefix(string(md.Name()), string(row.Name())) && len(row) > len(of) {
				of = row
			}
		}
		if of == "" {
			continue
		}

		fs := md.Fields()
		for j := range fs.Len() {
			f := fs.Get(j)
			d, ok := rows[of][f.Name()]
			if !ok || d.Kind() != f.Kind() || d.IsList() != f.IsList() || d.IsMap() != f.IsMap() {
				continue
			}
			if m := d.Message(); m != nil && !d.IsMap() && m.FullName() != f.Message().FullName() {
				continue
			}
			out[f.FullName()] = true
		}
	}

	return out
})

// whole is the access record's sink on `db`, writing what it is handed whole
// or not at all (#280).
//
// # What it was
//
// The generated sink writes its rows five hundred a statement, each statement
// by itself, and mints a row's identifier as it writes it. payday's writer
// puts every row of a flush that failed back to be tried again. So a flush of
// more than five hundred rows that failed after its first statement left
// those rows written, and wrote them again a second later under new
// identifiers, and again for as long as the failure lasted: 501 reads waiting
// and two failures were 1,501 rows. It is what the end of a database's outage
// looks like, when a great many reads have waited.
//
// One transaction, so the rows the writer tries again were not written. It is
// begun here and never joined: the writer calls this once a call has ended,
// or from its own loop, and neither is inside a transaction of the call's. A
// write's row, which is, does not come through here at all -- the recorder
// writes it in the write's own (`pd.AccessRecorder`).
func whole(db *ent.Client) func(context.Context, []access.Record) error {
	return func(ctx context.Context, rs []access.Record) error {
		tx, err := db.Tx(ctx)
		if err != nil {
			return fmt.Errorf("write the access record: %w", err)
		}
		defer tx.Rollback()

		if err := pd.AccessSink(tx.Client())(ctx, rs); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("write the access record: %w", err)
		}

		return nil
	}
}
