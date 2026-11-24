package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// TestExchangeNamesTheAppTheTokenIsFor: who a token `Exchange` mints is for
// is `app`, the word a role, a group and a key say it in.
//
// It was `audience`, the issuer's word for an API, on a field that names a
// holder of the tenant: the app the receiving server is answered as. So a
// caller read two words for one thing, and the refusals said the one nothing
// else in roster says. The field is the same number and the same type; what
// it is called, in the request and in what refuses it, is `app`.
func TestExchangeNamesTheAppTheTokenIsFor(t *testing.T) {
	x := require.New(t)

	md := (&app.DelegationExchangeRequest{}).ProtoReflect().Descriptor()
	fd := md.Fields().ByNumber(1)
	x.NotNil(fd)
	x.Equal(protoreflect.Name("app"), fd.Name(), "who the token is for is not called app")
	x.Equal(protoreflect.FullName("roster.HolderRef"), fd.Message().FullName())
	x.Nil(md.Fields().ByName("audience"), "the request still says audience")

	b := keyFor(t, app.TenantService_Get_FullMethodName)
	ctx := t.Context()

	exchange := "/roster.DelegationService/Exchange"
	nominates(t, b.Server, b.Contoso, b.Service, b.Who)
	permits(t, ctx, b, b.Contoso, b.Who, "loft", exchange)

	delegations := app.NewDelegationServiceClient(b.Conn)
	at := arrivedAt(ctx, b.Token, front.AtTenant("contoso"))

	t.Run("none is refused, naming app", func(t *testing.T) {
		_, err := delegations.Exchange(at, app.DelegationExchangeRequest_builder{
			Methods: []string{"/levyan.InvoiceService/Get"},
		}.Build())
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		exchangeSaysApp(t, err)
	})

	t.Run("and the caller itself is refused, naming app", func(t *testing.T) {
		_, err := delegations.Exchange(at, app.DelegationExchangeRequest_builder{
			App:     app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
			Methods: []string{"/levyan.InvoiceService/Get"},
		}.Build())
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		exchangeSaysApp(t, err)
	})
}

// exchangeSaysApp is a refusal of `Exchange` that names the field `app`, in
// its message or in a violation it carries, and says nothing of an audience.
func exchangeSaysApp(t *testing.T, err error) {
	t.Helper()

	s := status.Convert(err)
	said := s.Message()
	for _, d := range s.Details() {
		if v, ok := d.(interface{ String() string }); ok {
			said += " " + v.String()
		}
	}
	require.NotContains(t, said, "audience")
	require.True(t, strings.HasPrefix(s.Message(), "app") || strings.Contains(said, `"app"`), "the refusal names no app: %s", said)
}
