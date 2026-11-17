package cmd_test

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestNobodyAtAPortSaysWhenARowWasMade: the moment a row was made, and the
// moments somebody was suspended and signed out everywhere, are the server's
// to stamp, and a request at a port that says one is refused.
//
// # What it was
//
// Every generated `Add` takes `date_created` and wrote what it was handed,
// and `HolderService/Add` took `date_invalidated` and `date_disabled` with
// it. A tenant's administrator added somebody dated 2001: OK, and kept. Lists
// are in the order rows were made, and a cursor is a place in that order, so a
// walk already past 2001 never met them, and one dated 2099 stays last behind
// everybody made after. Somebody added with `date_invalidated` ten years on:
// OK, and every sign-in of theirs is refused until then, as one made before
// it; `holder.proto` says of that moment that nothing takes it as an
// argument. Right, for each, is InvalidArgument naming the field.
//
// # Where it is refused
//
// Where requests arrive, as a label that says a file declares a row is
// (`cmd.Undeclared`): it is one rule about every entity there is and will be,
// and what may say a moment -- a test that needs a row made last week, the
// deployment's own work -- does not arrive.
func TestNobodyAtAPortSaysWhenARowWasMade(t *testing.T) {
	b, ctx := build(t)
	b.mayAnything(b.ContosoUser, b.Contoso)
	wire := asOverTheWire(ctx, b.ContosoUser)
	g := b.grpc(t)
	conn := pdtest.Serve(t, g)
	holders := app.NewHolderServiceClient(conn)
	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()

	refused := func(t *testing.T, err error, field string) {
		t.Helper()
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		require.Contains(t, status.Convert(err).Message(), field)
	}
	missing := func(t *testing.T, alias string) {
		t.Helper()
		_, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{Alias: &alias, Tenant: at}.Build()}.Build(),
		}.Build())
		require.Equal(t, codes.NotFound, status.Code(err), "%s was made: %v", alias, err)
	}

	t.Run("not the moment somebody was made", func(t *testing.T) {
		for alias, when := range map[string]time.Time{
			"early": time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC),
			"late":  time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		} {
			_, err := holders.Add(wire, app.HolderAddRequest_builder{
				Tenant: at, Alias: alias, DateCreated: timestamppb.New(when),
			}.Build())
			refused(t, err, "date_created")
			missing(t, alias)
		}
	})

	t.Run("not that their sign-ins end ten years on", func(t *testing.T) {
		_, err := holders.Add(wire, app.HolderAddRequest_builder{
			Tenant: at, Alias: "voided", DateInvalidated: timestamppb.New(time.Now().AddDate(10, 0, 0)),
		}.Build())
		refused(t, err, "date_invalidated")
		missing(t, "voided")
	})

	t.Run("not that they are suspended", func(t *testing.T) {
		_, err := holders.Add(wire, app.HolderAddRequest_builder{
			Tenant: at, Alias: "stopped", DateDisabled: timestamppb.Now(),
		}.Build())
		refused(t, err, "date_disabled")
		missing(t, "stopped")
	})

	// Every `Add` this listener has, read off it and not written out: a list
	// written by hand is the entities there were that day.
	t.Run("of any entity", func(t *testing.T) {
		var asked, shut []string
		for svc, info := range g.GetServiceInfo() {
			for _, m := range info.Methods {
				if m.Name != "Add" {
					continue
				}
				d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svc))
				require.NoError(t, err)
				in := d.(protoreflect.ServiceDescriptor).Methods().ByName("Add").Input()
				f := in.Fields().ByName("date_created")
				if f == nil {
					continue
				}

				req := dynamicpb.NewMessage(in)
				req.Set(f, protoreflect.ValueOfMessage(timestamppb.New(time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)).ProtoReflect()))
				method := "/" + svc + "/Add"
				err = conn.Invoke(wire, method, req, dynamicpb.NewMessage(d.(protoreflect.ServiceDescriptor).Methods().ByName("Add").Output()))
				if status.Code(err) == codes.Unimplemented {
					// Not served at all (`closed`): there is nothing to say a
					// moment to.
					shut = append(shut, method)

					continue
				}
				require.Equal(t, codes.InvalidArgument, status.Code(err), "%s: %v", method, err)
				require.Contains(t, status.Convert(err).Message(), "date_created", method)
				asked = append(asked, method)
			}
		}
		sort.Strings(shut)
		require.Equal(t, []string{
			app.ApiKeyService_Add_FullMethodName, app.ConsentService_Add_FullMethodName,
			app.CredentialService_Add_FullMethodName, app.DelegationService_Add_FullMethodName,
		}, shut, "the `Add`s that are not served")
		require.GreaterOrEqual(t, len(asked), 20, "%s", strings.Join(asked, " "))
	})

	t.Run("and a row with no moment said is stamped now", func(t *testing.T) {
		x := require.New(t)
		before := time.Now().Add(-time.Second)
		v, err := holders.Add(wire, app.HolderAddRequest_builder{Tenant: at, Alias: "now"}.Build())
		x.NoError(err)
		x.True(v.GetDateCreated().AsTime().After(before))
		x.Nil(v.GetDateDisabled())
		x.Nil(v.GetDateInvalidated())
	})

	// What is not at a port may say one: this suite makes rows that are a week
	// old that way, and it is the deployment's to say of its own work.
	t.Run("and the deployment's own write still says one", func(t *testing.T) {
		x := require.New(t)
		when := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
		v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: at, Alias: "old", DateCreated: timestamppb.New(when),
		}.Build())
		x.NoError(err)
		x.True(v.GetDateCreated().AsTime().Equal(when))
	})
}

// TestAnOperatorSaysNoMomentEither: the admin listener is the deployment's
// root, and the moment a row was made is not an operator's to say there
// either. Nothing an operator does needs one, and a row dated otherwise is
// out of its place in every list, whoever wrote it.
func TestAnOperatorSaysNoMomentEither(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)
	c := signIn(t, s, "admin", passwordFrom(t, out))
	x.NotNil(c)

	g, err := s.GrpcAdmin(ctx, cmd.Config{})
	x.NoError(err)
	admin := pdtest.Serve(t, g)
	as := metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value))

	_, err = app.NewTenantServiceClient(admin).Add(as, app.TenantAddRequest_builder{
		Alias: "newco", DateCreated: timestamppb.New(time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	x.ErrorContains(err, "date_created")

	tn, err := app.NewTenantServiceClient(admin).Add(as, app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)

	_, err = app.NewHolderServiceClient(admin).Add(as, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(), Alias: "stopped", DateDisabled: timestamppb.Now(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)
	x.ErrorContains(err, "date_disabled")
}
