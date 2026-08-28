package cmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// TestTheFrontDoorsQuestionsHaveCommands is `roster front`: the two pre-sign-in
// lookups, answered at a shell exactly as a front door would be answered --
// which is the point of the command, since `roster host ls` says what is
// written down and this says what is served.
func TestTheFrontDoorsQuestionsHaveCommands(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	b := cliUp(t, "/roster.FrontService/WhoseHost", "/roster.FrontService/WhereFrom")

	_, err := b.Server.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.GetId()}.Build(),
		Name:   "newco.example.com",
	}.Build())
	x.NoError(err)

	_, err = b.Server.Ungated.MailDomain().Add(ctx, app.MailDomainAddRequest_builder{
		Tenant:   app.TenantRef_builder{Id: b.Tenant.GetId()}.Build(),
		Name:     "newco.com",
		Provider: "entra",
	}.Build())
	x.NoError(err)

	tn, _ := pdid.From(b.Tenant.GetId())

	t.Run("whose-host answers the tenant, ready for the next command", func(t *testing.T) {
		x := require.New(t)

		out, err := cliRun(t, &b.Hers, "front", "whose-host", "newco.example.com")
		x.NoError(err)
		x.Equal(tn.String(), strings.TrimSpace(out))
	})

	t.Run("a name nothing serves is NotFound, not an empty answer", func(t *testing.T) {
		x := require.New(t)

		_, err := cliRun(t, &b.Hers, "front", "whose-host", "stranger.example.com")
		x.Equal(codes.NotFound, status.Code(err))
	})

	t.Run("where-from answers the provider, and empty is an answer", func(t *testing.T) {
		x := require.New(t)

		out, err := cliRun(t, &b.Hers, "front", "where-from", tn.String(), "somebody@newco.com")
		x.NoError(err)
		x.Equal("entra", strings.TrimSpace(out))

		out, err = cliRun(t, &b.Hers, "front", "where-from", tn.String(), "somebody@gmail.com")
		x.NoError(err)
		x.Empty(strings.TrimSpace(out), "a domain this deployment says nothing about")
	})

	t.Run("and locally it is a refusal that names the local question", func(t *testing.T) {
		x := require.New(t)

		_, err := cliRun(t, &b.Local, "front", "whose-host", "newco.example.com")
		x.Error(err)
		x.ErrorContains(err, "client.addr")
		x.ErrorContains(err, "host ls")
	})
}
