package cmd_test

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// minted runs a command and answers with what it wrote to stdout.
//
// `roster key add` prints the token there and the sentence about it to stderr,
// which is the split that lets `$(roster key add …)` be the key and nothing
// else. So a test that wants the token has to read the stream the design put it
// on, rather than a buffer the command was handed.
func minted(t *testing.T, k *xli.Command, args ...string) string {
	t.Helper()
	x := require.New(t)

	r, w, err := os.Pipe()
	x.NoError(err)

	was := os.Stdout
	os.Stdout = w

	err = k.Run(t.Context(), args)

	os.Stdout = was
	x.NoError(w.Close())

	b, e := io.ReadAll(r)
	x.NoError(e)
	x.NoError(err, "key add: %s", b)

	return strings.TrimSpace(string(b))
}

// TestTheCliMintsACustomersKey is the last step of standing a customer up from
// a terminal, and until now there was not one.
//
// `roster init` seeds no customer, so somebody creates the first one -- and
// everything that creates it is a local command already: `tenant add`,
// `holder add`, `role add`, `binding add`, all through `Ungated`, with no rules
// at all. Then nothing. `key add` was the control plane's alone and said so:
// *a key for somebody inside a tenant is not something a shell on the box
// should be handing out.*
//
// Which is not a boundary. The same shell has just bound `/roster.*/*` to that
// person; refusing the key refuses the last step of something already wholly
// permitted, and what it made necessary was a browser -- for a deployment
// somebody is running from a terminal. PLAN.md D57.
func TestTheCliMintsACustomersKey(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	// The customer, the way a terminal makes one: four local writes.
	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)

	h, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:  "admin",
	}.Build())
	x.NoError(err)

	r, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:   "everything",
		Methods: []string{"/roster.*/*"},
	}.Build())
	x.NoError(err)

	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	token := minted(t, cmd.NewCmdKey(&c), "add",
		"--tenant", "newco", "--holder", "admin", "--allow", "/roster.*/*")

	// The prefix is a fact about which plane answered and never something a
	// caller names; `issue.proto` says why. Here it is a fact about which flags
	// were given, which is the same decision made one layer out.
	x.True(strings.HasPrefix(token, keys.PrefixTenant),
		"the customer's key is the deployment's own kind: %q", token)

	// A second server, because the command closed the one it built.
	s2, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s2.Close() })

	conn := served(t, s2)

	t.Run("and it answers as the person, not as the shell that made it", func(t *testing.T) {
		x := require.New(t)

		v, err := app.NewMeServiceClient(conn).Get(bearing(ctx, token),
			app.MeGetRequest_builder{}.Build())
		x.NoError(err)
		x.Equal("admin", v.GetAlias())
		x.Equal(tn.GetId(), v.GetTenant())
		x.Equal([]string{"/roster.*/*"}, v.GetMethods())
	})

	t.Run("and it is walled to their tenant", func(t *testing.T) {
		x := require.New(t)

		vs, err := app.NewTenantServiceClient(conn).List(bearing(ctx, token),
			app.TenantListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(vs.GetItems(), 1)
		x.Equal("newco", vs.GetItems()[0].GetAlias())
	})
}

// TestNamingACustomersPersonDoesNotCreateThem is the half `serviceOf` decides
// the other way, and the difference is the wall.
//
// The control plane has one tenant, so naming a service **is** the moment it
// becomes a caller -- asking for three commands to express one intent is how a
// runbook grows a step nobody remembers. The data plane has many, a customer's
// people are the customer's, and a command that made one by mentioning them
// would write rows into somebody else's tenant by typo. `IssueKeyRequest.holder`
// already states exactly this about the same act over the wire.
func TestNamingACustomersPersonDoesNotCreateThem(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	_, err = s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	err = cmd.NewCmdKey(&c).Run(ctx, []string{"add",
		"--tenant", "newco", "--holder", "nobody", "--allow", "/roster.HolderService/Get"})
	x.Error(err, "a key was minted for somebody who is not there")
	x.Equal(codes.NotFound, status.Code(err))
	x.ErrorContains(err, "@newco/nobody", "the refusal does not say who was not found")

	s2, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s2.Close() })

	n, err := s2.Ent.Holder.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n, "naming a customer's person created them")
}

// TestAKeyIsForOnePlaneOrTheOther, said by the flags rather than by a --kind
// nobody would get right.
func TestAKeyIsForOnePlaneOrTheOther(t *testing.T) {
	c := seedbed(t)

	for _, tc := range []struct {
		desc string
		args []string
		says string
	}{
		{"both at once", []string{"--service", "custody", "--tenant", "newco", "--holder", "admin"}, "name one"},
		{"neither", nil, "--service"},
		{"a person and no tenant", []string{"--holder", "admin"}, "--tenant"},
		{"a tenant and nobody in it", []string{"--tenant", "newco"}, "--holder"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			err := cmd.NewCmdKey(&c).Run(t.Context(),
				append([]string{"add", "--allow", "/roster.HolderService/Get"}, tc.args...))
			x.Error(err)
			x.ErrorContains(err, tc.says)
		})
	}
}
