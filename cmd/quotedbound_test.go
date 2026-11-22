package cmd_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/z"

	app "github.com/lesomnus/roster/rstr"
)

// TestARefusalQuotesWhatItWasSentToABound: a refusal that says back what a
// caller sent says the first eighty bytes of it and its length, whatever it
// was sent.
//
// # What it was
//
// A value of 70,000 bytes was quoted whole in the refusal of eighteen
// requests, and of one twice over, where an address was already quoted to
// eighty bytes and its length. The message is an attribute of the log's line
// for every refused call, so a caller wrote what it liked into the log, a
// megabyte at a time. Nothing a caller is told is lost by the bound: what was
// wrong is the start of it, and how long it was.
func TestARefusalQuotesWhatItWasSentToABound(t *testing.T) {
	b, ctx := build(t)
	b.mayAnything(b.ContosoUser, b.Contoso)
	conn := served(t, b.Server)
	as := asOverTheWire(ctx, b.ContosoUser)

	long := strings.Repeat("A", 70_000)
	tenant := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	me := app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build()

	role, err := app.NewRoleServiceClient(conn).Add(as, app.RoleAddRequest_builder{Tenant: tenant, Alias: "widened"}.Build())
	require.NoError(t, err)
	connection, err := app.NewConnectionServiceClient(conn).Add(as, app.ConnectionAddRequest_builder{
		Tenant: tenant, Name: "okta", Issuer: "https://okta.example.com", ClientId: "roster",
	}.Build())
	require.NoError(t, err)

	for name, call := range map[string]func(ctx context.Context, conn *grpc.ClientConn) error{
		"ApiKeyService/Issue methods": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewApiKeyServiceClient(conn).Issue(ctx, app.ApiKeyIssueRequest_builder{
				Holder: me, Alias: "long", Methods: []string{long},
			}.Build())
			return err
		},
		"RoleService/Add methods": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewRoleServiceClient(conn).Add(ctx, app.RoleAddRequest_builder{
				Tenant: tenant, Alias: "long", Methods: []string{long},
			}.Build())
			return err
		},
		"RoleService/Widen methods": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewRoleServiceClient(conn).Widen(ctx, app.RoleWidenRequest_builder{
				Ref: app.RoleRef_builder{Id: role.GetId()}.Build(), Methods: []string{long},
			}.Build())
			return err
		},
		"ConnectionService/Add issuer": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewConnectionServiceClient(conn).Add(ctx, app.ConnectionAddRequest_builder{
				Tenant: tenant, Name: "issuer", Issuer: long, ClientId: "roster",
			}.Build())
			return err
		},
		"ConnectionService/Add subject_claim": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewConnectionServiceClient(conn).Add(ctx, app.ConnectionAddRequest_builder{
				Tenant: tenant, Name: "claim", Issuer: "https://claim.example.com", ClientId: "roster", SubjectClaim: long,
			}.Build())
			return err
		},
		"ConnectionService/Update issuer": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewConnectionServiceClient(conn).Update(ctx, app.ConnectionUpdateRequest_builder{
				Ref:         app.ConnectionRef_builder{Id: connection.GetId()}.Build(),
				DateUpdated: timestamppb.New(connection.GetDateUpdated().AsTime()),
				Issuer:      z.Ptr(long),
			}.Build())
			return err
		},
		"ConnectionService/Update subject_claim": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewConnectionServiceClient(conn).Update(ctx, app.ConnectionUpdateRequest_builder{
				Ref:          app.ConnectionRef_builder{Id: connection.GetId()}.Build(),
				DateUpdated:  timestamppb.New(connection.GetDateUpdated().AsTime()),
				SubjectClaim: z.Ptr(long),
			}.Build())
			return err
		},
		"ConsentService/Withdraw document": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewConsentServiceClient(conn).Withdraw(ctx, app.ConsentWithdrawRequest_builder{
				Holder: me, Document: long, Version: "1",
			}.Build())
			return err
		},
		"ConsentService/Withdraw version": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewConsentServiceClient(conn).Withdraw(ctx, app.ConsentWithdrawRequest_builder{
				Holder: me, Document: "terms", Version: long,
			}.Build())
			return err
		},
		"CredentialService/Enrol kind": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewCredentialServiceClient(conn).Enrol(ctx, app.CredentialEnrolRequest_builder{
				Holder: me, Kind: long,
			}.Build())
			return err
		},
		"CredentialService/Issue kind": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewCredentialServiceClient(conn).Issue(ctx, app.CredentialIssueRequest_builder{
				Holder: me, Kind: long,
			}.Build())
			return err
		},
		"PhoneService/Add kind": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewPhoneServiceClient(conn).Add(ctx, app.PhoneAddRequest_builder{
				Holder: me, Number: "+82 10 1234 5678", Kind: long,
			}.Build())
			return err
		},
		"VouchService/Verify kind": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewVouchServiceClient(conn).Verify(ctx, app.VouchVerifyRequest_builder{
				Who:  app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
				Kind: long, Secret: []byte("secret"),
			}.Build())
			return err
		},
		"VouchService/Delegate kind": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewVouchServiceClient(conn).Delegate(ctx, app.VouchDelegateRequest_builder{
				Who:  app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
				Kind: long, Secret: []byte("secret"),
				Methods: []string{app.HolderService_Get_FullMethodName}, DateExpires: timestamppb.New(time.Now().Add(time.Hour)),
			}.Build())
			return err
		},
		"VouchService/Continue kind": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewVouchServiceClient(conn).Continue(ctx, app.VouchContinueRequest_builder{
				Continuation: "whatever", Kind: long, Secret: []byte("secret"),
			}.Build())
			return err
		},
		"HostService/Add name": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewHostServiceClient(conn).Add(ctx, app.HostAddRequest_builder{
				Tenant: tenant, Name: long,
			}.Build())
			return err
		},
		"HostProofService/Add name": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewHostProofServiceClient(conn).Add(ctx, app.HostProofAddRequest_builder{
				Tenant: tenant, Name: long,
			}.Build())
			return err
		},
		"MailDomainService/Add name": func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := app.NewMailDomainServiceClient(conn).Add(ctx, app.MailDomainAddRequest_builder{
				Tenant: tenant, Name: long, Provider: "okta",
			}.Build())
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call(as, conn)
			require.Error(t, err)
			said := status.Convert(err).Message()
			require.Less(t, len(said), 1024, "%s: %.300s", status.Code(err), said)
			require.Contains(t, said, "(70000 bytes)", "refused for something else than what was sent: %s", said)
		})
	}
}
