package cmd_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/admission"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	entgm "github.com/lesomnus/roster/internal/ent/groupmembership"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// A hook a deployment refuses a write by (#305): a service the file names, or
// a policy an app hands over in Go, asked about each row a write it covers is
// about to commit, once every rule of roster's has let it through. These are
// what it is told, what its answers do, what it is never asked about, and
// what a file that names one cannot say.

// hookHeard is a hook on a loopback listener: what it was asked, the metadata
// each question came with, and what it answers.
type hookHeard struct {
	app.UnimplementedHookServiceServer

	mu      sync.Mutex
	reviews []*app.WriteReview
	mds     []metadata.MD
	answer  func(context.Context, *app.WriteReview) (*app.WriteVerdict, error)
}

func (h *hookHeard) Review(ctx context.Context, r *app.WriteReview) (*app.WriteVerdict, error) {
	md, _ := metadata.FromIncomingContext(ctx)

	h.mu.Lock()
	h.reviews = append(h.reviews, proto.Clone(r).(*app.WriteReview))
	h.mds = append(h.mds, md)
	h.mu.Unlock()

	if h.answer == nil {
		return &app.WriteVerdict{}, nil
	}

	return h.answer(ctx, r)
}

func (h *hookHeard) asked() []*app.WriteReview {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]*app.WriteReview(nil), h.reviews...)
}

// hookListening stands a hook up on a loopback port and answers with its
// address.
func hookListening(t *testing.T, answer func(context.Context, *app.WriteReview) (*app.WriteVerdict, error)) (*hookHeard, string) {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	h := &hookHeard{answer: answer}
	g := grpc.NewServer()
	app.RegisterHookServiceServer(g, h)
	go g.Serve(l)
	t.Cleanup(g.Stop)

	return h, l.Addr().String()
}

// hookAt is a configuration naming a hook at `addr`, asked about `writes`.
func hookAt(addr string, writes ...string) func(*cmd.Config) {
	return func(c *cmd.Config) {
		c.Hook = cmd.HookConfig{Addr: addr, Insecure: true, Writes: writes}
	}
}

// hookGroup is a group of Contoso's with nobody in it, made at the box.
func hookGroup(t *testing.T, ctx context.Context, s *cmd.Server, in pdid.Id, alias string) pdid.Id {
	t.Helper()

	v, err := s.Ungated.Group().Add(ctx, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:  alias,
		Labels: map[string]string{"kept-by": "people-team"},
	}.Build())
	require.NoError(t, err)

	return mustId(t, v.GetId())
}

// hookMay binds `who` a role allowing `method` across their tenant, made at
// the box.
func hookMay(t *testing.T, ctx context.Context, s *cmd.Server, in, who pdid.Id, method string) {
	t.Helper()
	x := require.New(t)

	role, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:   "hooked-" + who.String(),
		Methods: []string{method},
	}.Build())
	x.NoError(err)

	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
}

// hookMembers is how many places `group` has, of anybody.
func hookMembers(t *testing.T, ctx context.Context, s *cmd.Server, group pdid.Id) int {
	t.Helper()

	n, err := s.Ent.GroupMembership.Query().Where(entgm.GroupIdEQ(group.Uuid()), entgm.DateErasedIsNil()).Count(ctx)
	require.NoError(t, err)

	return n
}

// TestAWriteAHookCoversIsReviewedWithWhatTheContractSays is the whole of what
// `hook.proto` promises a hook's author, asked of a real service: an app
// acting for a person by a delegation puts somebody in a group, and the hook
// is told the method, who is asking and for whom and how they arrived, the
// request, the entity, the row's identifier and tenant, the row after and no
// row before, and its labels -- and is sent nothing of the caller's
// credential. It answers *go on*, and the write is there.
func TestAWriteAHookCoversIsReviewedWithWhatTheContractSays(t *testing.T) {
	x := require.New(t)
	h, addr := hookListening(t, nil)

	add := app.GroupMembershipService_Add_FullMethodName
	b := keyedFor(t, hookAt(addr, "/roster.GroupMembershipService/*"), add)
	ctx := t.Context()
	hookMay(t, ctx, b.Server, b.Contoso, b.Who, add)
	group := hookGroup(t, ctx, b.Server, b.Contoso, "readers")
	other := addHolder(t, ctx, b.Server, b.Contoso, "other")
	token := delegates(t, ctx, b, b.Who, []string{add}, time.Hour)

	req := app.GroupMembershipAddRequest_builder{
		Group:  app.GroupRef_builder{Id: group.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: other.Bytes()}.Build(),
	}.Build()
	v, err := app.NewGroupMembershipServiceClient(b.Conn).Add(acting(ctx, b.Token, token), req)
	x.NoError(err)
	x.Equal(1, hookMembers(t, ctx, b.Server, group))

	rs := h.asked()
	x.Len(rs, 1, "one row was written, and the hook is asked once for it")
	r := rs[0]
	x.Equal(add, r.GetMethod())
	x.Equal("roster.GroupMembership", r.GetEntity())
	x.Equal(v.GetId(), r.GetId())
	x.Equal(b.Contoso.Bytes(), r.GetTenant())

	// Who: the app that called, by the key it presented as itself, acting by
	// a delegation for the person who handed it over.
	a := r.GetAsker()
	x.Equal("roster.ApiKey", a.GetKind())
	x.Equal(issuerOf(t, ctx, b), a.GetId())
	x.Empty(a.GetTenant(), "a deployment key is held by no tenant")
	x.Equal(app.WriteArrival_WRITE_ARRIVAL_DELEGATION, a.GetArrived())
	x.NotEmpty(a.GetDelegation())
	x.Equal("roster.Holder", a.GetOnBehalfOf().GetKind())
	x.Equal(b.Who.Bytes(), a.GetOnBehalfOf().GetId())
	x.Equal(b.Contoso.Bytes(), a.GetOnBehalfOf().GetTenant())

	// What was sent, as it was sent.
	sent := &app.GroupMembershipAddRequest{}
	x.NoError(r.GetRequest().UnmarshalTo(sent))
	x.True(proto.Equal(req, sent))

	// An add has no row before, and the row after is the one written.
	x.False(r.HasBefore())
	after := &app.GroupMembership{}
	x.NoError(r.GetAfter().UnmarshalTo(after))
	x.Equal(v.GetId(), after.GetId())
	x.Empty(r.GetLabelsBefore())
	x.Empty(r.GetLabelsAfter(), "a place in a group was written with no labels")

	// And nothing of the caller's credential travels.
	for _, k := range []string{"authorization", "roster-as", "cookie", "roster-at"} {
		x.Empty(h.mds[0].Get(k), "the hook was sent %s", k)
	}
}

// TestNoSecretOfARequestOrARowReachesAHook: a key ending a delegation by its
// token is asked about with the token cleared from the request and the
// delegation's verifier cleared from the row, as payday clears what the
// schema declares and roster declares the rest (`hookSecrets`). And a key
// calling as itself is a key, with no tenant.
func TestNoSecretOfARequestOrARowReachesAHook(t *testing.T) {
	x := require.New(t)
	h, addr := hookListening(t, nil)

	b := keyedFor(t, hookAt(addr, revoke), revoke)
	ctx := t.Context()
	token := delegates(t, ctx, b, b.Who, []string{listHolders}, time.Hour)

	_, err := app.NewDelegationServiceClient(b.Conn).Revoke(bearing(ctx, b.Token),
		app.DelegationRevokeRequest_builder{Token: token}.Build())
	x.NoError(err)

	rs := h.asked()
	x.NotEmpty(rs)
	for _, r := range rs {
		x.Equal(revoke, r.GetMethod())
		x.Equal(app.WriteArrival_WRITE_ARRIVAL_KEY, r.GetAsker().GetArrived())
		x.Equal("roster.ApiKey", r.GetAsker().GetKind())
		x.Empty(r.GetAsker().GetTenant())

		sent := &app.DelegationRevokeRequest{}
		x.NoError(r.GetRequest().UnmarshalTo(sent))
		x.Empty(sent.GetToken(), "the token a delegation is ended by reached the hook")

		for _, row := range []interface {
			GetValue() []byte
		}{r.GetBefore(), r.GetAfter()} {
			if len(row.GetValue()) == 0 {
				continue
			}
			d := &app.Delegation{}
			x.NoError(proto.Unmarshal(row.GetValue(), d))
			x.Empty(d.GetSecret(), "a delegation's verifier reached the hook")
		}
	}
	x.True(rs[0].HasBefore(), "an end of a delegation is asked about with the row as it was")
}

// TestAHookThatRefusesLeavesNothingWritten: the caller is told
// `PermissionDenied`, in the hook's words said to be the hook's and bounded as
// roster bounds a sentence it repeats; nothing was written; and the refusal is
// on the access record as a refusal by roster's own rules is.
func TestAHookThatRefusesLeavesNothingWritten(t *testing.T) {
	x := require.New(t)
	_, addr := hookListening(t, func(context.Context, *app.WriteReview) (*app.WriteVerdict, error) {
		return app.WriteVerdict_builder{Refused: true, Why: "readers are kept by the people team\n" + strings.Repeat("x", 200)}.Build(), nil
	})

	b, ctx := build(t, hookAt(addr, "/roster.GroupMembershipService/Add"))
	b.mayAnything(b.ContosoUser, b.Contoso)
	group := hookGroup(t, ctx, b.Server, b.Contoso, "readers")
	other := b.holder(t, ctx, b.Contoso, "other")
	_ = filed(t, ctx, b.Server, app.GroupMembershipService_Add_FullMethodName)

	_, err := app.NewGroupMembershipServiceClient(served(t, b.Server)).Add(asOverTheWire(ctx, b.ContosoUser),
		app.GroupMembershipAddRequest_builder{
			Group:  app.GroupRef_builder{Id: group.Bytes()}.Build(),
			Holder: app.HolderRef_builder{Id: other.Bytes()}.Build(),
		}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	msg := status.Convert(err).Message()
	x.True(strings.HasPrefix(msg, `refused by this deployment's hook: "readers are kept by the people team\n`), msg)
	x.NotContains(msg, "\n", "a control character of the hook's reached the caller")
	x.Contains(msg, "(236 bytes)", "the hook's words were not bounded")
	x.Zero(hookMembers(t, ctx, b.Server, group))

	rs := filed(t, ctx, b.Server, app.GroupMembershipService_Add_FullMethodName)
	x.NotEmpty(rs, "the refusal is not on the access record")
	for _, r := range rs {
		x.Equal(codes.PermissionDenied.String(), r.Result)
	}
}

// TestAHookThatDoesNotAnswerWritesNothing: a hook that answers past its
// deadline, one nothing is listening for, and one that answers with an error
// are each no answer, and never *go on*: nothing is written and the caller is
// told `Unavailable`, that this deployment's hook did not answer.
func TestAHookThatDoesNotAnswerWritesNothing(t *testing.T) {
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	nobody := closed.Addr().String()
	require.NoError(t, closed.Close())

	_, slow := hookListening(t, func(ctx context.Context, _ *app.WriteReview) (*app.WriteVerdict, error) {
		<-ctx.Done()
		return &app.WriteVerdict{}, nil
	})
	_, failing := hookListening(t, func(context.Context, *app.WriteReview) (*app.WriteVerdict, error) {
		return nil, status.Error(codes.PermissionDenied, "this is an error and not a verdict")
	})

	for name, addr := range map[string]string{
		"past its deadline":       slow,
		"with nobody listening":   nobody,
		"answering with an error": failing,
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			b, ctx := build(t, hookAt(addr, "/roster.GroupMembershipService/Add"), func(c *cmd.Config) {
				c.Hook.Timeout = 50 * time.Millisecond
			})
			b.mayAnything(b.ContosoUser, b.Contoso)
			group := hookGroup(t, ctx, b.Server, b.Contoso, "readers")
			other := b.holder(t, ctx, b.Contoso, "other")

			_, err := app.NewGroupMembershipServiceClient(served(t, b.Server)).Add(asOverTheWire(ctx, b.ContosoUser),
				app.GroupMembershipAddRequest_builder{
					Group:  app.GroupRef_builder{Id: group.Bytes()}.Build(),
					Holder: app.HolderRef_builder{Id: other.Bytes()}.Build(),
				}.Build())
			x.Equal(codes.Unavailable, status.Code(err), "%v", err)
			x.Equal("this deployment's hook did not answer, and nothing was written", status.Convert(err).Message())
			x.Zero(hookMembers(t, ctx, b.Server, group))
		})
	}
}

// TestAWriteNoPatternCoversAsksNobody: a hook about places in groups is not
// asked about a group's own row.
func TestAWriteNoPatternCoversAsksNobody(t *testing.T) {
	x := require.New(t)
	h, addr := hookListening(t, func(context.Context, *app.WriteReview) (*app.WriteVerdict, error) {
		return app.WriteVerdict_builder{Refused: true, Why: "no"}.Build(), nil
	})

	b, ctx := build(t, hookAt(addr, "/roster.GroupMembershipService/*"))
	b.mayAnything(b.ContosoUser, b.Contoso)

	_, err := app.NewGroupServiceClient(served(t, b.Server)).Add(asOverTheWire(ctx, b.ContosoUser),
		app.GroupAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
			Alias:  "writers",
		}.Build())
	x.NoError(err)
	x.Empty(h.asked())
}

// TestAVerbIsAskedAboutUnderItsOwnName: a hand-written verb is asked about
// under the name its caller called, for each row it writes -- `Holder.Disable`
// writes the person and ends what they hold -- and never under the names of
// the generated writes it makes underneath.
func TestAVerbIsAskedAboutUnderItsOwnName(t *testing.T) {
	x := require.New(t)
	h, addr := hookListening(t, nil)

	b, ctx := build(t, hookAt(addr, app.HolderService_Disable_FullMethodName))
	b.mayAnything(b.ContosoUser, b.Contoso)
	other := b.holder(t, ctx, b.Contoso, "other")

	was, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: other.Bytes()}.Build()}.Build())
	x.NoError(err)

	_, err = app.NewHolderServiceClient(served(t, b.Server)).Disable(asOverTheWire(ctx, b.ContosoUser),
		app.HolderDisableRequest_builder{
			Ref:         app.HolderRef_builder{Id: other.Bytes()}.Build(),
			DateUpdated: was.GetDateUpdated(),
		}.Build())
	x.NoError(err)

	rs := h.asked()
	x.NotEmpty(rs)
	holders := 0
	for _, r := range rs {
		x.Equal(app.HolderService_Disable_FullMethodName, r.GetMethod())
		if r.GetEntity() == "roster.Holder" && string(r.GetId()) == string(other.Bytes()) {
			holders++
			x.True(r.HasBefore())
			x.True(r.HasAfter())
		}
	}
	x.Equal(1, holders, "the person disabled is asked about once")
}

// TestASignInWithTheHookDownSucceeds: no way in asks a hook, so one that is
// down shuts nobody out -- whatever its patterns say, `/roster.*/*` included.
func TestASignInWithTheHookDownSucceeds(t *testing.T) {
	x := require.New(t)
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	nobody := closed.Addr().String()
	x.NoError(closed.Close())

	b := keyedFor(t, hookAt(nobody, "/roster.*/*"), verify)
	ctx := t.Context()
	_, err = b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	vouch := app.NewVouchServiceClient(b.Conn)
	for _, secret := range []string{"wrong horse", "correct horse battery staple"} {
		v, err := vouch.Verify(bearing(ctx, b.Token), app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.Who.Bytes()}.Build(),
			Secret: []byte(secret),
		}.Build())
		x.NoError(err, "a sign-in asked a hook that is down")
		x.Equal(secret != "wrong horse", v.GetOk())
	}
}

// TestAHookThatCannotBeMeantIsRefusedAtStart: each, by its key, in what it
// was and what would be right.
func TestAHookThatCannotBeMeantIsRefusedAtStart(t *testing.T) {
	for _, v := range []struct {
		name string
		hook cmd.HookConfig
		says string
	}{
		{"an address with no writes", cmd.HookConfig{Addr: "hook.internal:9443"},
			"hook.writes: names no write"},
		{"writes with no address", cmd.HookConfig{Writes: []string{"/roster.GroupMembershipService/*"}},
			"hook.writes: says something of a hook, and hook.addr names no service"},
		{"a timeout with no address", cmd.HookConfig{Timeout: time.Second},
			"hook.timeout: says something of a hook"},
		{"a timeout below none", cmd.HookConfig{Addr: "hook.internal:9443", Timeout: -time.Second, Writes: []string{"/roster.GroupMembershipService/*"}},
			"hook.timeout: -1s is less than no time"},
		{"a timeout past thirty seconds", cmd.HookConfig{Addr: "hook.internal:9443", Timeout: time.Minute, Writes: []string{"/roster.GroupMembershipService/*"}},
			"hook.timeout: 1m0s cannot be meant"},
		{"a pattern that covers nothing", cmd.HookConfig{Addr: "hook.internal:9443", Writes: []string{"/roster.GroupMembershipService/*", "/roster.GroupService/Leave"}},
			`hook.writes[1]: "/roster.GroupService/Leave" covers no method roster serves`},
		{"a pattern that covers only reads", cmd.HookConfig{Addr: "hook.internal:9443", Writes: []string{"/roster.*/List"}},
			`hook.writes[0]: "/roster.*/List" covers only reads`},
		{"a hand-written read", cmd.HookConfig{Addr: "hook.internal:9443", Writes: []string{app.HolderService_SignsIn_FullMethodName}},
			`hook.writes[0]: "/roster.HolderService/SignsIn" covers only reads`},
		{"a way in", cmd.HookConfig{Addr: "hook.internal:9443", Writes: []string{app.CredentialService_Set_FullMethodName}},
			`hook.writes[0]: "/roster.CredentialService/Set" is a way in`},
		{"a service of ways in", cmd.HookConfig{Addr: "hook.internal:9443", Writes: []string{"/roster.VouchService/*"}},
			`hook.writes[0]: "/roster.VouchService/*" is a way in`},
	} {
		t.Run(v.name, func(t *testing.T) {
			drv, dsn := pdtest.DB(t)
			_, err := cmd.Build(t.Context(), cmd.Config{Db: config.DbConfig{Driver: drv, Dsn: dsn}, Watch: config.WatchConfig{Broker: config.BrokerMemory}, Hook: v.hook})
			require.ErrorContains(t, err, v.says)
		})
	}

	// And the authority a hook is verified by, read as a listener's
	// `tls.client_ca` is.
	pair := startCertificateOf(t)
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	require.NoError(t, os.WriteFile(garbage, []byte("no certificate"), 0o600))
	writes := []string{"/roster.GroupMembershipService/*"}
	for _, v := range []struct {
		name string
		hook cmd.HookConfig
		says string
	}{
		{"an authority with no address", cmd.HookConfig{Ca: startCertificateFile(t, pair.cert)},
			"hook.ca: says something of a hook"},
		{"an authority beside insecure", cmd.HookConfig{Addr: "hook.internal:9443", Writes: writes, Insecure: true, Ca: startCertificateFile(t, pair.cert)},
			"hook.ca: names an authority to verify the hook by, and hook.insecure dials it with no TLS"},
		{"an authority that is not there", cmd.HookConfig{Addr: "hook.internal:9443", Writes: writes, Ca: startCertificateFile(t, filepath.Join(t.TempDir(), "gone.pem"))},
			"hook.ca: "},
		{"an authority that is none", cmd.HookConfig{Addr: "hook.internal:9443", Writes: writes, Ca: startCertificateFile(t, garbage)},
			"hook.ca: holds no certificate"},
	} {
		t.Run(v.name, func(t *testing.T) {
			err := cmd.Config{Hook: v.hook}.Meant()
			require.Error(t, err)
			require.True(t, strings.HasPrefix(err.Error(), v.says), "named: %s", err)
		})
	}

	t.Run("and a pattern over everything is everything but reads and ways in", func(t *testing.T) {
		drv, dsn := pdtest.DB(t)
		s, err := cmd.Build(t.Context(), cmd.Config{Db: config.DbConfig{Driver: drv, Dsn: dsn}, Watch: config.WatchConfig{Broker: config.BrokerMemory},
			Hook: cmd.HookConfig{Addr: "hook.internal:9443", Writes: []string{"/roster.*/*", "/roster.CredentialService/*"}}})
		require.NoError(t, err)
		s.Close()
	})

	t.Run("and an app's policy with nothing to ask about", func(t *testing.T) {
		drv, dsn := pdtest.DB(t)
		_, err := cmd.Build(t.Context(), cmd.Config{Db: config.DbConfig{Driver: drv, Dsn: dsn}, Watch: config.WatchConfig{Broker: config.BrokerMemory},
			Admission: cmd.Admission{Policy: admission.Func(func(context.Context, admission.Call) error { return nil })}})
		require.ErrorContains(t, err, "Config.Admission.Writes: names no write")
	})
}

// hookAboutAnother is every write one person makes about another's way in,
// and never about their own or at a sign-in: what `hook.writes` takes as it
// takes any write.
var hookAboutAnother = []string{
	app.BadgeService_Enrol_FullMethodName,
}

// TestAWriteAboutSomebodyElsesWayInIsAskedAboutAsAnyWriteIs: a deployment
// that wants its hook asked before a badge is given to somebody names it in
// `hook.writes`, and the hook is asked. It was refused at start as a way in
// (#606), whose reason -- a hook that is down shuts nobody out -- is true of
// signing in and not of an administrator's write about somebody else: a hook
// that is down then holds up that write, which is what the deployment asked
// for. End to end: the hook refuses an administrator's badge for somebody,
// and no badge is written.
func TestAWriteAboutSomebodyElsesWayInIsAskedAboutAsAnyWriteIs(t *testing.T) {
	for _, m := range hookAboutAnother {
		require.False(t, cmd.WayIn(m), "%s is a way in", m)
		require.NoError(t, cmd.Config{Hook: cmd.HookConfig{Addr: "hook.internal:9443", Writes: []string{m}}}.Meant(), m)
	}

	x := require.New(t)
	h, addr := hookListening(t, func(context.Context, *app.WriteReview) (*app.WriteVerdict, error) {
		return app.WriteVerdict_builder{Refused: true, Why: "badges are the front desk's"}.Build(), nil
	})
	b, ctx := build(t, hookAt(addr, app.BadgeService_Enrol_FullMethodName))
	b.mayAnything(b.ContosoUser, b.Contoso)
	park := b.holder(t, ctx, b.Contoso, "park")

	_, err := app.NewBadgeServiceClient(served(t, b.Server)).Enrol(asOverTheWire(ctx, b.ContosoUser), app.BadgeEnrolRequest_builder{
		Holder: app.HolderRef_builder{Id: park.Bytes()}.Build(),
		Code:   "0042-7781",
		Pin:    "2580",
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	x.Equal(`refused by this deployment's hook: "badges are the front desk's"`, status.Convert(err).Message())

	rs := h.asked()
	x.NotEmpty(rs, "the hook was not asked")
	x.Equal(app.BadgeService_Enrol_FullMethodName, rs[0].GetMethod())

	n, err := b.Ent.Badge.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n, "a badge the hook refused was written")
}

// TestAWayInIsAskedAboutWhereTheFileSaysSo: a pattern that names how somebody
// gets in or stays in -- signing in, a link spent, a provider's word taken, a
// session minted, a person's own password or key -- is refused at start,
// naming `hook.ways_in` and what saying it means: a hook that does not answer
// then shuts out everybody who signs in that way, and the box is where that is
// put right. With `hook.ways_in: true` the same pattern is taken, and the hook
// is asked about it.
func TestAWayInIsAskedAboutWhereTheFileSaysSo(t *testing.T) {
	patterns := []string{"/roster.VouchService/*", "/roster.AuthService/*"}
	for _, m := range cmd.Served(pd.Sink{}) {
		if cmd.WayIn(m) && !cmd.Reads(m) {
			patterns = append(patterns, m)
		}
	}
	require.Contains(t, patterns, app.CredentialService_Issue_FullMethodName)

	for _, p := range patterns {
		t.Run(p, func(t *testing.T) {
			x := require.New(t)
			hook := cmd.HookConfig{Addr: "hook.internal:9443", Writes: []string{p}}

			err := cmd.Config{Hook: hook}.Meant()
			x.Error(err)
			x.True(strings.HasPrefix(err.Error(), fmt.Sprintf("hook.writes[0]: %q is a way in", p)), "named: %s", err)
			x.Contains(err.Error(), "a hook that does not answer then shuts out everybody who signs in that way, "+
				"and the box is where it is put right. say hook.ways_in: true")

			hook.WaysIn = true
			x.NoError(cmd.Config{Hook: hook}.Meant())
		})
	}

	t.Run("and with it the hook is asked", func(t *testing.T) {
		x := require.New(t)
		h, addr := hookListening(t, nil)
		b, ctx := build(t, hookAt(addr, app.CredentialService_Issue_FullMethodName), func(c *cmd.Config) {
			c.Hook.WaysIn = true
		})
		b.mayAnything(b.ContosoUser, b.Contoso)
		other := b.holder(t, ctx, b.Contoso, "other")

		_, err := app.NewCredentialServiceClient(served(t, b.Server)).Issue(asOverTheWire(ctx, b.ContosoUser), app.CredentialIssueRequest_builder{
			Holder: app.HolderRef_builder{Id: other.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		x.NotEmpty(h.asked())
		x.Equal(app.CredentialService_Issue_FullMethodName, h.asked()[0].GetMethod())
	})

	t.Run("and ways_in with no address says something of a hook", func(t *testing.T) {
		err := cmd.Config{Hook: cmd.HookConfig{WaysIn: true}}.Meant()
		require.ErrorContains(t, err, "hook.ways_in: says something of a hook")
	})
}

// TestAPolicyInTheProcessIsAskedAsAServiceIs: an app that runs roster in its
// own process hands `cmd` a policy, and its answers are told a caller in the
// words a hook's service would be: a refusal is the deployment's hook
// refusing, and an error is the hook not answering.
func TestAPolicyInTheProcessIsAskedAsAServiceIs(t *testing.T) {
	for name, v := range map[string]struct {
		answer error
		code   codes.Code
		says   string
	}{
		"going on":       {nil, codes.OK, ""},
		"refusing":       {status.Error(codes.FailedPrecondition, "readers are kept by the people team"), codes.PermissionDenied, `refused by this deployment's hook: "readers are kept by the people team"`},
		"failing":        {errors.New("the policy's own store is down"), codes.Unavailable, "this deployment's hook did not answer, and nothing was written"},
		"busy elsewhere": {status.Error(codes.Unavailable, "down"), codes.Unavailable, "this deployment's hook did not answer, and nothing was written"},
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			var asked []admission.Call
			b, ctx := build(t, func(c *cmd.Config) {
				c.Admission = cmd.Admission{
					Policy: admission.Func(func(_ context.Context, c admission.Call) error {
						asked = append(asked, c)
						return v.answer
					}),
					Writes: []string{"/roster.GroupMembershipService/Add"},
				}
			})
			b.mayAnything(b.ContosoUser, b.Contoso)
			group := hookGroup(t, ctx, b.Server, b.Contoso, "readers")
			other := b.holder(t, ctx, b.Contoso, "other")

			_, err := app.NewGroupMembershipServiceClient(served(t, b.Server)).Add(asOverTheWire(ctx, b.ContosoUser),
				app.GroupMembershipAddRequest_builder{
					Group:  app.GroupRef_builder{Id: group.Bytes()}.Build(),
					Holder: app.HolderRef_builder{Id: other.Bytes()}.Build(),
				}.Build())
			x.Equal(v.code, status.Code(err), "%v", err)
			x.Equal(v.says, status.Convert(err).Message())
			x.Len(asked, 1)
			x.Equal(app.GroupMembershipService_Add_FullMethodName, asked[0].Method)
			if v.code == codes.OK {
				x.Equal(1, hookMembers(t, ctx, b.Server, group))
			} else {
				x.Zero(hookMembers(t, ctx, b.Server, group))
			}
		})
	}
}

// TestNoHookBuildsTheStackItAlwaysWas: with no hook nothing of admission is
// installed -- a write the deployment makes through ent outside any
// transaction is made, as it always was -- and with one, payday's guard is on
// the client, and such a write is refused unless it says it is the
// deployment's own.
func TestNoHookBuildsTheStackItAlwaysWas(t *testing.T) {
	if cmd.AdmittingEverything() {
		t.Skip("the suite is run with every write asked of a policy")
	}

	for name, with := range map[string]func(*cmd.Config){
		"with no hook": func(*cmd.Config) {},
		"with one":     hookAt("hook.internal:9443", "/roster.GroupMembershipService/*"),
	} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			b, ctx := build(t, with)
			group := hookGroup(t, ctx, b.Server, b.Contoso, "readers")

			_, err := b.Ent.Group.UpdateOneId(group.Uuid()).SetDesc("written past the servers").Save(ctx)
			if name == "with no hook" {
				x.NoError(err)
			} else {
				x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
			}
		})
	}
}

// TestACommandAtTheBoxAsksNoHook: a command at a shell has no caller, and
// what it writes is the deployment's own, so the hook its file names is held
// to its rules and asked by nothing.
func TestACommandAtTheBoxAsksNoHook(t *testing.T) {
	x := require.New(t)
	h, addr := hookListening(t, func(context.Context, *app.WriteReview) (*app.WriteVerdict, error) {
		return app.WriteVerdict_builder{Refused: true, Why: "no"}.Build(), nil
	})

	drv, dsn := pdtest.DB(t)
	c := cmd.Config{Db: config.DbConfig{Driver: drv, Dsn: dsn}, Watch: config.WatchConfig{Broker: config.BrokerMemory}}
	hookAt(addr, "/roster.GroupMembershipService/*")(&c)
	s, err := cmd.Build(t.Context(), c.AtTheBox())
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	ctx := t.Context()
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))

	b := &built{Server: s}
	tenant := b.tenant(t, ctx, "contoso")
	admin := b.holder(t, ctx, tenant, "admin")
	group := hookGroup(t, ctx, s, tenant, "readers")

	_, err = s.Walled.GroupMembership().Add(b.as(ctx, admin, tenant), app.GroupMembershipAddRequest_builder{
		Group:  app.GroupRef_builder{Id: group.Bytes()}.Build(),
		Holder: app.HolderRef_builder{Id: admin.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.Empty(h.asked())

	c.Hook.Writes = []string{"/roster.VouchService/Verify"}
	_, err = cmd.Build(ctx, c.AtTheBox())
	x.ErrorContains(err, "is a way in", "the box holds the file's hook to its rules")
}

// TestEveryVerbIsAReadAWayInOrAWriteAHookIsAskedAbout: a hand-written verb a
// port serves is one of three to a hook, and which is said of each -- a read,
// which nothing asks about; a way in, which no hook is asked about whatever its
// patterns say (`cmd.WayIn`); or a write, which a pattern may name. A verb
// added tomorrow is none of them until somebody says, so that a sign-in is not
// a write a hook can stop by nobody having thought of it.
func TestEveryVerbIsAReadAWayInOrAWriteAHookIsAskedAbout(t *testing.T) {
	asked := []string{
		"/roster.BadgeService/Enrol",
		"/roster.BadgeService/Unlock",
		"/roster.ConnectionService/Update",
		"/roster.ConsentService/Agree",
		"/roster.ConsentService/Withdraw",
		"/roster.CredentialService/Unlock",
		"/roster.DelegationService/Revoke",
		"/roster.GroupService/Update",
		"/roster.HolderService/Activate",
		"/roster.HolderService/Deactivate",
		"/roster.HolderService/Disable",
		"/roster.HolderService/Enable",
		"/roster.HolderService/Invalidate",
		"/roster.HolderService/Portray",
		"/roster.HolderService/Provision",
		"/roster.HolderService/Realias",
		"/roster.HolderService/Tell",
		"/roster.HolderService/Update",
		"/roster.HostService/Update",
		"/roster.MailDomainService/Update",
		"/roster.MeService/Leave",
		"/roster.MeService/SignOutEverywhere",
		"/roster.RoleService/HandsOver",
		"/roster.RoleService/Narrow",
		"/roster.RoleService/Widen",
		"/roster.TenantService/Close",
		"/roster.TenantService/Restore",
		"/roster.TenantService/Update",
	}

	for _, m := range cmd.Served(pd.Sink{}) {
		switch m[strings.LastIndex(m, "/")+1:] {
		case "Add", "AddMany", "Patch", "Apply", "Erase", "Get", "List", "Watch":
			continue
		}
		if cmd.Reads(m) || cmd.WayIn(m) || slices.Contains(asked, m) {
			continue
		}
		t.Errorf("%s is no read, no way in and no write a hook may be asked about: say which", m)
	}
	for _, m := range asked {
		require.False(t, cmd.WayIn(m) || cmd.Reads(m), "%s is said twice", m)
	}
}

// TestAHookBehindItsOwnAuthorityIsVerifiedByIt: a deployment's hook is
// usually behind a private authority, which the system's roots do not know.
// Named in `hook.ca`, the hook's certificate is verified against it, and the
// write is asked about and made; with no authority named, the same hook is
// one roster cannot verify, so it did not answer and nothing is written.
func TestAHookBehindItsOwnAuthorityIsVerifiedByIt(t *testing.T) {
	pair := startCertificateOf(t)
	cert, err := tls.LoadX509KeyPair(pair.cert, pair.key)
	require.NoError(t, err)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	h := &hookHeard{}
	g := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}})))
	app.RegisterHookServiceServer(g, h)
	go g.Serve(l)
	t.Cleanup(g.Stop)

	for name, ca := range map[string]bool{"by its authority": true, "by the system's": false} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			b, ctx := build(t, func(c *cmd.Config) {
				c.Hook = cmd.HookConfig{Addr: l.Addr().String(), Writes: []string{"/roster.GroupMembershipService/Add"}}
				if ca {
					c.Hook.Ca = startCertificateFile(t, pair.cert)
				}
			})
			b.mayAnything(b.ContosoUser, b.Contoso)
			group := hookGroup(t, ctx, b.Server, b.Contoso, "readers")
			before := len(h.asked())

			_, err := app.NewGroupMembershipServiceClient(served(t, b.Server)).Add(asOverTheWire(ctx, b.ContosoUser),
				app.GroupMembershipAddRequest_builder{
					Group:  app.GroupRef_builder{Id: group.Bytes()}.Build(),
					Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
				}.Build())
			if ca {
				x.NoError(err)
				x.Equal(1, hookMembers(t, ctx, b.Server, group))
				x.Len(h.asked(), before+1)
			} else {
				x.Equal(codes.Unavailable, status.Code(err), "%v", err)
				x.Zero(hookMembers(t, ctx, b.Server, group))
				x.Len(h.asked(), before)
			}
		})
	}
}

// TestAHookThatNeverAnswersHoldsUpOnlyWhatItIsAskedAbout is the promise of
// #305: a deployment whose hook is down is still one its people can get into.
//
// A hook that never answers, and payday's limit of decisions in flight reached
// by writes it is asked about, each waiting on it. Then a write no pattern
// covers is made at once, and so is a sign-in through the port -- a right
// password, and a wrong one, whose count is a write -- since payday is told
// which calls the hook is asked about (`admission.Options.Asked`) and admits
// every other before it takes a slot. A further write it is asked about is
// refused at once, `ResourceExhausted`, with no slot to wait in; and those
// waiting are refused `Unavailable`, the hook not having answered, at the
// hook's deadline, with nothing written. Before `Asked`, the write no pattern
// covers and the sign-in's count were refused `ResourceExhausted` with them.
//
// On PostgreSQL alone. SQLite takes one write at a time, so there a write
// waiting on its hook holds every other write until it is answered or its
// deadline passes -- which the manual says -- and the limit is never reached
// by writes at once.
func TestAHookThatNeverAnswersHoldsUpOnlyWhatItIsAskedAbout(t *testing.T) {
	if os.Getenv(pdtest.Postgres) == "" {
		t.Skip("SQLite takes one write at a time: run with PDTEST_POSTGRES")
	}
	x := require.New(t)

	// Never answers: it waits for the test to end, past every deadline.
	release := make(chan struct{})
	h, addr := hookListening(t, func(context.Context, *app.WriteReview) (*app.WriteVerdict, error) {
		<-release
		return &app.WriteVerdict{}, nil
	})
	t.Cleanup(func() { close(release) })

	b, ctx := build(t, hookAt(addr, app.GroupMembershipService_Add_FullMethodName), func(c *cmd.Config) {
		c.Hook.Timeout = 5 * time.Second
	})
	b.mayAnything(b.ContosoUser, b.Contoso)
	_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
		Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)

	conn := served(t, b.Server)
	as := asOverTheWire(ctx, b.ContosoUser)
	// payday's limit, reached by writes the hook is asked about: each a place
	// in a group of its own for somebody of their own, so that none waits on
	// another's row.
	const limit = 32
	places := app.NewGroupMembershipServiceClient(conn)
	groups, people := make([]pdid.Id, limit+1), make([]pdid.Id, limit+1)
	for i := range limit + 1 {
		groups[i] = hookGroup(t, ctx, b.Server, b.Contoso, fmt.Sprintf("waiting-%d", i))
		people[i] = b.holder(t, ctx, b.Contoso, fmt.Sprintf("waiting-%d", i))
	}
	place := func(i int) (pdid.Id, error) {
		_, err := places.Add(as, app.GroupMembershipAddRequest_builder{
			Group:  app.GroupRef_builder{Id: groups[i].Bytes()}.Build(),
			Holder: app.HolderRef_builder{Id: people[i].Bytes()}.Build(),
		}.Build())

		return groups[i], err
	}

	type waited struct {
		group pdid.Id
		err   error
	}
	waiting := make(chan waited, limit)
	for i := range limit {
		go func() {
			group, err := place(i)
			waiting <- waited{group, err}
		}()
	}
	x.Eventually(func() bool { return len(h.asked()) == limit }, time.Minute, 10*time.Millisecond,
		"the writes the hook is asked about did not all reach it")

	// A write no pattern covers: at once, since nothing waits on the hook for it.
	_, err = app.NewGroupServiceClient(conn).Add(as, app.GroupAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "not-asked",
	}.Build())
	x.NoError(err, "a write no pattern covers waited on a hook that is down")

	// A sign-in, through the port: a wrong password, whose count is a write,
	// and the right one.
	vouch := app.NewVouchServiceClient(conn)
	for _, secret := range []string{"wrong horse", "correct horse battery staple"} {
		v, err := vouch.Verify(as, app.VouchVerifyRequest_builder{
			Who:    app.VouchWho_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Secret: []byte(secret),
		}.Build())
		x.NoError(err, "a sign-in waited on a hook that is down")
		x.Equal(secret != "wrong horse", v.GetOk())
	}

	// A further write it is asked about has no slot to wait in.
	_, err = place(limit)
	x.Equal(codes.ResourceExhausted, status.Code(err), "%v", err)

	// And those waiting are refused at the hook's deadline, writing nothing.
	for range limit {
		v := <-waiting
		x.Equal(codes.Unavailable, status.Code(v.err), "%v", v.err)
		x.Equal("this deployment's hook did not answer, and nothing was written", status.Convert(v.err).Message())
		x.Zero(hookMembers(t, ctx, b.Server, v.group))
	}
}
