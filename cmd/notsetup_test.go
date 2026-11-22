package cmd_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"

	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/prove"
)

// notSetUpResolver is a resolver that answers nothing, for a stack that is to
// be refused before it asks one.
type notSetUpResolver struct{}

func (notSetUpResolver) TXT(context.Context, string) ([]string, error) { return nil, nil }

// TestADeploymentNotSetUpForSomethingIsUnimplemented: what this deployment was
// not set up with -- a resolver, a store it can open a transaction on, an
// owner, a name to arrive at on a listener -- is `Unimplemented`, over HTTP
// 501, from every verb that says so: the code a page hides a feature on, and
// the code the dozen other sentences of the kind already had.
//
// Seven of them said `FailedPrecondition`, the code of a rule's refusal about
// a row, over HTTP 400: a tenant claiming a name on a deployment with no
// `host.resolver`; a server that cannot open a transaction, taking a name or
// writing an address or a consent in more than one write; one that cannot let
// go of a name another tenant holds; a deployment that has no owner, minting a
// key for an operator or signing one in; and a link spent at the operators'
// door, where nothing arrives at a name.
func TestADeploymentNotSetUpForSomethingIsUnimplemented(t *testing.T) {
	x := require.New(t)
	a := assert.New(t)
	b, ctx := build(t, func(c *cmd.Config) { c.Host.Resolver = prove.None })

	// A tenant's administrator claims a name, on a deployment that cannot
	// ask DNS (`host.resolver: none`).
	{
		at := failureServe(t, b, cmd.Config{})
		admin := b.holder(t, ctx, b.Contoso, "not-set-up-admin")
		b.mayAnything(admin, b.Contoso)
		c := failureCall{method: "/roster.HostService/Add", as: admin, req: `{"tenant":` + failureRef(b.Contoso) + `,"name":"not-set-up.example.com"}`}
		a.Equal(codes.Unimplemented.String(), at.code(t, c).String(), "a name claimed with no host.resolver")
		a.Equal(501, at.status(t, c), "a name claimed with no host.resolver, over HTTP")
	}

	// A stack built with no store to open a transaction on, and one built
	// with nothing to let go of a name with.
	{
		under := app.NewOverlay(b.Ungated)
		at := app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build()
		claimer := b.holder(t, ctx, b.Contoso, "not-set-up-claimer")
		b.mayAnything(claimer, b.Contoso)
		claiming := framed(ctx, claimer, b.Contoso)

		noDriver, err := app.Build(under, core.Build(cmd.Rules(b.Ent), core.WithProving(notSetUpResolver{})))
		x.NoError(err)
		_, err = noDriver.Host().Add(claiming, app.HostAddRequest_builder{Tenant: at, Name: "no-driver.example.com"}.Build())
		a.Equal(codes.Unimplemented.String(), status.Code(err).String(), "a name taken with no transaction: %v", err)

		consenter := b.holder(t, ctx, b.Contoso, "not-set-up-app")
		_, err = noDriver.Consent().Agree(framed(ctx, consenter, b.Contoso), app.ConsentAgreeRequest_builder{
			Holder:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Documents: []*app.ConsentDocument{doc("terms", "1", false)},
		}.Build())
		a.Equal(codes.Unimplemented.String(), status.Code(err).String(), "a consent written with no transaction: %v", err)

		releasing := cmd.Rules(b.Ent)
		releasing.Releasing = nil
		noRules, err := app.Build(under, core.Build(releasing, core.On(b.Drv, cmd.Locking(b.Ent)), core.WithProving(notSetUpResolver{})))
		x.NoError(err)
		_, err = noRules.Host().Add(claiming, app.HostAddRequest_builder{Tenant: at, Name: "no-rules.example.com"}.Build())
		a.Equal(codes.Unimplemented.String(), status.Code(err).String(), "a name taken with nothing to let go of it with: %v", err)
	}

	// A deployment with a control plane and nobody owning it: `roster init`
	// has not been run.
	{
		drv, dsn := pdtest.DB(t)
		cdrv, cdsn := pdtest.DB(t)
		c := cmd.Config{
			Db:      config.DbConfig{Driver: drv, Dsn: dsn},
			Watch:   config.WatchConfig{Broker: config.BrokerMemory},
			Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
			Vouch:   cmd.VouchConfig{Keys: []string{"one:" + base64.StdEncoding.EncodeToString(freshKey(t))}},
		}
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })
		x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
		x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))

		_, err = s.Control.Ungated.ApiKey().Issue(ctx, app.ApiKeyIssueRequest_builder{
			HolderAlias: z.Ptr("ops"), Alias: "laptop", Methods: []string{app.MeService_Get_FullMethodName},
		}.Build())
		a.Equal(codes.Unimplemented.String(), status.Code(err).String(), "a key minted for an operator of nobody's deployment: %v", err)

		g, err := s.GrpcAdmin(ctx, c)
		x.NoError(err)
		conn := pdtest.Serve(t, g)
		for _, tc := range []struct{ what, req string }{
			{"signing in to nobody's deployment", `{"alias":"ops","secret":"` + base64.StdEncoding.EncodeToString([]byte("correct horse battery staple")) + `"}`},
			{"a link spent where nothing arrives at a name", `{"link":"a-link"}`},
		} {
			in, out := failureMessages(t, app.AuthService_SignIn_FullMethodName, tc.req)
			err := conn.Invoke(ctx, app.AuthService_SignIn_FullMethodName, in, out)
			a.Equal(codes.Unimplemented.String(), status.Code(err).String(), "%s: %v", tc.what, err)
		}
	}
}
