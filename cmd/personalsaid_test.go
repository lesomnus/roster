package cmd_test

import (
	"bytes"
	"encoding/base64"
	"slices"
	"testing"

	"github.com/protobuf-orm/protobuf-orm/ormpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	entaudit "github.com/lesomnus/roster/internal/ent/audit"
	entcredential "github.com/lesomnus/roster/internal/ent/credential"
	entholder "github.com/lesomnus/roster/internal/ent/holder"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/forget"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/vouch"
)

// TestEveryFieldSaysWhetherItIsSomebodys: a field that says neither
// `personal: true` nor `personal: false` is kept on the trail as a value, and
// nobody decided that it should be. Seven that are somebody's words had said
// neither -- a key's `alias` and `desc`, a credential's `name`, a relation's
// and a binding's `labels`, a nomination's `name` and `labels` -- and the
// trail's lines held `Hana Kim's laptop` for as long as the trail kept them.
// `pd doctor` asks only about an entity that already declares a field
// personal, and none of those five did, so nothing asked.
//
// So every field of every entity says one or the other, and the next field
// does or fails here. Exempt by what it is, and not by its name, is a field
// the question means nothing of: an edge and an identifier, which are what
// the trail names a row by; a moment; a number, a flag and an enum. What is
// asked is what `pd doctor` asks of an entity that holds people -- a string,
// bytes, a document, and a list or a map of them -- of every entity. A field
// declared `secret` has said what it is: generation refuses the two on one
// field.
//
// payday's own kinds are payday's to declare, since an overlay may not say
// anything of a field of payday's: the trail, the access record, the archive,
// the outbox, what is owed and the seals are passed by, by what their entity
// says it is. A holder and a tenant are payday's too and are asked, because
// this schema adds fields to them; payday's own are declared there already.
func TestEveryFieldSaysWhetherItIsSomebodys(t *testing.T) {
	x := require.New(t)

	// What the question means something of, by the field's type.
	text := func(f protoreflect.FieldDescriptor) bool {
		of := f
		if f.IsMap() {
			of = f.MapValue()
		}

		switch of.Kind() {
		case protoreflect.StringKind:
			return true

		case protoreflect.BytesKind:
			// An identifier is bytes the schema says are one.
			o, _ := proto.GetExtension(f.Options(), ormpb.E_Field).(*ormpb.FieldOptions)

			return o.GetType() != ormpb.Type_TYPE_UUID

		case protoreflect.MessageKind:
			// An edge names another row, and a moment is a moment.
			return !proto.HasExtension(f.Options(), ormpb.E_Edge) && of.Message().FullName() != "google.protobuf.Timestamp"
		}

		return false
	}

	asked, entities := 0, 0
	silent := []string{}
	protoregistry.GlobalFiles.RangeFilesByPackage("roster", func(file protoreflect.FileDescriptor) bool {
		for i := range file.Messages().Len() {
			m := file.Messages().Get(i)
			if !proto.HasExtension(m.Options(), pdpb.E_Entity) {
				continue
			}
			e, _ := proto.GetExtension(m.Options(), pdpb.E_Entity).(*pdpb.Entity)
			if own := e.GetOwn(); own != pdpb.Own_OWN_UNSPECIFIED && own != pdpb.Own_OWN_HOLDER && own != pdpb.Own_OWN_TENANT {
				continue
			}
			entities++

			for j := range m.Fields().Len() {
				f := m.Fields().Get(j)
				if !text(f) {
					continue
				}
				asked++

				o, _ := proto.GetExtension(f.Options(), pdpb.E_Field).(*pdpb.Field)
				if !o.HasPersonal() && !o.GetSecret() {
					silent = append(silent, string(m.Name())+"."+string(f.Name()))
				}
			}
		}

		return true
	})
	x.Greater(entities, 20, "few entities were read, so this proves little")
	x.Greater(asked, 60, "few fields were asked, so this proves little")

	slices.Sort(silent)
	x.Empty(silent, "these say neither `personal: true` nor `personal: false`, so the trail keeps what is written in them and nobody decided it should. "+
		"Say which beside each: [(payday.field) = {personal: true}] for words about a person, [(payday.field) = {personal: false}] for what is about no one")
}

// TestWhatIsWrittenAboutSomebodyIsNotOnTheTrail is what the declarations
// change, seen on a run, for each of the seven fields that had said nothing
// and are somebody's words (`TestEveryFieldSaysWhetherItIsSomebodys`).
//
// The trail's line about the row said them: `zq-laptop`, `Hana Kim's
// laptop`, `zq-phone`, `manager-of-zq`, `zq asked on 11-02`, `zq-bot`. It
// says now that the field was set and not what to, at the content level a
// deployment has unless it says otherwise and at the one that keeps the row;
// the keyed digest leaves the field out, and the row's seal covers it
// instead; the row still answers it to whoever may read the row, and a key
// and a credential are still found by the name; and once the person is
// forgotten the words are nowhere, since the row was their one copy.
//
// And a tenant's hold is the other way on purpose: why a hold was placed is
// on the trail in words, which is the point of writing one.
func TestWhatIsWrittenAboutSomebodyIsNotOnTheTrail(t *testing.T) {
	for _, level := range []string{"", "value"} {
		name := "at the content level a deployment has unless it says"
		if level != "" {
			name = "and where the trail keeps the row"
		}

		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			var cfg cmd.Config
			b, ctx := build(t, func(c *cmd.Config) {
				c.Audit.Content = level
				c.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
				cfg = *c
			})
			contoso := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

			who := b.holder(t, ctx, b.Contoso, "zqhana")
			ref := app.HolderRef_builder{Id: who.Bytes()}.Build()
			bystander := app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "bystander").Bytes()}.Build()
			reader := roleRef(b.role(t, ctx, "reader", getHolder))

			key, err := b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
				Holder: ref, Alias: "zq-laptop", Desc: "Hana Kim's laptop", Methods: []string{getMe}, Secret: []byte("0123456789abcdef0123456789abcdef"),
			}.Build())
			x.NoError(err)
			_, err = b.Ungated.Credential().Enrol(ctx, app.CredentialEnrolRequest_builder{Ref: ref, Kind: vouch.KindTotp, Name: "zq-phone"}.Build())
			x.NoError(err)
			factor, err := b.Ent.Credential.Query().Where(entcredential.HasHolderWith(entholder.IdEQ(who.Uuid()))).Only(ctx)
			x.NoError(err)
			relation, err := b.Ungated.Relation().Add(ctx, app.RelationAddRequest_builder{
				Holder: ref, Other: bystander, Labels: map[string]string{"is": "manager-of-zq"},
			}.Build())
			x.NoError(err)
			binding, err := b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
				Role: reader, Holder: ref, Labels: map[string]string{"why": "zq asked on 11-02"},
			}.Build())
			x.NoError(err)
			nomination, err := b.Ungated.Nomination().Add(ctx, app.NominationAddRequest_builder{
				Tenant: contoso, BorrowerId: pdid.New(pd.HolderDomain).Bytes(), ActsAs: ref,
				Name: "zq-bot", Labels: map[string]string{"note": "zq-note"},
			}.Build())
			x.NoError(err)

			said := []struct {
				what   string
				row    pdid.Id
				words  []string
				fields []uint32
			}{
				{"a key's alias and description", mustId(t, key.GetId()), []string{"zq-laptop", "Hana Kim's laptop"}, []uint32{4, 6}},
				{"a credential's name", pdid.Id(factor.Id), []string{"zq-phone"}, []uint32{5}},
				{"a relation's labels", mustId(t, relation.GetId()), []string{"manager-of-zq"}, []uint32{7}},
				{"a binding's labels", mustId(t, binding.GetId()), []string{"zq asked on 11-02"}, []uint32{7}},
				{"a nomination's name and labels", mustId(t, nomination.GetId()), []string{"zq-bot", "zq-note"}, []uint32{5, 7}},
			}

			for _, v := range said {
				lines, err := b.Ent.Audit.Query().Where(entaudit.ObjectIdEQ(v.row.Uuid())).All(ctx)
				x.NoError(err)
				x.NotEmpty(lines, "nothing on the trail is about %s, so this proves nothing", v.what)

				for _, line := range lines {
					for _, word := range v.words {
						x.False(bytes.Contains(line.Patch, []byte(word)), "the trail's line %s says %q in what was written: %s", line.Action, word, v.what)
						x.False(bytes.Contains(line.Value, []byte(word)), "the trail's line %s says %q in the row it keeps: %s", line.Action, word, v.what)
					}
					for _, n := range v.fields {
						x.Contains(line.Fields, n, "the trail's line %s does not say that field %d was set: %s", line.Action, n, v.what)
						x.NotContains(line.DigestFields, n, "the trail's digest covers field %d, which is somebody's: %s", n, v.what)
					}
				}
			}

			// The row still answers them, to whoever may read it, and a key and
			// a factor are still found by what they are called.
			found, err := b.Ungated.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{Ref: app.ApiKeyRef_builder{
				Slug: app.ApiKeyRefBySlug_builder{Holder: ref, Alias: proto.String("zq-laptop")}.Build(),
			}.Build()}.Build())
			x.NoError(err, "a key is not found by its alias")
			x.Equal("Hana Kim's laptop", found.GetDesc())
			keys, err := b.Ungated.ApiKey().List(ctx, app.ApiKeyListRequest_builder{
				Filters: []*app.ApiKeyFilter{app.ApiKeyFilter_builder{Holder: ref}.Build()},
			}.Build())
			x.NoError(err)
			x.Len(keys.GetItems(), 1)
			x.Equal("zq-laptop", keys.GetItems()[0].GetAlias(), "a list does not answer a key's alias")
			named, err := b.Ungated.Credential().Get(ctx, app.CredentialGetRequest_builder{Ref: app.CredentialRef_builder{
				Kind: app.CredentialRefByKind_builder{Holder: ref, Kind: proto.String(vouch.KindTotp), Name: proto.String("zq-phone")}.Build(),
			}.Build()}.Build())
			x.NoError(err, "a credential is not found by its name")
			x.Equal("zq-phone", named.GetName())
			bound, err := b.Ungated.Binding().Get(ctx, app.BindingGetRequest_builder{Ref: app.BindingRef_builder{Id: binding.GetId()}.Build()}.Build())
			x.NoError(err)
			x.Equal("zq asked on 11-02", bound.GetLabels()["why"])
			related, err := b.Ungated.Relation().Get(ctx, app.RelationGetRequest_builder{Ref: app.RelationRef_builder{Id: relation.GetId()}.Build()}.Build())
			x.NoError(err)
			x.Equal("manager-of-zq", related.GetLabels()["is"])
			nominated, err := b.Ungated.Nomination().Get(ctx, app.NominationGetRequest_builder{Ref: app.NominationRef_builder{Id: nomination.GetId()}.Build()}.Build())
			x.NoError(err)
			x.Equal("zq-bot", nominated.GetName())
			x.Equal("zq-note", nominated.GetLabels()["note"])

			// And a check of the rows against the trail finds nothing: each row
			// is what its newest line and its seal say it is.
			x.NoError(cli.Cmd(&cfg).Run(ctx, []string{"trail", "rows"}), "a row that declares a field personal is a finding of `roster trail rows`")

			// A hold is on the trail in its own words, on purpose.
			x.NoError(forget.Hold(ctx, b.Ungated, b.Fabrikam, "Kim v. Fabrikam, 2026-123"))
			held, err := b.Ent.Audit.Query().Where(entaudit.ObjectIdEQ(b.Fabrikam.Uuid())).All(ctx)
			x.NoError(err)
			why := false
			for _, line := range held {
				why = why || bytes.Contains(line.Patch, []byte("Kim v. Fabrikam, 2026-123"))
			}
			x.True(why, "the trail does not say why a hold was placed, which is what one is written for")

			// Forgotten, and the words are nowhere: the rows went with them, and
			// no line of the trail ever held them.
			p, err := cmd.TrailPolicy(ctx, cfg)
			x.NoError(err)
			cs, err := cfg.Audit.Contents()
			x.NoError(err)
			_, err = forget.Forget(ctx, b.Ent, who, forget.Held(p, b.Ent), cs, b.Queue)
			x.NoError(err)
			x.Empty(b.namingTheForgotten(t, ctx), "a forgotten person still has what was written about them")

			every, err := b.Ent.Audit.Query().All(ctx)
			x.NoError(err)
			for _, line := range every {
				for _, v := range said {
					for _, word := range v.words {
						x.False(bytes.Contains(line.Patch, []byte(word)) || bytes.Contains(line.Value, []byte(word)),
							"after the forget, the trail's line %s still says %q: %s", line.Action, word, v.what)
					}
				}
			}
			x.NoError(cli.Cmd(&cfg).Run(ctx, []string{"trail", "rows"}), "after a forget, `roster trail rows` finds something")
		})
	}
}
