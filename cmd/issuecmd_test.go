package cmd_test

import (
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

	// `/roster.IssueService/*` is the wildcard an operator reaches for to let a
	// customer self-serve `IssueKey` -- and the review's point was that it also
	// reaches `IssuePassword`. So this test holds it, to prove the server guard
	// (not just a narrow grant) is what refuses the password mint.
	b := cliUp(t, holderGet, "/roster.IssueService/*")

	t.Run("a key for a customer's person, from the data port", func(t *testing.T) {
		x := require.New(t)

		v := stdoutOf(t, cmd.Cmd(&b.Hers), "issue", "key",
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

		err := cmd.Cmd(&b.Hers).Run(ctx, []string{"issue", "key",
			"--name", "x", "--allow", holderGet, "@bob"})
		x.Error(err)
		x.NotContains(err.Error(), "rpc error", "the refusal is the CLI's, before the wire")
		x.ErrorContains(err, "@tenant/alias")
	})

	t.Run("issue password is refused on the data plane, where a bare alias names many", func(t *testing.T) {
		x := require.New(t)

		// The hole the review found: IssuePassword took a bare alias, resolved
		// it against an arbitrary tenant and wrote a password with no reach
		// check. The server refuses it off the control plane now.
		err := cmd.Cmd(&b.Hers).Run(ctx, []string{"issue", "password", "bob"})
		x.Equal(codes.Unimplemented, status.Code(err),
			"a first password on the data plane is an escalation, and is refused")
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
		Methods: []string{"/roster.IssueService/*", "/roster.VouchService/Verify"},
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
	// reads bindings, and a key holds none -- `cmd/issue.go` carries the why.
	// What a key CAN do here is write a first password, since that asks the
	// reach rule and a deployment key covers everything; which is exactly why
	// `roster key add` now warns about it.
	t.Run("a first password for an operator, printed once", func(t *testing.T) {
		x := require.New(t)

		pw := stdoutOf(t, cmd.Cmd(&console), "issue", "password", "ops")
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

	t.Run("and the mint that can become an operator is the one key add warns on", func(t *testing.T) {
		x := require.New(t)

		x.NotEmpty(cmd.Widest([]string{"/roster.IssueService/IssuePassword"}))
		x.NotEmpty(cmd.Widest([]string{"/roster.IssueService/*"}),
			"the warning must survive a wildcard, or reaching for `*` is how it is missed")
	})
}
