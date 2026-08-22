package cmd_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/vouch"
)

// TestNoVerifierReachesTheQueueEither.
//
// `trailsecret_test.go` asks this of `Audit`, in both of its columns, and the
// answer came from a redactor payday's trail recorder runs. There are **three**
// recorders behind every write and they read the same `bare.Change`; the
// finding named one and the fix reached one.
//
// So this is the same question one table over. A row in `outbox` holds the
// patch document at rest until something drains it, and what it is drained
// into is whichever broker a deployment names -- nothing carries a patch off
// the box today, which is a property of the two brokers that exist rather than
// of the recorder.
//
// It is roster's test of a property of the **pinned payday**, for
// `erasetrail_test.go`'s reason: a sentence that is true only because of how
// somebody else composes a document is a sentence that stops being true without
// anything here changing.
func TestNoVerifierReachesTheQueueEither(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)

	s, err := cmd.Build(ctx, cmd.Config{
		Db:    config.DbConfig{Driver: drv, Dsn: dsn},
		Watch: config.WatchConfig{Broker: config.BrokerMemory, Outbox: true},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))

	tenant, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)

	who, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant.GetId()}.Build(),
		Alias:  "somebody",
	}.Build())
	x.NoError(err)

	v := vouch.New(s.Ungated, s.Ungated)

	// The first `Set` adds the row and has no patch document at all. The
	// second is the one that compiles to one, which is the whole subject here.
	for _, secret := range []string{"correct horse battery staple", "a second one entirely"} {
		_, err := v.Set(ctx, app.VouchSetRequest_builder{
			Who:    app.VouchWho_builder{Id: who.GetId()}.Build(),
			Secret: []byte(secret),
		}.Build())
		x.NoError(err)
	}

	vs, err := s.Ent.Outbox.Query().All(ctx)
	x.NoError(err)
	x.NotEmpty(vs, "nothing was queued, so this proves nothing")

	patched := false
	for _, u := range vs {
		if len(u.Patch) > 0 {
			patched = true
		}

		// The encoded form rather than this deployment's parameters, as
		// `trailsecret_test.go` does: what must not be there is an argon2id
		// string, whoever tuned it.
		x.False(bytes.Contains(u.Patch, []byte("$argon2id$")),
			"a verifier reached the queue in %s", u.Method)
	}
	x.True(patched, "no queued write carried a patch, so nothing was redacted")
}
