package cmd_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// cliServed is a deployment on a real listener, one customer with two people in
// it, and the configuration alice's shell points at it: an address and her
// `rt_`, no database anywhere near her.
type cliServed struct {
	Server *cmd.Server
	Local  cmd.Config
	Hers   cmd.Config

	Tenant *app.Tenant
	Alice  *app.Holder
	Bob    *app.Holder
}

// cliUp is D58's sentence as a fixture: the same binary, remote mode, walled
// and gated like any other caller. `methods` is what alice's role names -- and
// therefore also the widest her key can be.
func cliUp(t *testing.T, methods ...string) *cliServed {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)

	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},

		// A keyring, so the served deployment can wrap a TOTP seed -- which is
		// what lets `roster vouch enrol` be tested against it.
		Vouch: cmd.VouchConfig{Keys: []string{"one:" + base64.StdEncoding.EncodeToString(freshKey(t))}},
	}

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)

	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)

	alice, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:  "alice",
	}.Build())
	x.NoError(err)

	bob, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:  "bob",
	}.Build())
	x.NoError(err)

	r, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:   "operator",
		Methods: methods,
	}.Build())
	x.NoError(err)

	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: alice.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	token := stdoutOf(t, cmd.NewCmdKey(&c), "add",
		"--tenant", "newco", "--holder", "alice", "--name", "terminal",
		"--allow", strings.Join(methods, ","))

	s2, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s2.Close() })

	g, err := s2.Grpc(ctx, cmd.Config{})
	x.NoError(err)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	t.Cleanup(func() { g.Stop() })
	go func() { _ = g.Serve(l) }()

	return &cliServed{
		Server: s2,
		Local:  c,
		Hers: cmd.Config{
			Client: cmd.ClientConfig{
				Addr:     l.Addr().String(),
				Insecure: true,
				Auth:     cmd.ClientAuthConfig{Scheme: "bearer", Credential: token},
			},
		},
		Tenant: tn,
		Alice:  alice,
		Bob:    bob,
	}
}

// cliRun is one invocation at alice's shell, and what it printed.
func cliRun(t *testing.T, c *cmd.Config, args ...string) (string, error) {
	t.Helper()

	out := &bytes.Buffer{}
	k := root(t, c)
	k.Writer = out

	err := k.Run(t.Context(), args)

	return out.String(), err
}

// holderOf reads the row back through the serving stack, which is what the
// commands under test are supposed to have changed.
func holderOf(t *testing.T, b *cliServed, id []byte) *app.Holder {
	t.Helper()
	x := require.New(t)

	v, err := b.Server.Ungated.Holder().Get(t.Context(), app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: id}.Build(),
	}.Build())
	x.NoError(err)

	return v
}

// TestTheTerminalOperatesOnSomebody is the six methods the overlay declared,
// each from a shell, over the wire, through every rule -- the second of D58's
// two buckets.
//
// The commands are built by `pdcmd.Tree.Unary` from the descriptors, so what
// is being asserted is the wiring in `cmd/entity.go` and nothing about the
// methods themselves; `cmd/disable_test.go` and its neighbours hold those.
func TestTheTerminalOperatesOnSomebody(t *testing.T) {
	const (
		holderGet  = "/roster.HolderService/Get"
		update     = "/roster.HolderService/Update"
		disable    = "/roster.HolderService/Disable"
		enable     = "/roster.HolderService/Enable"
		invalidate = "/roster.HolderService/Invalidate"
		signsIn    = "/roster.HolderService/SignsIn"
		revokeKey  = "/roster.HolderService/RevokeKey"
		issueKey   = "/roster.ApiKeyService/Issue"
		meGet      = "/roster.MeService/Get"
	)

	b := cliUp(t, holderGet, update, disable, enable, invalidate, signsIn, revokeKey, issueKey, meGet)

	t.Run("disable is a stop, and enable ends it", func(t *testing.T) {
		x := require.New(t)

		_, err := cliRun(t, &b.Hers, "holder", "disable", "@newco/bob")
		x.NoError(err)
		x.True(holderOf(t, b, b.Bob.GetId()).HasDateDisabled(), "the command answered and wrote nothing")

		_, err = cliRun(t, &b.Hers, "holder", "enable", "@newco/bob")
		x.NoError(err)
		x.False(holderOf(t, b, b.Bob.GetId()).HasDateDisabled())
	})

	t.Run("invalidate stamps now, asking nobody for a time", func(t *testing.T) {
		x := require.New(t)

		_, err := cliRun(t, &b.Hers, "holder", "invalidate", "@newco/bob")
		x.NoError(err)

		v := holderOf(t, b, b.Bob.GetId())
		x.True(v.HasDateInvalidated())
		x.WithinDuration(time.Now(), v.GetDateInvalidated().AsTime(), time.Minute)
	})

	t.Run("update replaces the profile, and wants the version the caller read", func(t *testing.T) {
		x := require.New(t)

		// Refused without one, which is `Patch`'s rule arriving intact: an
		// unset version cannot be told apart from a caller who never
		// considered locking at all.
		_, err := cliRun(t, &b.Hers, "holder", "update", "@newco/bob",
			`{"profile":{"display_name":"Robert"}}`)
		x.Error(err)

		was := holderOf(t, b, b.Bob.GetId()).GetDateUpdated().AsTime()
		_, err = cliRun(t, &b.Hers, "holder", "update", "@newco/bob",
			fmt.Sprintf(`{"date_updated":"%s","profile":{"display_name":"Robert"}}`,
				was.UTC().Format(time.RFC3339Nano)))
		x.NoError(err)
		x.Equal("Robert", holderOf(t, b, b.Bob.GetId()).GetProfile().GetDisplayName())
	})

	t.Run("signs-in answers her keys among the rest", func(t *testing.T) {
		x := require.New(t)

		out, err := cliRun(t, &b.Hers, "holder", "signs-in", "-o", "json", "@newco/alice")
		x.NoError(err)
		x.Contains(out, "terminal", "the key she is calling with is not in the answer")
	})

	t.Run("revoke-key takes a way in away, from the wire", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()

		// A second key to take away, minted over the wire so nothing here
		// opens the database beside a serving deployment.
		doomed := stdoutOf(t, root(t, &b.Hers), "me", "issue-key",
			"--name", "doomed", "--allow", holderGet)

		ks, err := b.Server.Ungated.ApiKey().List(ctx, app.ApiKeyListRequest_builder{
			Filters: []*app.ApiKeyFilter{app.ApiKeyFilter_builder{
				Holder: app.HolderRef_builder{Id: b.Alice.GetId()}.Build(),
			}.Build()},
		}.Build())
		x.NoError(err)

		var id pdid.Id
		for _, k := range ks.GetItems() {
			if k.GetAlias() == "doomed" {
				id, _ = pdid.From(k.GetId())
			}
		}
		x.False(id.IsZero(), "the minted key has no row")

		_, err = cliRun(t, &b.Hers, "holder", "revoke-key", "@newco/alice",
			fmt.Sprintf(`{"id":"%s"}`, id))
		x.NoError(err)

		// And the token it reached is over, which is what the command is for.
		dead := b.Hers
		dead.Client.Auth.Credential = doomed
		_, err = cliRun(t, &dead, "holder", "get", "@newco/alice")
		x.Equal(codes.Unauthenticated, status.Code(err))
	})

	t.Run("a key without the grant is refused, command or not", func(t *testing.T) {
		x := require.New(t)

		narrow := stdoutOf(t, root(t, &b.Hers), "me", "issue-key",
			"--name", "narrow", "--allow", holderGet)

		hers := b.Hers
		hers.Client.Auth.Credential = narrow
		_, err := cliRun(t, &hers, "holder", "disable", "@newco/bob")
		x.Equal(codes.PermissionDenied, status.Code(err),
			"a command is not a grant; the gate answers as it would for any caller")
	})
}
