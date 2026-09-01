package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	app "github.com/lesomnus/roster/rstr"
)

// TestTheRecordOfAnEraseBelongsToWhoseRowItWas.
//
// The trail is filed under the tenant of the **thing that changed**, so that
// whoever holds that thing can read what was done to it. That is the whole
// reason the recorder pays a read on the write path rather than filing
// everything under whoever asked.
//
// It did not hold for an erase, and roster is a deployment where that is every
// erase **of a person**: no entity holding somebody's data declares `hard:`, so
// every one of those rows is soft-erased. Three do -- `Tenant`, `Audit` and
// `Outbox`, all three payday's own -- and none of them is anybody. This comment
// used to say "nothing here declares `hard:`", which was easier to read and
// false. The
// recorder reads the row it is recording through the bare server, and every
// read the bare server makes narrows to the rows still here -- so the row the
// erase had just stamped was NotFound to the record of that very erase. The
// record then took the path meant for a row that is really gone: filed under
// the **actor's** tenant, with an empty value.
//
// Which is exactly the wrong way round. An operator erasing somebody in a
// customer's tenant left a record that the operator's own tenant could read
// and the customer's could not -- so the party with the strongest claim to
// know that their person was removed is the one the row was hidden from, and
// nothing anywhere said so.
//
// Fixed upstream, in the generator that writes the recorder: a NotFound for a
// soft entity is retried past the erased filter, on the same transaction. This
// is roster asking whether that arrived, which is worth its own test because
// the answer is a property of the pinned payday and not of anything here.
func TestTheRecordOfAnEraseBelongsToWhoseRowItWas(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Somebody in contoso, erased by somebody who is not in contoso -- which is what
	// makes the two tenants tell apart. The operator is fabrikam's, and through
	// the ungated server, so nothing narrows the write itself.
	who := b.holder(t, ctx, b.Contoso, "leaver")
	operator := b.holder(t, ctx, b.Fabrikam, "ops")

	res, err := b.Ungated.Holder().Erase(b.asNobody(ctx, operator, b.Fabrikam),
		app.HolderRef_builder{Id: who.Bytes()}.Build())
	x.NoError(err)
	x.True(res.GetErased())

	// The erase's own row. The Add that made the person is recorded against
	// the same object, and it is the one that was always filed correctly --
	// asking for both would be asking a question two rows can answer.
	vs, err := b.Ent.Audit.Query().
		Where(
			entaudit.ObjectIdEQ(who.Uuid()),
			entaudit.ActionEQ("/roster.HolderService/Erase"),
		).
		All(ctx)
	x.NoError(err)
	x.Len(vs, 1, "the erase left no record, or left more than one")

	v := vs[0]

	// Under contoso, whose row it was -- not under fabrikam, who did it.
	x.Equal(b.Contoso.Uuid(), v.TenantId,
		"the record of an erase was filed away from the tenant whose row was erased")
	x.Equal(operator.Uuid(), v.ActorId)

	// And with the row in it. The value is the row as the event left it, which
	// for a soft erase is everything of the moment before plus the stamp -- so
	// an empty one is the record saying nothing was there to read, which was
	// the other half of the same mistake.
	x.NotEmpty(v.Value, "the record of an erase carries nothing of what was erased")

	// The three columns the trail declares NOT NULL are bytes rather than nil,
	// whichever database this is running on. On SQLite a nil is stored as an
	// empty blob and nothing notices; on PostgreSql it is SQL NULL and the
	// write is refused, which is the shape that has to be asserted here rather
	// than discovered by an operator.
	x.NotNil(v.TraceID)
	x.NotNil(v.Patch)
	x.NotNil(v.Value)
}

// TestAnEraseThroughTheWallIsFiledTheSameWay, so that what the test above
// pinned is not an artefact of writing through the ungated server.
func TestAnEraseThroughTheWallIsFiledTheSameWay(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Contoso, "leaver")
	admin := b.holder(t, ctx, b.Contoso, "admin")

	_, err := b.Walled.Holder().Erase(b.as(ctx, admin, b.Contoso),
		app.HolderRef_builder{Id: who.Bytes()}.Build())
	x.NoError(err)

	vs, err := b.Ent.Audit.Query().
		Where(
			entaudit.ObjectIdEQ(who.Uuid()),
			entaudit.ActionEQ("/roster.HolderService/Erase"),
		).
		All(ctx)
	x.NoError(err)
	x.Len(vs, 1)
	x.Equal(b.Contoso.Uuid(), vs[0].TenantId)
	x.NotEmpty(vs[0].Value)
}
