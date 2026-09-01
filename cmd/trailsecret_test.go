package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/roster/rstr"
)

// TestNoVerifierReachesTheTrailInEitherColumn.
//
// `TestNoVerifierReachesTheTrail` says this already and checked half of it.
// `Audit` carries two records of a write -- `value`, the row as the event left
// it, and `patch`, the document the write was compiled from -- and only the
// first was ever asked about.
//
// The declaration is `(payday.field).secret`, and what it buys in the trail is
// a generated `hide<E>` that clears the column out of the row before it is
// marshalled into `value`. Nothing does the same to `patch`, which is built
// from the request and carries whatever the request set.
//
// So `Vouch.Set` -- the one Rpc whose whole job is to take a secret in and
// never give one back -- left the argon2id string in `Audit.patch`, in full,
// in the one table nothing erases. And `AuditService` is **served**: the wall
// files a credential's row under the person's tenant, so anybody in that
// tenant whose role names `/roster.AuditService/*` reads the password hash of
// everybody in it.
//
// Which is the thing `CredentialService` is unregistered to prevent, reached
// by the other road. D13 shut the door on the read; this was the window.
func TestNoVerifierReachesTheTrailInEitherColumn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Twice, because the first is an Add and the second a Patch, and a patch
	// document is what each compiles to.
	for _, secret := range []string{"correct horse battery staple", "another one entirely"} {
		_, err := b.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
			Ref:    app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(),
			Secret: []byte(secret),
		}.Build())
		x.NoError(err)
	}

	cred, err := b.Ent.Credential.Query().Only(ctx)
	x.NoError(err)
	x.NotEmpty(cred.Secret, "nothing was stored, so this proves nothing")

	vs, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)

	patches := 0
	for _, a := range vs {
		if len(a.Patch) > 0 {
			patches++
		}

		x.NotContains(string(a.Value), string(cred.Secret),
			"%s: the trail's value holds a password hash", a.Action)
		x.NotContains(string(a.Patch), string(cred.Secret),
			"%s: the trail's patch holds a password hash", a.Action)

		// And the shape of one, not only this deployment's parameters: a
		// column cleared by name would still let the encoded form through if
		// something else copied it.
		x.NotContains(string(a.Patch), "$argon2id$",
			"%s: the trail's patch holds a password hash", a.Action)
	}
	x.NotZero(patches, "no row carried a patch, so the checks above never looked at one")
}
