package cmd_test

import (
	"github.com/lesomnus/roster/cli"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/vouch"
)

// TestIssueMintsOverTheWire is `roster issue` on both planes: a customer's
// person minting for somebody in their tenant against the data port, and a
// deployment service minting -- and handing out a first password -- against
// the control port. Which kind comes back is which port was dialed, never a
// field.
func TestIssueMintsOverTheWire(t *testing.T) {
	const holderGet = "/roster.HolderService/Get"

	x := require.New(t)
	ctx := t.Context()

	// The password mint is held here on purpose, so that what refuses it on the
	// data plane is the **server** rather than a narrow grant. It used to be
	// `/roster.IssueService/*`, one wildcard reaching both mints, which is the
	// shape the review objected to; the two live on the entities they write now
	// and a role names each.
	b := cliUp(t, holderGet, "/roster.ApiKeyService/Issue", "/roster.CredentialService/Issue")

	t.Run("a key for a customer's person, from the data port", func(t *testing.T) {
		x := require.New(t)

		v := stdoutOf(t, cli.Cmd(&b.Hers), "issue", "key",
			"--name", "bots", "--allow", holderGet, "@newco/bob")
		x.True(strings.HasPrefix(v, keys.PrefixTenant), "%q", v)

		ks, err := b.Server.Ungated.ApiKey().List(ctx, app.ApiKeyListRequest_builder{
			Filters: []*app.ApiKeyFilter{app.ApiKeyFilter_builder{
				Holder: app.HolderRef_builder{Id: b.Bob.GetId()}.Build(),
			}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(ks.GetItems(), 1, "the mint answered a token and wrote no row")
		x.Equal("bots", ks.GetItems()[0].GetAlias())
	})

	t.Run("a name with no tenant is refused before the wire, naming the mistake", func(t *testing.T) {
		x := require.New(t)

		err := cli.Cmd(&b.Hers).Run(ctx, []string{"issue", "key",
			"--name", "x", "--allow", holderGet, "@bob"})
		x.Error(err)
		x.NotContains(err.Error(), "rpc error", "the refusal is the CLI's, before the wire")
		x.ErrorContains(err, "@tenant/alias")
	})

	t.Run("issue password is refused on the data plane, where a bare alias names many", func(t *testing.T) {
		x := require.New(t)

		// The hole the review found: `IssuePassword` took a bare alias, resolved
		// it against an arbitrary tenant and wrote a password with no reach
		// check. `Credential.Issue` refuses the bare alias off the control
		// plane -- `service` is the form that creates whoever it names, and a
		// name is one person only where there is one tenant. A customer's
		// person is `ref` or `email`, in full, and `roster vouch reset` is that
		// call.
		err := cli.Cmd(&b.Hers).Run(ctx, []string{"issue", "password", "bob"})
		x.Equal(codes.InvalidArgument, status.Code(err),
			"a bare alias on the data plane named somebody")
	})

	// The control half wants a control-plane caller, which is a deployment
	// key: minted in process, the way `cmd/controlkey_test.go` does, because
	// `roster key add` opens the database this server is already holding.
	ts, err := b.Server.Control.Ungated.Tenant().List(ctx, app.TenantListRequest_builder{Size: 2}.Build())
	x.NoError(err)
	x.Len(ts.GetItems(), 1, "a control plane has one owner")

	svc, err := b.Server.Control.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: ts.GetItems()[0].GetId()}.Build(),
		Alias:  "a-console",
	}.Build())
	x.NoError(err)

	token, sum, err := keys.Mint(keys.PrefixDeployment)
	x.NoError(err)

	_, err = b.Server.Control.Ungated.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder: app.HolderRef_builder{Id: svc.GetId()}.Build(),
		Alias:  "console-key",
		Secret: sum,
		// The mint and the method it will hand out, because nobody hands out
		// what they do not hold -- the same rule a customer's mint is under.
		Methods: []string{"/roster.CredentialService/Issue", "/roster.VouchService/Verify"},
	}.Build())
	x.NoError(err)

	g, err := b.Server.GrpcControl(ctx, b.Local)
	x.NoError(err)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	t.Cleanup(func() { g.Stop() })
	go func() { _ = g.Serve(l) }()

	console := cmd.Config{
		Client: cmd.ClientConfig{
			Addr:     l.Addr().String(),
			Insecure: true,
			Auth:     cmd.ClientAuthConfig{Scheme: "bearer", Credential: token},
		},
	}

	// There is no `issue key --service`: minting is granting, the grant rule
	// reads bindings, and a key holds none -- `cli/issue.go` carries the why.
	//
	// A first password is a different rule and this is the case it allows: the
	// operator being named does not exist yet, so they hold nothing, and
	// `mayReach` passes because there is nothing to escalate to. A key
	// re-issuing for an operator who already holds a role is the case it now
	// refuses, which is what retired the `BecomesAnOperator` warning
	// (`cmd/service.go`).
	t.Run("a first password for an operator, printed once", func(t *testing.T) {
		x := require.New(t)

		pw := stdoutOf(t, cli.Cmd(&console), "issue", "password", "ops")
		x.NotEmpty(pw)

		// And it is theirs: the control plane's own vouch says yes to it.
		res, err := vouch.New(b.Server.Control.Ungated, b.Server.Control.Ungated).Verify(ctx,
			app.VouchVerifyRequest_builder{
				Who: app.VouchWho_builder{
					Tenant: ts.GetItems()[0].GetAlias(),
					Alias:  "ops",
				}.Build(),
				Secret: []byte(pw),
			}.Build())
		x.NoError(err)
		x.True(res.GetOk(), "the printed password is not the one stored")
	})

	// And the rule that retired the warning, asked at the door. A key holds no
	// bindings, so `mayReach` lets it reach somebody who holds nothing -- the
	// subtest above, which is what the mint is *for* -- and nobody who holds a
	// role. That is the difference between naming a new operator and taking
	// over an existing one, and it is wiring rather than a printed NOTE.
	t.Run("and a key cannot re-issue for an operator who holds something", func(t *testing.T) {
		x := require.New(t)

		in := app.TenantRef_builder{Id: ts.GetItems()[0].GetId()}.Build()

		them, err := b.Server.Control.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: in, Alias: "wider",
		}.Build())
		x.NoError(err)

		role, err := b.Server.Control.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant: in, Alias: "runs-it", Methods: []string{"/roster.HolderService/List"},
		}.Build())
		x.NoError(err)

		_, err = b.Server.Control.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: them.GetId()}.Build(),
		}.Build())
		x.NoError(err)

		err = cli.Cmd(&console).Run(ctx, []string{"issue", "password", "wider"})
		x.Equal(codes.PermissionDenied, status.Code(err),
			"a key holding nothing handed out the password of somebody who holds a role")
	})

	// The password mint used to be on `roster key add`'s warning list, because
	// it wrote the column through the generated verbs and so met no reach rule
	// at all -- a key naming it could hand out any operator's password, and a
	// printed NOTE was the whole mitigation. `Credential.Issue` runs the rule,
	// so the wiring says it and the warning is gone.
	//
	// The mechanism is still tested through the one that is still wide, or a
	// warning list that quietly stopped matching anything would read the same.
	t.Run("and the mint no longer needs a warning, while the wide one still does", func(t *testing.T) {
		x := require.New(t)

		x.Empty(cmd.Widest([]string{"/roster.CredentialService/Issue"}))
		x.Empty(cmd.Widest([]string{"/roster.CredentialService/*"}))

		x.NotEmpty(cmd.Widest([]string{"/roster.VouchService/Accept"}))
		x.NotEmpty(cmd.Widest([]string{"/roster.VouchService/*"}),
			"the warning must survive a wildcard, or reaching for `*` is how it is missed")
	})
}
