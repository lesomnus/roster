package cmd_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/web"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// versionedAt is a port, over gRPC and through the transcoder, and how a call
// says who is asking on each.
type versionedAt struct {
	conn  *grpc.ClientConn
	url   string
	as    func(ctx context.Context) context.Context
	asked func(r *http.Request)
}

// versionedAnswer is what one call was answered: the code, the status over
// HTTP, and the words of each.
type versionedAnswer struct {
	code    codes.Code
	said    string
	status  int
	written string
}

func (at versionedAt) ask(t *testing.T, method string, req proto.Message) versionedAnswer {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	var a versionedAnswer
	err := at.conn.Invoke(at.as(ctx), method, req, &emptypb.Empty{})
	a.code, a.said = status.Code(err), status.Convert(err).Message()

	body, err := protojson.Marshal(req)
	x.NoError(err)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, at.url+method, strings.NewReader(string(body)))
	x.NoError(err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Connect-Protocol-Version", "1")
	at.asked(r)
	res, err := http.DefaultClient.Do(r)
	x.NoError(err)
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	x.NoError(err)

	a.status = res.StatusCode
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) == nil {
		a.written = e.Message
	}

	return a
}

// TestAVersionThatMovedIsAbortedInOneSentence: each of the seventeen verbs on
// the wire that take a version (`date_updated`) answers one that has moved
// `Aborted`, over HTTP 409, in one sentence that names `date_updated` and says
// to read again and send again.
//
// They answered `FailedPrecondition`, the code sixty rules answer that nothing
// mends, and over HTTP 400, a malformed request's: a page could not tell the
// one refusal it mends by reading again from a refusal it shows. And in three
// wordings: the generated `Patch`'s *a test in the patch did not hold*, about
// a verb no caller has; the directory's two verbs', and a tenant's.
//
// Each verb is asked with a version of 2001, by somebody who may call it, of a
// row the call would otherwise write: the version is the one thing wrong. A
// tenant is restored by an operator, at the operators' port, which is where a
// closed tenant is written from.
func TestAVersionThatMovedIsAbortedInOneSentence(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	g := b.grpc(t)
	h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
	x.NoError(err)
	srv := httptest.NewServer(cmd.Arrived(h))
	t.Cleanup(srv.Close)

	admin := b.holder(t, ctx, b.Contoso, "versioned-admin")
	b.mayAnything(admin, b.Contoso)
	data := versionedAt{
		conn: pdtest.Serve(t, g), url: srv.URL,
		as:    func(ctx context.Context) context.Context { return asOverTheWire(ctx, admin) },
		asked: func(r *http.Request) { r.Header.Set("Authorization", "Plain "+admin.String()) },
	}

	at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
	stale := timestamppb.New(time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))

	// Somebody, and somebody the tenant's directory speaks for.
	someone := app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "versioned").Bytes()}.Build()
	_, err = b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant: at, Name: "entra", Issuer: "https://login.microsoftonline.com/contoso/v2.0", ClientId: "the-app",
		SubjectClaim: "oid", Provisions: true,
	}.Build())
	x.NoError(err)
	directed := b.holder(t, ctx, b.Contoso, "versioned-directed")
	b.identity(t, ctx, directed, "entra", "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0")
	spoken := app.HolderRef_builder{Id: directed.Bytes()}.Build()

	// An account at a connection that has come to name its people by `oid`,
	// held under the subject it named them by before.
	okta, err := b.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
		Tenant: at, Name: "okta", Issuer: "https://contoso.okta.example", ClientId: "the-app", SubjectClaim: "oid",
	}.Build())
	x.NoError(err)
	account := b.identity(t, ctx, b.holder(t, ctx, b.Contoso, "versioned-moving"), "local", "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ")
	_, err = b.Ungated.Identity().Patch(ctx, app.IdentityPatchRequest_builder{
		Ref: app.IdentityRef_builder{Id: account.GetId()}.Build(), Provider: z.Ptr(okta.GetName()), DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	group, err := b.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{Tenant: at, Alias: "versioned"}.Build())
	x.NoError(err)
	host, err := b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{Tenant: at, Name: "versioned.example.com"}.Build())
	x.NoError(err)
	domain, err := b.Ungated.MailDomain().Add(ctx, app.MailDomainAddRequest_builder{Tenant: at, Name: "versioned.example.com"}.Build())
	x.NoError(err)

	calls := []struct {
		method string
		req    proto.Message
	}{
		{app.HolderService_Update_FullMethodName, app.HolderUpdateRequest_builder{Ref: someone, DateUpdated: stale, Profile: &app.Profile{}}.Build()},
		{app.HolderService_Realias_FullMethodName, app.HolderRealiasRequest_builder{Ref: someone, DateUpdated: stale, Alias: "versioned-again"}.Build()},
		{app.HolderService_Disable_FullMethodName, app.HolderDisableRequest_builder{Ref: someone, DateUpdated: stale}.Build()},
		{app.HolderService_Enable_FullMethodName, app.HolderEnableRequest_builder{Ref: someone, DateUpdated: stale}.Build()},
		{app.HolderService_Invalidate_FullMethodName, app.HolderInvalidateRequest_builder{Ref: someone, DateUpdated: stale}.Build()},
		{app.HolderService_Portray_FullMethodName, app.HolderPortrayRequest_builder{Ref: someone, DateUpdated: stale}.Build()},
		{app.HolderService_Deactivate_FullMethodName, app.HolderDeactivateRequest_builder{Ref: spoken, DateUpdated: stale}.Build()},
		{app.HolderService_Activate_FullMethodName, app.HolderActivateRequest_builder{Ref: spoken, DateUpdated: stale}.Build()},
		{app.HolderService_Tell_FullMethodName, app.HolderTellRequest_builder{Ref: spoken, DateUpdated: stale, Address: z.Ptr("told@contoso.example")}.Build()},
		{app.TenantService_Update_FullMethodName, app.TenantUpdateRequest_builder{Ref: at, DateUpdated: stale, Desc: z.Ptr("said")}.Build()},
		{app.TenantService_Close_FullMethodName, app.TenantCloseRequest_builder{Ref: at, DateUpdated: stale}.Build()},
		{app.GroupService_Update_FullMethodName, app.GroupUpdateRequest_builder{Ref: app.GroupRef_builder{Id: group.GetId()}.Build(), DateUpdated: stale, Name: z.Ptr("named")}.Build()},
		{app.ConnectionService_Update_FullMethodName, app.ConnectionUpdateRequest_builder{Ref: app.ConnectionRef_builder{Id: okta.GetId()}.Build(), DateUpdated: stale, Desc: z.Ptr("said")}.Build()},
		{app.HostService_Update_FullMethodName, app.HostUpdateRequest_builder{Ref: app.HostRef_builder{Id: host.GetId()}.Build(), DateUpdated: stale, Desc: z.Ptr("said")}.Build()},
		{app.MailDomainService_Update_FullMethodName, app.MailDomainUpdateRequest_builder{Ref: app.MailDomainRef_builder{Id: domain.GetId()}.Build(), DateUpdated: stale, Desc: z.Ptr("said")}.Build()},
		{app.IdentityService_Resubject_FullMethodName, app.IdentityResubjectRequest_builder{Ref: app.IdentityRef_builder{Id: account.GetId()}.Build(), DateUpdated: stale, Subject: "1a2b3c4d-5e6f-7081-92a3-b4c5d6e7f809"}.Build()},
	}

	// Each of them asked, and each answer held, before any is stopped at.
	sentences := map[string][]string{}
	answered := func(method string, got versionedAnswer) {
		t.Helper()
		a := assert.New(t)
		a.Equal(codes.Aborted.String(), got.code.String(), "%s: %s", method, got.said)
		a.Equal(http.StatusConflict, got.status, "%s over HTTP: %s", method, got.written)
		a.Equal(got.said, got.written, "%s: the two halves say it in the same words", method)
		sentences[got.said] = append(sentences[got.said], method)
	}
	for _, c := range calls {
		answered(c.method, data.ask(t, c.method, c.req))
	}

	// The seventeenth, at the operators' port: a tenant closed is nobody of
	// its own's to write.
	{
		s, c, out := adminDeployment(t, nil)
		k := signIn(t, s, "admin", passwordFrom(t, out))
		x.NotNil(k, "the operator init printed cannot sign in")
		g, err := s.GrpcAdmin(ctx, c)
		x.NoError(err)
		h, err := web.New(config.HttpConfig{AllowWeb: true}, g)
		x.NoError(err)
		srv := httptest.NewServer(cmd.Arrived(h))
		t.Cleanup(srv.Close)
		ops := versionedAt{
			conn: pdtest.Serve(t, g), url: srv.URL,
			as: func(ctx context.Context) context.Context {
				return metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", k.Name+"="+k.Value))
			},
			asked: func(r *http.Request) { r.AddCookie(k) },
		}

		v, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "versioned-closed"}.Build())
		x.NoError(err)
		closed := app.TenantRef_builder{Id: v.GetId()}.Build()
		_, err = s.Ungated.Tenant().Close(ctx, app.TenantCloseRequest_builder{Ref: closed}.Build())
		x.NoError(err)

		method := app.TenantService_Restore_FullMethodName
		answered(method, ops.ask(t, method, app.TenantRestoreRequest_builder{Ref: closed, DateUpdated: stale}.Build()))
	}

	x.Len(sentences, 1, "one refusal, in one sentence: %v", sentences)
	for said, methods := range sentences {
		x.Len(methods, 17)
		x.Contains(said, "date_updated", "the sentence names the field")
		x.Contains(said, "read it again", "and says what mends it")
	}
}
