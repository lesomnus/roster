package cmd_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"uuid"

	payaudit "github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/internal/ent"
	"github.com/lesomnus/roster/internal/ent/audit"
	entsession "github.com/lesomnus/roster/internal/ent/session"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/session"
)

// linesAbout is what a plane's trail says of one thing, oldest first.
func linesAbout(t *testing.T, ctx context.Context, db *ent.Client, id pdid.Id) []*ent.Audit {
	t.Helper()

	vs, err := db.Audit.Query().Where(audit.ObjectId(id.Uuid())).Order(audit.ByDateCreated(), audit.ById()).All(ctx)
	require.NoError(t, err)

	return vs
}

// sessionOf is the row a cookie's value is kept as.
func sessionOf(t *testing.T, ctx context.Context, db *ent.Client, value string) pdid.Id {
	t.Helper()

	v, err := db.Session.Query().Where(entsession.SecretEQ(session.Sum(value))).Only(ctx)
	require.NoError(t, err)

	return pdid.Id(v.Id)
}

// findingsOf is what `roster trail rows` printed of what it found, a finding a
// line, beside its error: on stdout, where a schedule greps it.
func findingsOf(t *testing.T, c *cmd.Config) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	require.NoError(t, err)
	read := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		read <- b
	}()

	was := os.Stdout
	os.Stdout = w
	err = cli.Cmd(c).Run(t.Context(), []string{"trail", "rows"})
	os.Stdout = was
	require.NoError(t, w.Close())

	return string(<-read), err
}

// aSessionsLine holds one line of the trail about a session to what every one
// of them is: about a thing of the session kind, by whoever signed in, under
// their tenant, and with nothing of the cookie or of what the row keeps of it.
func aSessionsLine(x *require.Assertions, v *ent.Audit, act string, who, tenant pdid.Id, value string) {
	x.Equal(act, v.Action)
	x.Equal(uint32(pd.SessionDomain), v.Domain)
	x.Equal(who.Uuid(), v.ActorId, "it does not say who signed in")
	x.Equal(tenant.Uuid(), v.TenantId, "it is not filed under their tenant")
	x.Equal(tenant.Uuid(), v.ActorTenantId)

	for _, kept := range [][]byte{v.Patch, v.Value} {
		x.False(bytes.Contains(kept, []byte(value)), "a line of the trail holds a cookie")
		x.False(bytes.Contains(kept, session.Sum(value)), "a line of the trail holds what a cookie is found by")
	}
}

// TestASessionMadeAtRostersOwnDoorIsOnTheTrail: a sign-in at roster's own door
// leaves a session, and the trail says it was made, used, ended and collected.
//
// # What it was
//
// The store behind the door wrote its rows straight to the database, under an
// identifier of no kind, and said why: *this is the control plane, whose one
// tenant is the deployment's own*. `sign_in.enabled` puts the same store on
// the data plane, where a session is a kind a check of the rows reads (`roster
// trail rows`). So one sign-in on a deployment that keeps digests was a finding
// nothing cleared: `not-on-trail unknown <id>` and a failed check, the same
// after the sign-out; an acceptance that accepted no row, since the recorder
// passes over an identifier of unknown kind; and a session the sweep removed
// with no line, though the manual says what the sweeps collect is on the trail
// (#280).
//
// A session is minted as every other row is, with an identifier of its own
// kind. And what is written of one is on the trail as the act it is, in the
// transaction that wrote it: by whoever signed in, under their tenant, with
// which row and when and nothing of what it holds -- so that the check finds
// the row as its last write left it, the idle clock's among them, which is a
// write as the others are.
func TestASessionMadeAtRostersOwnDoorIsOnTheTrail(t *testing.T) {
	x := require.New(t)

	var c cmd.Config
	b, ctx := build(t, func(cc *cmd.Config) {
		cc.SignIn.Enabled = true
		cc.Audit.Digest.Key = "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		c = *cc
	})
	rows := func() error { return cli.Cmd(&c).Run(ctx, []string{"trail", "rows"}) }
	cs, err := c.Audit.Contents()
	x.NoError(err)

	// A name contoso answers at, and a password for somebody in it.
	_, err = b.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Name:   "contoso.example",
	}.Build())
	x.NoError(err)
	res, err := b.Ungated.Credential().Issue(ctx, app.CredentialIssueRequest_builder{
		Holder: app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
	x.NoError(rows())

	auth := app.NewAuthServiceClient(pdtest.Serve(t, b.grpc(t)))
	at := metadata.NewOutgoingContext(ctx, arrivingAt(t, "contoso.example"))

	var h metadata.MD
	_, err = auth.SignIn(at, app.AuthSignInRequest_builder{Alias: "someone", Secret: []byte(res.GetSecret())}.Build(), grpc.Header(&h))
	x.NoError(err)
	cookie := cookieIn(t, h)
	_, value, _ := strings.Cut(cookie, "=")
	id := sessionOf(t, ctx, b.Ent, value)

	// The store the door keeps its sessions in, as `authsession` asks it.
	store := session.New(b.Ent, cs)

	t.Run("its making is a line, by whoever signed in and under their tenant", func(t *testing.T) {
		x := require.New(t)

		x.Equal(pd.SessionDomain, id.Domain(), "a session was minted under an identifier of no kind")

		vs := linesAbout(t, ctx, b.Ent, id)
		x.Len(vs, 1, "a sign-in is one line about its session")
		aSessionsLine(x, vs[0], session.Minted, b.ContosoUser, b.Contoso, value)

		x.NoError(rows(), "a session somebody signed in for is a finding")
	})

	t.Run("and its idle clock moving is one, which is the row written again", func(t *testing.T) {
		x := require.New(t)

		// What `authsession` writes once a session is used past half its idle
		// window: the same session, with a later deadline.
		was, err := store.Get(ctx, value)
		x.NoError(err)
		was.Idle = was.Idle.Add(10 * time.Minute)
		x.NoError(store.Put(ctx, was))

		vs := linesAbout(t, ctx, b.Ent, id)
		x.Len(vs, 2, "the idle clock moved, and the trail does not say the row was written")
		aSessionsLine(x, vs[1], session.Used, b.ContosoUser, b.Contoso, value)

		x.NoError(rows(), "a session that was used is a finding")
	})

	t.Run("and its ending is one", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.SignOut(metadata.NewOutgoingContext(ctx, metadata.Join(arrivingAt(t, "contoso.example"), metadata.Pairs("cookie", cookie))),
			&app.AuthSignOutRequest{})
		x.NoError(err)
		_, err = store.Get(ctx, value)
		x.ErrorIs(err, authsession.ErrNoSession)

		vs := linesAbout(t, ctx, b.Ent, id)
		x.Len(vs, 3, "a sign-out is a line about its session")
		aSessionsLine(x, vs[2], session.Ended, b.ContosoUser, b.Contoso, value)

		// Ended again, it is ended already: nothing is written, and nothing said.
		x.NoError(store.Del(ctx, value))
		x.Len(linesAbout(t, ctx, b.Ent, id), 3)

		x.NoError(rows(), "a session somebody signed out of is a finding")
	})

	t.Run("and one the sweep collects past its expiry is one", func(t *testing.T) {
		x := require.New(t)

		// A session that ran its whole life and was never signed out of.
		const ran = "a-cookie-that-ran-its-twelve-hours"
		x.NoError(store.Put(ctx, authsession.Session{
			Key: ran, Id: b.ContosoUser.String(), TenantId: b.Contoso.String(),
			Grant: frame.Whole(), Expires: time.Now().Add(-time.Minute),
		}))
		gone := sessionOf(t, ctx, b.Ent, ran)

		n, err := session.Collect(ctx, b.Ent, cs)
		x.NoError(err)
		x.Equal(1, n)

		vs := linesAbout(t, ctx, b.Ent, gone)
		x.Len(vs, 2, "a session was made and collected, and the trail says each")
		aSessionsLine(x, vs[0], session.Minted, b.ContosoUser, b.Contoso, ran)

		// Under the tenant of whoever it was of, and by nobody: the sweep
		// collected it, and a sweep is no holder.
		x.Equal(session.Expired, vs[1].Action)
		x.Equal(uint32(pd.SessionDomain), vs[1].Domain)
		x.Equal(b.Contoso.Uuid(), vs[1].TenantId, "what the sweep collected is not filed under the tenant it was of")
		x.Equal(uuid.Nil(), vs[1].ActorId)

		x.NoError(rows(), "a session the sweep collected is a finding")
	})

	// A deployment that served the door before this has rows the store wrote
	// then, and nothing is rewritten for them. The manual says what a check
	// says of one and that it goes at its expiry (`docs/operating.md`).
	t.Run("and one made before this, under an identifier of no kind, is a finding until the sweep collects it", func(t *testing.T) {
		x := require.New(t)

		old := uuid.New()
		x.NoError(b.Ent.Session.Create().
			SetId(old).
			SetHolderId(b.ContosoUser.Uuid()).
			SetSecret(session.Sum("a-cookie-from-before")).
			SetGrant([]byte{}).
			SetDateExpired(time.Now().Add(-time.Minute)).
			SetDateUpdated(time.Now()).
			SetDateCreated(time.Now()).Exec(bare.TrustAdmission(ctx)))

		found, err := findingsOf(t, &c)
		x.ErrorContains(err, "1 finding(s)")
		x.Contains(found, "not-on-trail unknown "+old.String())

		// It is still signed out of, and still said by nothing: the recorder
		// passes over an identifier of no kind.
		x.NoError(store.Del(ctx, "a-cookie-from-before"))
		x.Empty(linesAbout(t, ctx, b.Ent, pdid.Id(old)))

		n, err := session.Collect(ctx, b.Ent, cs)
		x.NoError(err)
		x.Equal(1, n)
		x.NoError(rows())
	})
}

// TestAnOperatorsSessionIsOnTheControlPlanesTrail is the other door: an
// operator's session is a row of the control plane, kept by the same store,
// and what is written of it is on that plane's trail -- by the operator, under
// the control plane's one tenant.
//
// # What it was
//
// The same rows under the same identifier of no kind, where nothing reads the
// rows against the trail. So what was missing here was only the lines: when
// whoever runs the deployment signed in at the admin console and when they
// signed out was on no trail, and the session the sweep collected was said by
// nothing (#280).
func TestAnOperatorsSessionIsOnTheControlPlanesTrail(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, out := inited(t)
	c := signIn(t, s, "admin", passwordFrom(t, out))
	x.NotNil(c, "the operator init printed cannot sign in")

	operator, err := cmd.ControlHolder(ctx, s.Control, "admin")
	x.NoError(err)
	owner := pdid.Id(s.Control.Ent.Tenant.Query().OnlyIdX(ctx))

	id := sessionOf(t, ctx, s.Control.Ent, c.Value)
	x.Equal(pd.SessionDomain, id.Domain(), "a session was minted under an identifier of no kind")

	vs := linesAbout(t, ctx, s.Control.Ent, id)
	x.Len(vs, 1, "a sign-in is one line about its session")
	aSessionsLine(x, vs[0], session.Minted, operator, owner, c.Value)

	// Signed out where it was signed in, at the admin listener.
	g, err := s.GrpcAdmin(ctx, cmd.Config{})
	x.NoError(err)
	_, err = app.NewAuthServiceClient(pdtest.Serve(t, g)).SignOut(
		metadata.NewOutgoingContext(ctx, metadata.Pairs("cookie", c.Name+"="+c.Value)), &app.AuthSignOutRequest{})
	x.NoError(err)

	vs = linesAbout(t, ctx, s.Control.Ent, id)
	x.Len(vs, 2, "a sign-out is a line about its session")
	aSessionsLine(x, vs[1], session.Ended, operator, owner, c.Value)

	// And one that ran its whole life, collected by the control plane's sweep
	// with what that plane's recorder is handed.
	const ran = "an-operators-cookie-that-ran-its-twelve-hours"
	x.NoError(session.New(s.Control.Ent, payaudit.Contents{}).Put(ctx, authsession.Session{
		Key: ran, Id: operator.String(), TenantId: owner.String(),
		Grant: frame.Whole(), Expires: time.Now().Add(-time.Minute),
	}))
	gone := sessionOf(t, ctx, s.Control.Ent, ran)

	n, err := session.Collect(ctx, s.Control.Ent, payaudit.Contents{})
	x.NoError(err)
	x.Equal(1, n)

	vs = linesAbout(t, ctx, s.Control.Ent, gone)
	x.Len(vs, 2, "a session was made and collected, and the trail says each")
	aSessionsLine(x, vs[0], session.Minted, operator, owner, ran)
	x.Equal(session.Expired, vs[1].Action)
	x.Equal(owner.Uuid(), vs[1].TenantId, "what the sweep collected is not filed under the tenant it was of")

	// Each plane's sessions are its own rows, and so are the lines about them.
	about, err := s.Ent.Audit.Query().Where(audit.DomainEQ(uint32(pd.SessionDomain))).Count(ctx)
	x.NoError(err)
	x.Zero(about, "an operator's session is on the data plane's trail")
}
