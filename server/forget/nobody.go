package forget

import (
	"context"
	"fmt"
	"strings"

	"uuid"

	"github.com/protobuf-orm/ent/dialect/sql"

	"github.com/lesomnus/roster/internal/ent/apikey"
	"github.com/lesomnus/roster/internal/ent/binding"
	"github.com/lesomnus/roster/internal/ent/consent"
	"github.com/lesomnus/roster/internal/ent/continuation"
	"github.com/lesomnus/roster/internal/ent/credential"
	"github.com/lesomnus/roster/internal/ent/delegation"
	"github.com/lesomnus/roster/internal/ent/email"
	"github.com/lesomnus/roster/internal/ent/group"
	"github.com/lesomnus/roster/internal/ent/groupmembership"
	"github.com/lesomnus/roster/internal/ent/holder"
	"github.com/lesomnus/roster/internal/ent/identity"
	"github.com/lesomnus/roster/internal/ent/link"
	"github.com/lesomnus/roster/internal/ent/nomination"
	"github.com/lesomnus/roster/internal/ent/phone"
	"github.com/lesomnus/roster/internal/ent/predicate"
	"github.com/lesomnus/roster/internal/ent/relation"
	"github.com/lesomnus/roster/internal/ent/role"
	"github.com/lesomnus/roster/internal/ent/sitemembership"
	"github.com/lesomnus/roster/internal/ent/teammembership"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
)

// Nobody is what refuses a row written for somebody forgotten: a recorder,
// told of every write inside the transaction that makes it, that asks of a
// row that names a person whether that person is anybody still.
//
// # What was wrong
//
// A forgotten person's row stays, blank ([Forget]). So a foreign key to it is
// satisfied by somebody forgotten as by anybody, and what kept a row from
// being written for one was a read: every write that names somebody reads
// them first, and somebody erased is read by no server. A read and then a
// write is two moments. A forget that committed between them left the row
// hanging off a blank person -- a binding, a place in a site, a team or a
// group, a nomination, each seen on a run -- and nothing looks at somebody
// forgotten again: a pass finds people by the name a forgotten one has not
// got. An address left that way stayed taken for everybody else of the
// tenant (#280).
//
// # Why it is asked here, and after the write
//
// Because there is no one place before it. Each kind's write is its own,
// with its own reads, in `server/core` and below; some are behind the lock
// on the person's row (`server/core/only.go`) and most are not; and a rule
// written on the verbs is written sixteen times, where the verb that forgets
// is the hole (`core.living` says the same of a tenant). What every such
// write has in common is where it ends: in a transaction, with a row. A
// recorder is told there.
//
// And after, because that is when the answer cannot change. On SQLite a
// transaction is the one writer, so what it reads after its own insert is
// what is so when it commits. On PostgreSQL the insert itself holds the
// person's row against a forget until it commits -- the foreign key's own
// lock -- and a forget takes that row for itself before it reads anything
// ([mine]): one already under way is waited for by the insert, and is found
// here; one that comes after waits for this transaction, and removes the row
// with everything else of theirs.
//
// # What it refuses, and what it leaves
//
// A row added, of every kind that names a person, and a nomination pointed
// at somebody else, which is the one edge to a person a write can move.
// [naming] is the list, and `TestEveryRowThatNamesAPersonIsAskedAboutThem`
// holds it to the schema: a table keyed to a holder tomorrow, and not asked
// about here, fails there.
//
// Somebody forgotten, and not somebody erased. Whether a row may be written
// for somebody who has left and may come back is each write's own rule, and
// most refuse it; a row that slips past one hangs off somebody a restore
// brings back or the next pass forgets, rows and all. Only a forgotten
// person keeps what is written for them for good.
//
// The write is undone with the refusal where the server that made it opened
// the transaction, and where a caller did, by the caller, whom it reaches as
// the write's own error ([bare.Recorder]).
//
// It reaches them as `Internal`, with these words in it. That is the
// generated server's: whatever a recorder answers is reported so
// (`bare.record`), on the reasoning that a recorder keeps the trail and a
// trail not kept is the server's fault. `NotFound` is what the same write is
// answered a moment later, when its own read finds nobody, and would be the
// better word here; a recorder cannot say it.
func Nobody() bare.Recorder { return nobody{} }

type nobody struct{}

func (nobody) Record(ctx context.Context, s bare.Server, c bare.Change) error {
	of, ok := naming[c.By]
	if !ok {
		return nil
	}
	row, ok := c.Key.(uuid.UUID)
	if !ok {
		return nil
	}

	gone, err := s.Db.Holder.Query().
		Where(holder.AliasEQ(""), holder.DateErasedNotNil(), namedBy(of, row)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if gone {
		return fmt.Errorf("%s: whoever this names was forgotten while it was being written, and nothing is written for somebody forgotten",
			strings.Join(of.fields(), ", "))
	}

	return nil
}

// names is one kind of row that names a person: its table, and the columns
// of it that are keyed to a holder.
type names struct {
	table   string
	columns []string
}

// fields is the columns as a request calls them: `holder`, `acts_as`.
func (n names) fields() []string {
	out := make([]string, len(n.columns))
	for i, c := range n.columns {
		out[i] = strings.TrimSuffix(c, "_id")
	}

	return out
}

// namedBy is the people the row `row` of a kind names, as a predicate on
// them. Subqueries on the kind's own columns and not edge predicates, for
// [left]'s reason and one more: a holder has no edge back to what names it.
func namedBy(of names, row uuid.UUID) predicate.Holder {
	return func(s *sql.Selector) {
		t := sql.Table(of.table)

		by := make([]*sql.Predicate, len(of.columns))
		for i, c := range of.columns {
			by[i] = sql.Exists(sql.Select(t.C(c)).From(t).Where(sql.And(
				sql.EQ(t.C(holder.FieldId), row),
				sql.ColumnsEQ(t.C(c), s.C(holder.FieldId)),
			)))
		}

		s.Where(sql.Or(by...))
	}
}

// naming is every write that names a person, by the method that makes it
// ([bare.Change.By]): an `Add` of each kind keyed to a holder that a server
// adds, and the two writes that move a nomination's `acts_as`, which is the
// only such edge that is not fixed when its row is made. A session is keyed
// to a holder and has no `Add`: its store writes it through ent, past every
// recorder (`server/session`).
//
// Written out, for [subjects]' reason, and held to the schema by a test; see
// [Nobody]. A consent is here though a forget leaves it: one that is there is
// kept past its account, and one is not begun for somebody who is nobody.
var naming = map[string]names{
	app.ApiKeyService_Add_FullMethodName:          {apikey.Table, []string{apikey.HolderColumn}},
	app.BindingService_Add_FullMethodName:         {binding.Table, []string{binding.HolderColumn}},
	app.ConsentService_Add_FullMethodName:         {consent.Table, []string{consent.HolderColumn}},
	app.ContinuationService_Add_FullMethodName:    {continuation.Table, []string{continuation.HolderColumn}},
	app.CredentialService_Add_FullMethodName:      {credential.Table, []string{credential.HolderColumn}},
	app.DelegationService_Add_FullMethodName:      {delegation.Table, []string{delegation.HolderColumn}},
	app.EmailService_Add_FullMethodName:           {email.Table, []string{email.HolderColumn}},
	app.GroupService_Add_FullMethodName:           {group.Table, []string{group.AppColumn}},
	app.GroupMembershipService_Add_FullMethodName: {groupmembership.Table, []string{groupmembership.HolderColumn}},
	app.IdentityService_Add_FullMethodName:        {identity.Table, []string{identity.HolderColumn}},
	app.LinkService_Add_FullMethodName:            {link.Table, []string{link.HolderColumn}},
	app.PhoneService_Add_FullMethodName:           {phone.Table, []string{phone.HolderColumn}},
	app.RelationService_Add_FullMethodName:        {relation.Table, []string{relation.HolderColumn, relation.OtherColumn}},
	app.RoleService_Add_FullMethodName:            {role.Table, []string{role.AppColumn}},
	app.SiteMembershipService_Add_FullMethodName:  {sitemembership.Table, []string{sitemembership.HolderColumn}},
	app.TeamMembershipService_Add_FullMethodName:  {teammembership.Table, []string{teammembership.HolderColumn}},

	app.NominationService_Add_FullMethodName:   {nomination.Table, []string{nomination.ActsAsColumn}},
	app.NominationService_Patch_FullMethodName: {nomination.Table, []string{nomination.ActsAsColumn}},
	app.NominationService_Apply_FullMethodName: {nomination.Table, []string{nomination.ActsAsColumn}},
}
