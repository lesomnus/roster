package cmd_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	app "github.com/lesomnus/roster/rstr"
)

// TestNoVerifierReachesTheQueueEither.
//
// `trailsecret_test.go` asks this of `Audit`, in both of its columns, and the
// answer came from a redactor payday's trail recorder runs. There are **three**
// recorders behind every write and they read the same `bare.Change`; the
// finding named one and the fix reached one.
//
// So this is the same question one table over. A row in `outbox` held the
// patch document at rest until something drained it, redacted by payday's
// recorder. It holds none now: roster's recorder hands payday's the change
// without its patch, which row was written and by what, and nothing else
// (`TestTheQueueKeepsWhichRowAndNotWhatWasWritten`). So no verifier reaches
// the queue because nothing of a write does, and this holds that every row is
// so, whatever payday redacts.
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
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))

	tenant, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "contoso"}.Build())
	x.NoError(err)

	who, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant.GetId()}.Build(),
		Alias:  "somebody",
	}.Build())
	x.NoError(err)

	// The first `Set` adds the row and has no patch document at all. The
	// second is the one that compiles to one, which is the whole subject here.
	for _, secret := range []string{"correct horse battery staple", "a second one entirely"} {
		_, err := s.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: who.GetId()}.Build(),
			Secret: []byte(secret),
		}.Build())
		x.NoError(err)
	}

	vs, err := s.Ent.Outbox.Query().All(ctx)
	x.NoError(err)
	x.NotEmpty(vs, "nothing was queued, so this proves nothing")

	for _, u := range vs {
		// The encoded form rather than this deployment's parameters, as
		// `trailsecret_test.go` does: what must not be there is an argon2id
		// string, whoever tuned it.
		x.False(bytes.Contains(u.Patch, []byte("$argon2id$")),
			"a verifier reached the queue in %s", u.Method)
		x.Empty(u.Patch, "a write's document reached the queue in %s", u.Method)
	}
}
