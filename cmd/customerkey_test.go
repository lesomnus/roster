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

// stdoutOf runs a command and answers with what it wrote to stdout.
//
// `roster key add` prints the token there and the sentence about it to stderr,
// which is the split that lets `$(roster key add …)` be the key and nothing
// else. So a test that wants the token has to read the stream the design put it
// on, rather than a buffer the command was handed.
func stdoutOf(t *testing.T, k *xli.Command, args ...string) string {
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
	x.NoError(err, "%s: %s", strings.Join(args, " "), b)

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
// somebody is running from a terminal.
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

	token := stdoutOf(t, cmd.NewCmdKey(&c), "add",
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

// TestRevokingReachesTheKeyItNames is a credential that was reported stopped
// and had not been.
//
// `key revoke` erased on the **control plane** and nowhere else, and the
// generated `Erase` answers no error for a row that is not there. So revoking
// one of a customer's keys -- which `key add --tenant` mints, one command over
// -- printed nothing, exited zero, and left the key working. That is the worst
// direction for this particular act: somebody stopping a credential they
// believe is leaked, being told it stopped.
//
// Nothing about the identifier says which plane it came from; both are `ApiKey`
// rows in the same domain from the same generator. So it is a lookup, and one
// on neither plane is a refusal rather than a silent success.
func TestRevokingReachesTheKeyItNames(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)

	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "newco"}.Build())
	x.NoError(err)

	h, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:  "alice",
	}.Build())
	x.NoError(err)

	// Bound, because a key is never wider than the person it hangs off: one on
	// somebody who holds nothing opens nothing, and this test would then pass
	// on the wrong refusal.
	r, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: tn.GetId()}.Build(),
		Alias:   "reader",
		Methods: []string{"/roster.MeService/Get"},
	}.Build())
	x.NoError(err)

	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.NoError(s.Close())

	theirs := stdoutOf(t, cmd.NewCmdKey(&c), "add",
		"--tenant", "newco", "--holder", "alice", "--allow", "/roster.MeService/Get")
	ours := stdoutOf(t, cmd.NewCmdKey(&c), "add",
		"--service", "custody", "--allow", "/roster.HolderService/Get")

	// Both, which is the other half: this listed the control plane's alone, so
	// the key the command beside it had just minted appeared nowhere.
	vs := stdoutOf(t, cmd.NewCmdKey(&c), "list")
	t.Logf("LIST:\n%s", vs)
	x.Contains(vs, "@newco/alice/default", "a customer's key is not listed")
	x.Contains(vs, "@owner/custody/default")

	id := func(at string) string {
		t.Helper()

		for _, line := range strings.Split(vs, "\n") {
			if strings.Contains(line, at) {
				return strings.Fields(line)[0]
			}
		}

		t.Fatalf("no key for %s in:\n%s", at, vs)

		return ""
	}

	s2, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s2.Close() })

	conn := served(t, s2)

	// Two probes, because the two kinds of key are not two of the same thing.
	// An `rt_` **resolves to its holder**, so `MeService` answers about a
	// person; an `rk_` presents as the key row and is nobody, so the same call
	// is truthfully `NotFound` for it. Asking each what it is for is what makes
	// a refusal here mean the key stopped rather than the probe being wrong.
	refused := func(err error) bool {
		t.Helper()
		if err == nil {
			return false
		}

		require.Equal(t, codes.Unauthenticated, status.Code(err), "%v", err)

		return true
	}

	theirsWorks := func() bool {
		t.Helper()

		_, err := app.NewMeServiceClient(conn).Get(bearing(ctx, theirs),
			app.MeGetRequest_builder{}.Build())

		return !refused(err)
	}

	oursWorks := func() bool {
		t.Helper()

		_, err := app.NewHolderServiceClient(conn).Get(bearing(ctx, ours),
			app.HolderGetRequest_builder{
				Ref: app.HolderRef_builder{Id: h.GetId()}.Build(),
			}.Build())

		return !refused(err)
	}

	x.True(theirsWorks(), "the key does not work before it is revoked, so this proves nothing")
	x.True(oursWorks())
	x.NoError(s2.Close())

	x.NoError(cmd.NewCmdKey(&c).Run(ctx, []string{"revoke", "--id", id("@newco/alice")}))

	s3, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s3.Close() })

	conn = served(t, s3)
	x.False(theirsWorks(), "a revoked key still opens the door")
	x.True(oursWorks(), "revoking one key stopped another")

	t.Run("and it is gone from the listing", func(t *testing.T) {
		x := require.New(t)

		// Erased rows filtered, which reading ent directly does not do: erasure
		// is applied by the servers and this is under them.
		x.NotContains(stdoutOf(t, cmd.NewCmdKey(&c), "list"), "@newco/alice/default")
	})

	t.Run("and a key on neither plane is a refusal", func(t *testing.T) {
		x := require.New(t)

		err := cmd.NewCmdKey(&c).Run(ctx, []string{"revoke", "--id", id("@newco/alice")})
		x.Error(err, "revoking a key that is not there reported success")
		x.ErrorContains(err, "either plane")
	})
}

// TestAllowIsALisHoweverItIsWritten.
//
// `--allow` was `flg.String`, which takes the **last** occurrence. That is
// right for `--config` and every other *choose one* flag and silently wrong for
// a list: four `--allow` flags minted a key allowing the fourth and nothing
// else, and the only sign was the line the command prints saying `allowing 1
// method(s)`.
//
// Found in another app's runbook, which documented exactly that form -- so
// somebody following it got a key that fails on its first call, with a refusal
// naming a method they believed they had granted.
//
// The fix is the flag type. xli already had `flg.Strings`, which appends per
// occurrence, so nothing was missing anywhere but here. Both forms work and so
// does mixing them, because a list is a list.
func TestAllowIsAListHoweverItIsWritten(t *testing.T) {
	const (
		verify    = "/roster.VouchService/Verify"
		introspec = "/payday.TokenService/Introspect"
		get       = "/roster.HolderService/Get"
	)

	for _, tc := range []struct {
		desc string
		args []string
	}{
		{"repeated", []string{"--allow", verify, "--allow", introspec, "--allow", get}},
		{"comma separated", []string{"--allow", verify + "," + introspec + "," + get}},
		{"mixed", []string{"--allow", verify + "," + introspec, "--allow", get}},
		{"and the spacing a runbook leaves", []string{
			"--allow", verify + ", " + introspec, "--allow", " " + get}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)
			ctx := t.Context()

			c := seedbed(t)

			out, err := initRun(t, c)
			x.NoError(err, "init: %s", out)

			args := append([]string{"add", "--service", "custody"}, tc.args...)
			x.NotEmpty(stdoutOf(t, cmd.NewCmdKey(&c), args...))

			s, err := cmd.Build(ctx, c)
			x.NoError(err)
			t.Cleanup(func() { s.Close() })

			v, err := s.Control.Ent.ApiKey.Query().Only(ctx)
			x.NoError(err)
			x.Equal([]string{verify, introspec, get}, v.Methods,
				"the key does not allow what the command line said")
		})
	}

	t.Run("and none of them is still a refusal", func(t *testing.T) {
		x := require.New(t)

		c := seedbed(t)

		out, err := initRun(t, c)
		x.NoError(err, "init: %s", out)

		err = cmd.NewCmdKey(&c).Run(t.Context(), []string{"add", "--service", "custody"})
		x.Error(err, "a key that allows nothing is not a key")
		x.ErrorContains(err, "--allow")
	})
}
