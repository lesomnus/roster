package cmd_test

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/user"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/config/brokerpg"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	entaccess "github.com/lesomnus/roster/internal/ent/access"
	app "github.com/lesomnus/roster/rstr"
)

// What is read and written at a shell on the server's own box, on the access
// record (#221).
//
// A command there reaches `Ungated` on a pipe in a process of its own. Before
// this the pipe carried nothing of the serving chain, so `roster holder get`
// showed somebody's row to whoever ran it and left no row of the access record
// at all -- where the same read over the wire is one, naming the caller.

// box is a deployment as `roster init` leaves it, with a customer the
// operator made at the shell: the configuration a command at the box reads,
// the operator `--as admin` names, and the control plane's tenant they are
// held by.
type box struct {
	c cmd.Config

	operator pdid.Id
	owner    pdid.Id
	newco    pdid.Id
	admin    pdid.Id
}

func atTheBox(t *testing.T) box {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	c := seedbed(t)
	out, err := initRun(t, c)
	x.NoError(err, "init: %s", out)

	_, err = entities(t, at(c, "admin"), "tenant", "add", "@newco")
	x.NoError(err)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()

	b := box{c: c}
	b.operator, err = cmd.ControlHolder(ctx, s.Control, "admin")
	x.NoError(err)
	b.owner = pdid.Id(s.Control.Ent.Tenant.Query().OnlyIdX(ctx))
	b.admin, err = cmd.CustomerOf(ctx, s, "newco", "admin")
	x.NoError(err)
	b.newco = pdid.Id(s.Ent.Tenant.Query().OnlyIdX(ctx))

	return b
}

// at is a copy of `c` for a command run as `--as` says: a copy, because
// `--as` is a flag a process is given once, and a configuration that kept it
// from one command to the next would be a shell that remembered who it was.
func at(c cmd.Config, as string) *cmd.Config {
	c.Client.As = as
	return &c
}

// accessed is the access record of `plane`, by the RPC it was of.
func accessed(t *testing.T, c cmd.Config, control bool, method string) []*ent.Access {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()

	db := s.Ent
	if control {
		db = s.Control.Ent
	}
	vs, err := db.Access.Query().Where(entaccess.ActionEQ(method)).All(ctx)
	x.NoError(err)

	return vs
}

// whereFrom is what a row says of where a call came from.
func whereFrom(t *testing.T, v *ent.Access) string {
	t.Helper()

	d := &pdpb.AccessDetail{}
	require.NoError(t, proto.Unmarshal(v.Value, d))

	return d.GetAddress()
}

// TestAReadAtTheBoxIsOnTheAccessRecord: a read made with the CLI on the box
// is a row of the access record naming the operator `--as` named -- the same
// actor an admin console's session names them as -- written before the command
// prints anything; and what it writes, and what it reads of the control plane,
// and the names `key list` prints, are rows naming them too.
func TestAReadAtTheBoxIsOnTheAccessRecord(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	out, err := entities(t, at(b.c, "admin"), "holder", "get", "@newco/admin")
	x.NoError(err)
	x.Contains(out, "admin")

	rs := accessed(t, b.c, false, app.HolderService_Get_FullMethodName)
	x.Len(rs, 1, "the read is on the record")
	x.Equal("read", rs[0].Kind)
	x.Equal("ok", rs[0].Result)
	x.Equal(b.newco.Uuid(), rs[0].TenantId, "filed under the tenant whose person it was")
	x.Equal(b.operator.Uuid(), rs[0].ActorId, "naming the operator --as named")
	x.Equal(b.owner.Uuid(), rs[0].ActorTenantId, "held by the control plane, as a console's session frames them")
	x.Contains(rs[0].Subjects, b.admin.Bytes())

	// Where from: the shell, by its user and host, which is how a reviewer
	// tells it from a console's address.
	where := whereFrom(t, rs[0])
	x.True(strings.HasPrefix(where, "shell"), where)
	if u, err := user.Current(); err == nil {
		x.Contains(where, u.Username)
	}
	if h, err := os.Hostname(); err == nil {
		x.Contains(where, h)
	}

	t.Run("and what it writes is a row naming them", func(t *testing.T) {
		x := require.New(t)

		_, err := entities(t, at(b.c, "admin"), "holder", "add", "@newco/erin")
		x.NoError(err)

		rs := accessed(t, b.c, false, app.HolderService_Add_FullMethodName)
		x.Len(rs, 1)
		x.Equal("write", rs[0].Kind)
		x.Equal(b.operator.Uuid(), rs[0].ActorId)
		x.Equal(b.owner.Uuid(), rs[0].ActorTenantId)
	})

	t.Run("and the control plane's own, on its own record", func(t *testing.T) {
		x := require.New(t)

		out, err := entities(t, at(b.c, "admin"), "control", "holder", "ls")
		x.NoError(err)
		x.Contains(out, "admin")

		rs := accessed(t, b.c, true, app.HolderService_List_FullMethodName)
		x.Len(rs, 1, "the operators' rows are the control plane's, and so is the record of reading them")
		x.Equal(b.operator.Uuid(), rs[0].ActorId)
		x.Equal(b.owner.Uuid(), rs[0].TenantId)
		x.Contains(rs[0].Subjects, b.operator.Bytes())

		x.Empty(accessed(t, b.c, false, app.HolderService_List_FullMethodName))
	})

	t.Run("and the names key list prints, on each plane's", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()

		k := cli.NewCmdControl(at(b.c, ""))
		k.Writer = io.Discard
		x.NoError(k.Run(ctx, []string{"key", "add", "--allow", "/roster.HolderService/Get", "loft"}))
		_, err := entities(t, at(b.c, "admin"), "key", "add", "--tenant", "newco", "--holder", "erin", "--allow", "/roster.HolderService/Get")
		x.NoError(err)

		out := captured(t, func() error {
			_, err := entities(t, at(b.c, "admin"), "key", "list")
			return err
		})
		x.Contains(out, "/loft/")
		x.Contains(out, "@newco/erin/")

		s, err := cmd.Build(ctx, b.c)
		x.NoError(err)
		defer s.Close()
		loft, err := cmd.ControlHolder(ctx, s.Control, "loft")
		x.NoError(err)
		erin, err := cmd.CustomerOf(ctx, s, "newco", "erin")
		x.NoError(err)

		rs := accessed(t, b.c, true, app.ApiKeyService_List_FullMethodName)
		x.Len(rs, 1)
		x.Equal("read", rs[0].Kind)
		x.Equal(b.operator.Uuid(), rs[0].ActorId)
		x.Contains(rs[0].Subjects, loft.Bytes())

		rs = accessed(t, b.c, false, app.ApiKeyService_List_FullMethodName)
		x.Len(rs, 1)
		x.Equal(b.operator.Uuid(), rs[0].ActorId)
		x.Equal(b.newco.Uuid(), rs[0].TenantId)
		x.Equal([][]byte{erin.Bytes()}, rs[0].Subjects)
	})
}

// captured is what `run` printed to stdout, which is where `key list` prints:
// it is not one of the entity tree's, which print to the command's writer.
func captured(t *testing.T, run func() error) string {
	t.Helper()
	x := require.New(t)

	r, w, err := os.Pipe()
	x.NoError(err)
	was := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	err = run()
	os.Stdout = was
	w.Close()
	out := <-done
	x.NoError(err)

	return out
}

// TestACommandAtTheBoxIsToldWhoIsThere: a command at the box of a deployment
// with operators names one, or is refused before it opens anything -- and an
// operator who is not there, or is stopped, is refused too. Nothing is shown,
// and nothing recorded as nobody's.
func TestACommandAtTheBoxIsToldWhoIsThere(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	out, err := entities(t, at(b.c, ""), "holder", "get", "@newco/admin")
	x.ErrorIs(err, cmd.ErrUnnamed)
	x.NotContains(out, "admin")

	_, err = entities(t, at(b.c, ""), "control", "holder", "ls")
	x.ErrorIs(err, cmd.ErrUnnamed)
	_, err = entities(t, at(b.c, ""), "key", "list")
	x.ErrorIs(err, cmd.ErrUnnamed)

	out, err = entities(t, at(b.c, "nobody"), "holder", "get", "@newco/admin")
	x.ErrorContains(err, "no operator is called that")
	x.NotContains(out, "admin")

	t.Run("and one who is stopped does not act", func(t *testing.T) {
		x := require.New(t)

		k := cli.NewCmdControl(at(b.c, ""))
		k.Writer = io.Discard
		x.NoError(k.Run(t.Context(), []string{"key", "add", "--allow", "/roster.HolderService/Get", "grace"}))
		_, err := entities(t, at(b.c, "admin"), "control", "holder", "disable", "@owner/grace")
		x.NoError(err)

		out, err := entities(t, at(b.c, "grace"), "holder", "get", "@newco/admin")
		x.ErrorContains(err, "disabled")
		x.NotContains(out, "admin")
	})

	t.Run("and over the wire it is the credential's, so a name is refused", func(t *testing.T) {
		x := require.New(t)

		c := at(b.c, "admin")
		c.Client.Addr = "127.0.0.1:1"
		_, err := entities(t, c, "holder", "get", "@newco/admin")
		x.ErrorContains(err, "over the wire")
	})

	x.Empty(accessed(t, b.c, false, app.HolderService_Get_FullMethodName), "nothing was read, so nothing is recorded")
}

// TestAReadAtTheBoxThatCannotBeRecordedIsNotShown: a read whose row of the
// access record cannot be written is refused, rather than printed and left
// out of the record -- and a write whose row cannot be is not made.
func TestAReadAtTheBoxThatCannotBeRecordedIsNotShown(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()
	b := atTheBox(t)

	// The record's table taken away, which is the plainest way for a row of
	// it to be one the database will not take.
	s, err := cmd.Build(ctx, b.c)
	x.NoError(err)
	_, err = s.Db.ExecContext(ctx, `DROP TABLE access`)
	x.NoError(err)
	s.Close()

	out, err := entities(t, at(b.c, "admin"), "holder", "get", "@newco/admin")
	x.ErrorContains(err, "could not be recorded")
	x.NotContains(out, b.admin.String(), "shown, and on no record")

	_, err = entities(t, at(b.c, "admin"), "holder", "add", "@newco/zed")
	x.Error(err)

	s, err = cmd.Build(ctx, b.c)
	x.NoError(err)
	defer s.Close()
	_, err = cmd.CustomerOf(ctx, s, "newco", "zed")
	x.Error(err, "a write whose record could not be written was made")
}

// TestABoxWithNoOperatorsRecordsTheDeploymentAndTheShell: a deployment with no
// control plane has nobody `--as` could name -- `init` refuses to make one, and
// a sandbox has none -- so a read there is recorded as the deployment's, from
// this shell, and naming somebody is refused.
func TestABoxWithNoOperatorsRecordsTheDeploymentAndTheShell(t *testing.T) {
	x := require.New(t)
	c := seeded(t)

	out, err := entities(t, c, "holder", "get", "@contoso/admin")
	x.NoError(err)
	x.Contains(out, "admin")

	rs := accessed(t, *c, false, app.HolderService_Get_FullMethodName)
	x.Len(rs, 1)
	x.Equal(pdid.Nil.Uuid(), rs[0].ActorId, "nobody to name")
	x.True(strings.HasPrefix(whereFrom(t, rs[0]), "shell"))

	_, err = entities(t, at(*c, "admin"), "holder", "get", "@contoso/admin")
	x.ErrorContains(err, "no control plane")
}

// TestAWatchAtTheBoxThatHearsOnlyItselfIsRefused (#221): under `memory` the
// box's broker is its own process's, which publishes nothing the deployment
// does, so a watch there is refused with a sentence naming the wire -- rather
// than answering once and then hearing nothing, on a stream that looks open.
func TestAWatchAtTheBoxThatHearsOnlyItselfIsRefused(t *testing.T) {
	x := require.New(t)
	b := atTheBox(t)

	_, err := entities(t, at(b.c, "admin"), "holder", "watch", "@newco/admin")
	x.ErrorContains(err, "FailedPrecondition")
	x.ErrorContains(err, "client.addr")
	x.ErrorContains(err, "watch.broker")

	x.Empty(accessed(t, b.c, false, app.HolderService_Watch_FullMethodName))
}

// TestAWatchAtTheBoxHearsTheDeploymentWhereItsBrokerDoes: under a broker every
// process hears, a watch at the box hears what the serving process publishes --
// a write made at another shell, drained from the outbox -- and each message is
// on the record, naming the operator, before it is printed.
func TestAWatchAtTheBoxHearsTheDeploymentWhereItsBrokerDoes(t *testing.T) {
	x := require.New(t)
	if os.Getenv(pdtest.Postgres) == "" {
		t.Skipf("%s is not set; a broker that crosses processes needs the database that carries it", pdtest.Postgres)
	}

	b := atTheBox(t)
	b.c.Watch = config.WatchConfig{Broker: brokerpg.Name}
	b.c.Control.Watch = config.WatchConfig{Broker: config.BrokerMemory}

	// The serving process, which drains what a shell queues.
	ctx := t.Context()
	s, err := cmd.Build(ctx, b.c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	sctx, stop := context.WithCancel(ctx)
	spun := make(chan error, 1)
	go func() { spun <- spin.Run(sctx, slices.Values(s.Spin)) }()
	t.Cleanup(func() {
		stop()
		<-spun
	})

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	k := cli.Cmd(at(b.c, "admin"))
	k.Writer = pw
	watched := make(chan error, 1)
	go func() {
		watched <- k.Run(wctx, []string{"holder", "watch", "@newco/admin"})
		pw.Close()
	}()

	lines := make(chan string, 32)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	// The first answer is what is there; a line naming what was done to her
	// is the deployment's -- a write at another shell, queued, drained by the
	// serving process, published to the broker, and heard here.
	got := ""
	for i := range 100 {
		verb := "disable"
		if i%2 == 1 {
			verb = "enable"
		}
		_, err := entities(t, at(b.c, "admin"), "holder", verb, "@newco/admin")
		x.NoError(err)

		deadline := time.After(200 * time.Millisecond)
	read:
		for {
			select {
			case v := <-lines:
				got += v + "\n"
			case <-deadline:
				break read
			}
		}
		if strings.Contains(got, app.HolderService_Disable_FullMethodName) {
			break
		}
	}
	x.Contains(got, app.HolderService_Disable_FullMethodName, "the watch at the box heard nothing the deployment did")

	cancel()
	<-watched

	rs := accessed(t, b.c, false, app.HolderService_Watch_FullMethodName)
	x.NotEmpty(rs, "what the watch showed is on the record")
	for _, v := range rs {
		x.Equal("read", v.Kind)
		x.Equal(b.operator.Uuid(), v.ActorId)
	}
}
