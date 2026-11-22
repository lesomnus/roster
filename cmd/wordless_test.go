package cmd_test

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/pd"
)

// What a row of the access record keeps of the request it is of (#280).
//
// For a read that answered, payday keeps the request, whole. So a row held the
// alias a `Get` named somebody by, the fragment of a `Search`, the address of
// an `EmailService/Get`, a subject at a provider -- and a key's verifier, for a
// `Get` by the `secret` arm of its reference. It is archived with the trail and
// is not reached when somebody is forgotten, and the manual said a row holds
// identifiers.

// keptOf is what `r` keeps of its request, read as `into`, and the row's
// `value` as it is stored.
func keptOf(t *testing.T, r *ent.Access, into proto.Message) []byte {
	t.Helper()
	x := require.New(t)

	d := &pdpb.AccessDetail{}
	x.NoError(proto.Unmarshal(r.Value, d))
	x.NoError(proto.Unmarshal(d.GetRequest(), into))

	return r.Value
}

// TestARowKeepsHowItWasAskedAndNotTheWordsItWasAskedBy: each kind of read
// leaves a row that says how it was asked -- which arm of a reference, which
// filters, what was selected, how many -- and none of the words.
//
// # What it was
//
// Seen on a run: `{"ref":{"slug":{"alias":"alice","tenant":{"alias":"contoso"}}}}`
// for a `Get` by name, `"q":"ali"` for a search, the address of a lookup by
// address, the provider and the subject of a lookup by subject, and
// `{"ref":{"secret":"K0GRxHqG..."}}` for a key read by its verifier.
//
// An identifier is not a word and stays: it is what `subjects` holds already.
func TestARowKeepsHowItWasAskedAndNotTheWordsItWasAskedBy(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	alice := b.holder(t, ctx, b.Contoso, "alice")
	b.mayAnything(b.ContosoUser, b.Contoso)

	address, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
		Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(), Address: "alice@contoso.example",
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Phone().Add(ctx, app.PhoneAddRequest_builder{
		Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(), Number: "+82 10-5550-0100", Kind: "mobile",
	}.Build())
	x.NoError(err)
	b.identity(t, ctx, alice, "entra", "sub-of-alice")
	_, verifier, err := keys.Mint(keys.PrefixTenant)
	x.NoError(err)
	_, err = b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(), Alias: "laptop", Secret: verifier,
		Methods: []string{app.HolderService_Get_FullMethodName},
	}.Build())
	x.NoError(err)
	filed(t, ctx, b.Server, "")

	conn := served(t, b.Server)
	as := asOverTheWire(ctx, b.ContosoUser)
	H := app.NewHolderServiceClient(conn)

	contoso := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	words := []string{"alice", "contoso", "ali", "eng", "dept", "entra", "sub-of-alice", "5550", "mobile", string(verifier)}

	// kept asserts the one row of `method` keeps `want` of its request, and
	// none of the words anywhere in what it stores.
	kept := func(t *testing.T, method string, into, want proto.Message) {
		t.Helper()
		x := require.New(t)

		rs := filed(t, ctx, b.Server, method)
		x.Len(rs, 1)
		value := keptOf(t, rs[0], into)
		x.True(proto.Equal(want, into), "kept %s\nwant %s", protojson.Format(into), protojson.Format(want))
		for _, w := range words {
			x.NotContains(string(value), w, "the row keeps a word it was asked by")
		}
	}

	t.Run("a Get by name", func(t *testing.T) {
		_, err := H.Get(as, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
				Alias: z.Ptr("alice"), Tenant: app.TenantRef_builder{Alias: z.Ptr("contoso")}.Build(),
			}.Build()}.Build(),
			Select: app.HolderSelect_builder{Alias: z.Ptr(true), Tenant: app.TenantSelect_builder{}.Build()}.Build(),
		}.Build())
		require.NoError(t, err)

		// By `slug`, with an alias and a tenant named by alias: that much, and
		// the `select`.
		kept(t, app.HolderService_Get_FullMethodName, &app.HolderGetRequest{}, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
				Alias: z.Ptr(""), Tenant: app.TenantRef_builder{Alias: z.Ptr("")}.Build(),
			}.Build()}.Build(),
			Select: app.HolderSelect_builder{Alias: z.Ptr(true), Tenant: app.TenantSelect_builder{}.Build()}.Build(),
		}.Build())
	})

	t.Run("a Get by identifier, whole", func(t *testing.T) {
		req := app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Id: alice.Bytes()}.Build(),
			Select: app.HolderSelect_builder{Alias: z.Ptr(true), Profile: z.Ptr(true)}.Build(),
		}.Build()
		_, err := H.Get(as, req)
		require.NoError(t, err)

		kept(t, app.HolderService_Get_FullMethodName, &app.HolderGetRequest{}, req)
	})

	t.Run("a Search", func(t *testing.T) {
		_, err := H.Search(as, app.HolderSearchRequest_builder{
			Q:    z.Ptr("ali"),
			Size: 5,
			Filters: []*app.HolderFilter{
				app.HolderFilter_builder{Tenant: contoso}.Build(),
				app.HolderFilter_builder{Labels: map[string]string{"dept": "eng"}}.Build(),
			},
		}.Build())
		require.NoError(t, err)

		// That a fragment was given, the page's size, the tenant by its
		// identifier, and that there was a second filter: by labels, which go.
		kept(t, app.HolderService_Search_FullMethodName, &app.HolderSearchRequest{}, app.HolderSearchRequest_builder{
			Q:    z.Ptr(""),
			Size: 5,
			Filters: []*app.HolderFilter{
				app.HolderFilter_builder{Tenant: contoso}.Build(),
				app.HolderFilter_builder{}.Build(),
			},
		}.Build())
	})

	t.Run("an address by the address", func(t *testing.T) {
		_, err := app.NewEmailServiceClient(conn).Get(as, app.EmailGetRequest_builder{Ref: app.EmailRef_builder{
			At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr("alice@contoso.example")}.Build(),
		}.Build()}.Build())
		require.NoError(t, err)

		kept(t, app.EmailService_Get_FullMethodName, &app.EmailGetRequest{}, app.EmailGetRequest_builder{Ref: app.EmailRef_builder{
			At: app.EmailRefByAt_builder{TenantId: b.Contoso.Bytes(), Address: z.Ptr("")}.Build(),
		}.Build()}.Build())
	})

	t.Run("an account by its subject", func(t *testing.T) {
		_, err := app.NewIdentityServiceClient(conn).Get(as, app.IdentityGetRequest_builder{Ref: app.IdentityRef_builder{
			Subject: app.IdentityRefBySubject_builder{TenantId: b.Contoso.Bytes(), Provider: z.Ptr("entra"), Subject: z.Ptr("sub-of-alice")}.Build(),
		}.Build()}.Build())
		require.NoError(t, err)

		kept(t, app.IdentityService_Get_FullMethodName, &app.IdentityGetRequest{}, app.IdentityGetRequest_builder{Ref: app.IdentityRef_builder{
			Subject: app.IdentityRefBySubject_builder{TenantId: b.Contoso.Bytes(), Provider: z.Ptr(""), Subject: z.Ptr("")}.Build(),
		}.Build()}.Build())
	})

	t.Run("a number by the number", func(t *testing.T) {
		_, err := app.NewPhoneServiceClient(conn).Get(as, app.PhoneGetRequest_builder{Ref: app.PhoneRef_builder{
			Number: app.PhoneRefByNumber_builder{
				Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(), Number: z.Ptr("+82 10-5550-0100"), Kind: z.Ptr("mobile"),
			}.Build(),
		}.Build()}.Build())
		require.NoError(t, err)

		kept(t, app.PhoneService_Get_FullMethodName, &app.PhoneGetRequest{}, app.PhoneGetRequest_builder{Ref: app.PhoneRef_builder{
			Number: app.PhoneRefByNumber_builder{
				Holder: app.HolderRef_builder{Id: alice.Bytes()}.Build(), Number: z.Ptr(""), Kind: z.Ptr(""),
			}.Build(),
		}.Build()}.Build())
	})

	t.Run("a List", func(t *testing.T) {
		first, err := H.List(as, app.HolderListRequest_builder{Size: 1}.Build())
		require.NoError(t, err)
		require.NotEmpty(t, first.GetNext())
		filed(t, ctx, b.Server, "")

		_, err = H.List(as, app.HolderListRequest_builder{
			Size:    1,
			After:   first.GetNext(),
			Reverse: false,
			Filters: []*app.HolderFilter{
				app.HolderFilter_builder{Tenant: app.TenantRef_builder{Alias: z.Ptr("contoso")}.Build()}.Build(),
			},
		}.Build())
		require.NoError(t, err)

		// Its filters and its size. The cursor is the last row's place in the
		// order, which for a list in order of a name is the name: it goes,
		// and so does that there was one.
		kept(t, app.HolderService_List_FullMethodName, &app.HolderListRequest{}, app.HolderListRequest_builder{
			Size: 1,
			Filters: []*app.HolderFilter{
				app.HolderFilter_builder{Tenant: app.TenantRef_builder{Alias: z.Ptr("")}.Build()}.Build(),
			},
		}.Build())
	})

	t.Run("a Watch", func(t *testing.T) {
		req := app.HolderWatchRequest_builder{Filters: []*app.HolderFilter{
			app.HolderFilter_builder{Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build()}.Build(),
		}}.Build()

		// What a stream showed is queued a moment after it was sent, while it
		// is still open: a second by default, and less here.
		b.Access.Every = 10 * time.Millisecond
		defer func() { b.Access.Every = 0 }()

		wctx, cancel := context.WithCancel(as)
		defer cancel()
		st, err := H.Watch(wctx, req)
		require.NoError(t, err)
		_, err = st.Recv()
		require.NoError(t, err)
		require.Eventually(t, func() bool { return b.Access.Pending() > 0 }, 5*time.Second, 5*time.Millisecond)

		// A watch names its rows by identifier, so what it keeps is what was
		// asked.
		kept(t, app.HolderService_Watch_FullMethodName, &app.HolderWatchRequest{}, req)
	})

	// A key is not read by its verifier at a port any more
	// (`TestAVerifierNamesNoRowOverTheWire`), and the refusal keeps nothing of
	// what it was asked, as any refusal does.
	t.Run("a key by its verifier", func(t *testing.T) {
		x := require.New(t)

		sel := app.ApiKeySelect_builder{Alias: z.Ptr(true), Holder: app.HolderSelect_builder{Alias: z.Ptr(true)}.Build()}.Build()
		_, err := app.NewApiKeyServiceClient(conn).Get(as, app.ApiKeyGetRequest_builder{
			Ref: app.ApiKeyRef_builder{Secret: verifier}.Build(), Select: sel,
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

		for _, r := range filed(t, ctx, b.Server, app.ApiKeyService_Get_FullMethodName) {
			d := &pdpb.AccessDetail{}
			x.NoError(proto.Unmarshal(r.Value, d))
			x.Empty(d.GetRequest())
			x.NotContains(string(r.Value), string(verifier))
		}
	})

	t.Run("and a call that was refused keeps nothing of what it asked, as before", func(t *testing.T) {
		x := require.New(t)

		_, err := app.NewEmailServiceClient(conn).Get(as, app.EmailGetRequest_builder{
			Ref:    app.EmailRef_builder{Id: address.GetId()}.Build(),
			Select: app.EmailSelect_builder{Holder: app.HolderSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build()}.Build(),
		}.Build())
		x.NoError(err)
		filed(t, ctx, b.Server, "")

		_, err = H.List(as, app.HolderListRequest_builder{
			Filters: []*app.HolderFilter{app.HolderFilter_builder{Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build()}.Build()},
			After:   "not a cursor",
		}.Build())
		x.Error(err)

		rs := filed(t, ctx, b.Server, app.HolderService_List_FullMethodName)
		x.Len(rs, 1)
		d := &pdpb.AccessDetail{}
		x.NoError(proto.Unmarshal(rs[0].Value, d))
		x.Empty(d.GetRequest())
	})
}

// word is what the test below writes wherever a request holds a string, and
// secretly what it writes wherever one holds bytes.
const word = "erin-example-of-contoso"

var secretly = bytes.Repeat([]byte("\x00verifier\xff"), 4)

// wordy fills `m` with a word in every string it can hold, and bytes that
// are no identifier in every run of bytes -- all the way down, a value in
// every list and every map, and of each `oneof` the arm `pick` counts to, so
// that asked for every `pick` up to the widest one, every arm there is has
// been filled.
func wordy(m protoreflect.Message, pick int, depth int) {
	fs := m.Descriptor().Fields()
	for i := range fs.Len() {
		fd := fs.Get(i)
		if o := fd.ContainingOneof(); o != nil && !o.IsSynthetic() && o.Fields().Get(pick%o.Fields().Len()) != fd {
			continue
		}

		one := func(fd protoreflect.FieldDescriptor, make func() protoreflect.Value) (protoreflect.Value, bool) {
			switch fd.Kind() {
			case protoreflect.StringKind:
				return protoreflect.ValueOfString(word), true
			case protoreflect.BytesKind:
				return protoreflect.ValueOfBytes(secretly), true
			case protoreflect.BoolKind:
				return protoreflect.ValueOfBool(true), true
			case protoreflect.MessageKind:
				if depth == 0 {
					return protoreflect.Value{}, false
				}
				v := make()
				wordy(v.Message(), pick, depth-1)
				return v, true
			}

			return protoreflect.Value{}, false
		}

		switch {
		case fd.IsMap():
			mp := m.Mutable(fd).Map()
			if v, ok := one(fd.MapValue(), mp.NewValue); ok && fd.MapKey().Kind() == protoreflect.StringKind {
				mp.Set(protoreflect.ValueOfString(word).MapKey(), v)
			}
		case fd.IsList():
			l := m.Mutable(fd).List()
			if v, ok := one(fd, l.NewElement); ok {
				l.Append(v)
			}
		default:
			if v, ok := one(fd, func() protoreflect.Value { return m.NewField(fd) }); ok {
				m.Set(fd, v)
			}
		}
	}
}

// TestNoWordOfAnyRequestIsKept: whatever RPC a row is of and whatever its
// request holds, the row keeps no string of it and no run of bytes that is
// not an identifier.
//
// The test before this says what is kept of the reads there are. This says
// what is not, of every request the schema has -- every RPC of every service,
// served at a port, at the box or not at all, with every string and every
// `bytes` it can hold filled in, each arm of each reference in turn -- through
// the writer a server writes its record with, into the table. So an RPC added
// tomorrow, or a string added to a request, is held to it without a line here.
func TestNoWordOfAnyRequestIsKept(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	rpcs, worded := 0, 0
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if p := fd.Package(); p != "roster" && p != "payday" {
			return true
		}

		for i := range fd.Services().Len() {
			sd := fd.Services().Get(i)
			for j := range sd.Methods().Len() {
				md := sd.Methods().Get(j)
				mt, err := protoregistry.GlobalTypes.FindMessageByName(md.Input().FullName())
				x.NoError(err)
				rpcs++

				// Every arm of the widest reference there is.
				for pick := range 6 {
					req := mt.New()
					wordy(req, pick, 6)
					asked, err := proto.Marshal(req.Interface())
					x.NoError(err)
					if strings.Contains(string(asked), word) {
						worded++
					}

					x.NoError(b.Access.Write(ctx, access.Record{
						Tenant: b.Contoso, Actor: b.ContosoUser, ActorTenant: b.Contoso,
						Action: fmt.Sprintf("/%s/%s", sd.FullName(), md.Name()),
						Kind:   access.Read, Result: "ok", At: time.Now(),
						Subjects: []pdid.Id{b.ContosoUser}, Count: 1,
						Detail: pdpb.AccessDetail_builder{Address: "192.0.2.1", Request: asked}.Build(),
					}))
				}
			}
		}

		return true
	})
	x.Greater(rpcs, 200, "the schema's services were not found")
	x.Greater(worded, rpcs, "the requests this made hold no word to take out")

	rs, err := b.Ent.Access.Query().All(ctx)
	x.NoError(err)
	x.Len(rs, rpcs*6)
	for _, r := range rs {
		x.NotContains(string(r.Value), word, "%s keeps a word of its request", r.Action)
		x.False(bytes.Contains(r.Value, secretly[:10]), "%s keeps bytes of its request that are no identifier", r.Action)
		x.Contains(string(r.Value), "192.0.2.1", "where the call came from stays")
	}
}

// secretArms is every arm of a reference that names a row by a column the
// schema declares `secret`, as `Kind.arm`: read off the schema, a reference
// being the message the generator names `<Kind>Ref`.
func secretArms(t *testing.T) []string {
	t.Helper()

	var out []string
	protoregistry.GlobalFiles.RangeFilesByPackage("roster", func(fd protoreflect.FileDescriptor) bool {
		ms := fd.Messages()
		for i := range ms.Len() {
			ref := ms.Get(i)
			kind, ok := strings.CutSuffix(string(ref.Name()), "Ref")
			if !ok {
				continue
			}
			row := fd.Messages().ByName(protoreflect.Name(kind))
			if row == nil {
				d, err := protoregistry.GlobalFiles.FindDescriptorByName(ref.FullName().Parent().Append(protoreflect.Name(kind)))
				if err != nil {
					continue
				}
				row = d.(protoreflect.MessageDescriptor)
			}

			for j := range ref.Fields().Len() {
				arm := ref.Fields().Get(j)
				of := row.Fields().ByName(arm.Name())
				if of == nil {
					continue
				}
				if v, _ := proto.GetExtension(of.Options(), pdpb.E_Field).(*pdpb.Field); v.GetSecret() {
					out = append(out, kind+"."+string(arm.Name()))
				}
			}
		}
		return true
	})
	slices.Sort(out)

	return out
}

// TestNothingDeclaredSecretReachesTheRecord: a row read by the `secret` arm
// of its reference -- a key by its verifier, a session by its cookie's -- leaves
// a row of the access record that says it was read that way and holds nothing
// of the secret, for every such arm the schema has.
//
// # What it was
//
// `ApiKeyService/Get` by `ref.secret`, with a `select` reaching the holder's
// alias, left a row whose `value` held the key's 32-byte verifier: answered
// 200 and wrote the verifier down, in the record kept beside the trail and
// archived with it. No port answers a verifier and the trail holds none
// (`TestNoVerifierReachesTheTrail`).
//
// Asked at a shell on the box, which is the one door every kind's reads are
// at: no port serves a session's, a link's or a continuation's, and a
// delegation's are shut. The arms are read off the schema, so one added is
// asked by here or fails this.
func TestNothingDeclaredSecretReachesTheRecord(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	alice := b.holder(t, ctx, b.Contoso, "alice")
	whose := app.HolderRef_builder{Id: alice.Bytes()}.Build()
	soon := timestamppb.New(time.Now().Add(time.Hour))

	// Who issued it, and what a session grants: each is a column a row is not
	// made without. SQLite made one without, and PostgreSQL refused it, which
	// is how this test passed here and failed where it was merged from.
	issuer := pdid.New(pd.ApiKeyDomain).Bytes()

	// How a row of each kind is made with a secret this test chose.
	made := map[string]func(secret []byte) error{
		"ApiKey.secret": func(secret []byte) error {
			_, err := b.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
				Holder: whose, Alias: "laptop", Secret: secret, Methods: []string{app.MeService_Get_FullMethodName},
			}.Build())
			return err
		},
		"Continuation.secret": func(secret []byte) error {
			_, err := b.Ungated.Continuation().Add(ctx, app.ContinuationAddRequest_builder{
				Holder: whose, Issuer: issuer, Secret: secret, DateExpires: soon,
			}.Build())
			return err
		},
		"Delegation.secret": func(secret []byte) error {
			_, err := b.Ungated.Delegation().Add(ctx, app.DelegationAddRequest_builder{
				Holder: whose, Issuer: issuer, Secret: secret, Methods: []string{app.MeService_Get_FullMethodName}, DateExpires: soon,
			}.Build())
			return err
		},
		"Link.secret": func(secret []byte) error {
			_, err := b.Ungated.Link().Add(ctx, app.LinkAddRequest_builder{
				Holder: whose, Issuer: issuer, At: "alice@contoso.example", Secret: secret, DateExpires: soon,
			}.Build())
			return err
		},
		"Session.secret": func(secret []byte) error {
			_, err := b.Ungated.Session().Add(ctx, app.SessionAddRequest_builder{
				Holder: whose, Grant: []byte{0}, Secret: secret, DateExpires: soon,
			}.Build())
			return err
		},
	}

	arms := secretArms(t)
	x.Contains(arms, "ApiKey.secret")
	for _, arm := range arms {
		x.Contains(made, arm, "%s names a row by a secret: say here how one is made, so that it is asked by", arm)
	}
	x.Len(made, len(arms), "a kind is made here whose reference has no secret arm any more")

	// The box's pipe (`cli/entity.go`, `pipe`): every kind's service on the
	// server with no wall, and the access record in front as a shell's.
	g := grpc.NewServer(b.Shell(cmd.Operator{Where: "a test"}).Interceptor().ServerOptions()...)
	app.RegisterServer(g, b.Ungated)
	conn := pdtest.Serve(t, g)

	for _, arm := range arms {
		kind, name, _ := strings.Cut(arm, ".")
		t.Run(arm, func(t *testing.T) {
			x := require.New(t)

			_, secret, err := keys.Mint(keys.PrefixTenant)
			x.NoError(err)
			x.NoError(made[arm](secret))
			_, err = b.Ent.Access.Delete().Exec(ctx)
			x.NoError(err)

			// `Get`, by that arm, with a `select` that reaches the holder's
			// alias: a read of somebody, which is what leaves a row.
			req := message(t, "roster."+kind+"GetRequest")
			ref := req.Mutable(req.Descriptor().Fields().ByName("ref")).Message()
			ref.Set(ref.Descriptor().Fields().ByName(protoreflect.Name(name)), protoreflect.ValueOfBytes(secret))
			sel := req.Mutable(req.Descriptor().Fields().ByName("select")).Message()
			holder := sel.Mutable(sel.Descriptor().Fields().ByName("holder")).Message()
			holder.Set(holder.Descriptor().Fields().ByName("alias"), protoreflect.ValueOfBool(true))

			method := "/roster." + kind + "Service/Get"
			res := message(t, "roster."+kind)
			x.NoError(conn.Invoke(ctx, method, req.Interface(), res.Interface()))
			x.Contains(protojson.Format(res.Interface()), `"alice"`, "the read answered somebody's alias")

			rs, err := b.Ent.Access.Query().All(ctx)
			x.NoError(err)
			x.Len(rs, 1, "a read of somebody by a row's secret is on the record")
			x.Equal(method, rs[0].Action)
			x.Equal([][]byte{alice.Bytes()}, rs[0].Subjects)

			kept := message(t, "roster."+kind+"GetRequest")
			value := keptOf(t, rs[0], kept.Interface())
			x.False(bytes.Contains(value, secret), "the row holds the secret it was read by")
			x.False(bytes.Contains(value, secret[:8]), "the row holds part of the secret it was read by")

			// And says it was read by it.
			ref = kept.Get(kept.Descriptor().Fields().ByName("ref")).Message()
			by := ref.Descriptor().Fields().ByName(protoreflect.Name(name))
			x.True(ref.Has(by), "that it was read by its secret is not kept")
			x.Empty(ref.Get(by).Bytes())
		})
	}
}

// message is a new message of the schema's, by name.
func message(t *testing.T, name string) protoreflect.Message {
	t.Helper()

	mt, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(name))
	require.NoError(t, err, name)

	return mt.New()
}

// TestASecretThatLooksLikeAnIdentifierIsNotKept: a field the schema declares
// `secret` goes whatever it holds.
//
// An identifier stays in what a row keeps, and is told by its shape: sixteen
// bytes of a kind this schema has. A verifier is thirty-two today and is told
// from one by that. This is the declaration deciding instead, for a secret
// that is ever sixteen: the same bytes are kept as `ApiKeyRef.id` and are not
// as `ApiKeyRef.secret`.
func TestASecretThatLooksLikeAnIdentifierIsNotKept(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	looks := pdid.New(b.ContosoUser.Domain())
	write := func(ref *app.ApiKeyRef) *app.ApiKeyRef {
		_, err := b.Ent.Access.Delete().Exec(ctx)
		x.NoError(err)

		asked, err := proto.Marshal(app.ApiKeyGetRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		x.NoError(b.Access.Write(ctx, access.Record{
			Tenant: b.Contoso, Actor: b.ContosoUser, ActorTenant: b.Contoso,
			Action: app.ApiKeyService_Get_FullMethodName, Kind: access.Read, Result: "ok", At: time.Now(),
			Subjects: []pdid.Id{b.ContosoUser}, Count: 1,
			Detail: pdpb.AccessDetail_builder{Request: asked}.Build(),
		}))

		kept := &app.ApiKeyGetRequest{}
		keptOf(t, b.Ent.Access.Query().OnlyX(ctx), kept)

		return kept.GetRef()
	}

	x.Equal(looks.Bytes(), write(app.ApiKeyRef_builder{Id: looks.Bytes()}.Build()).GetId(), "an identifier stays")

	kept := write(app.ApiKeyRef_builder{Secret: looks.Bytes()}.Build())
	x.True(kept.HasSecret())
	x.Empty(kept.GetSecret(), "declared secret, and kept because it is sixteen bytes long")
}

// TestWhatIsLeftOfSomebodyForgottenOnTheRecordIsIdentifiers: once somebody is
// forgotten, the access record holds nothing they were called.
//
// # What it was
//
// `forget` does not reach the access record: a row names whose data a call
// carried by identifier, which stays as the trail's actor does. But a row kept
// its request as it was sent, so after erin was forgotten the record still
// said `erin` (the `Get` that named her), `Erin Exa` (the search that found
// her) and her address (the lookup by it), for the record's window -- which is
// for ever unless a deployment says.
//
// What is left is identifiers: hers, which is a blank row's, and her
// address's, which is no row's.
func TestWhatIsLeftOfSomebodyForgottenOnTheRecordIsIdentifiers(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	b := atTheBox(t)

	run := func(as string, args ...string) string {
		t.Helper()
		out, err := entities(t, at(b.c, as), args...)
		require.NoError(t, err, "roster %s", strings.Join(args, " "))

		return out
	}

	run("admin", "holder", "add", "@newco/erin", `{"name":"Erin Example"}`)
	s, err := cmd.Build(ctx, b.c)
	x.NoError(err)
	erin, err := cmd.CustomerOf(ctx, s, "newco", "erin")
	x.NoError(err)
	x.NoError(s.Close())
	run("admin", "email", "add", fmt.Sprintf(`{"holder":{"id":"%s"},"address":"erin@example.com"}`, erin))

	// Read by what she is called: her alias, a fragment of her name, her
	// address.
	run("admin", "holder", "get", "@newco/erin")
	x.Contains(run("admin", "holder", "search", "-o", "json", `{"q":"Erin Exa"}`), "Erin Example")
	run("admin", "email", "list", "-o", "json", fmt.Sprintf(`{"filters":[{"holder":{"slug":{"alias":"erin","tenant":{"id":"%s"}}}}]}`, b.newco))

	run("admin", "holder", "erase", "@newco/erin")
	run("", "forget", erin.String())

	s, err = cmd.Build(ctx, b.c)
	x.NoError(err)
	defer s.Close()
	x.Empty(s.Ent.Holder.GetX(ctx, erin.Uuid()).Alias, "she is forgotten")

	rs, err := s.Ent.Access.Query().All(ctx)
	x.NoError(err)
	named := 0
	for _, r := range rs {
		for _, w := range []string{"erin", "Erin", "example.com"} {
			x.NotContains(string(r.Value), w, "%s still says what she was called", r.Action)
		}
		if slices.ContainsFunc(r.Subjects, func(v []byte) bool { return bytes.Equal(v, erin.Bytes()) }) {
			named++
		}
	}
	x.GreaterOrEqual(named, 2, "the reads of her are still on the record, by her identifier")
}
