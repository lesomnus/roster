package core

import (
	"context"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// A key is a grant, so writing one is held to the rule every other grant is.
//
// # What was missing
//
// `mayGrant` was wired to `Role.Add` and `Binding.Add` and to nothing else, and
// `ApiKey.methods` is the third place a method list is written down. It is also
// the most direct: a role has to be bound to somebody before it does anything,
// and a key **is** the credential -- whoever holds the string can call whatever
// the column says.
//
// Nothing exploited it, and the reason is not a check. It is that minting a key
// needed a shell: `roster key add` writes through `Ungated`, where there is no
// frame and `mayGrant` is a no-op by design. So the hole was invisible for
// exactly as long as there was no console, and a console is the thing that
// removes the shell.
//
// # Why it is not in the schema
//
// The same reason the others are not. `methods` is a list of strings and each
// one is valid on its own; what is refused is a **combination** -- this list,
// written by this caller -- and that is a judgement about the request rather
// than a constraint on a column.
//
// # What it does not check
//
// That the methods exist. They are opaque strings here on purpose: a key may
// name another app's RPCs, which roster has no descriptors for and should not
// try to acquire -- see `payday.TokenService`. What makes that safe is that a
// grant only ever takes away, so a method named on a key that its holder cannot
// call is still refused where the call lands.

type coreApiKey struct {
	Core
	app.ApiKeyServiceServer
}

func (s Core) ApiKey() app.ApiKeyServiceServer { return coreApiKey{s, s.Next().ApiKey()} }

func (s coreApiKey) Add(ctx context.Context, req *app.ApiKeyAddRequest) (*app.ApiKey, error) {
	// `pdid.Nil`: a key names no site, so whoever holds it is narrowed by
	// whatever narrows its holder and by nothing else. Writing one is therefore
	// a tenant-wide grant, and somebody who holds a method only in a site may
	// not put it on a key.
	if err := s.mayGrant(ctx, "methods", req.GetMethods(), pdid.Nil); err != nil {
		return nil, err
	}

	return s.ApiKeyServiceServer.Add(ctx, req)
}

// Patch is held to the same rule, and it has to be: a key whose methods can be
// widened after it was written is a key whose first version says nothing about
// what it may do.
//
// The whole list is checked rather than what changed. Working out the
// difference means reading the row, and a caller who may not grant a method
// they are leaving in place is a caller who should not be writing this row at
// all.
func (s coreApiKey) Patch(ctx context.Context, req *app.ApiKeyPatchRequest) (*app.ApiKey, error) {
	if err := s.mayGrant(ctx, "methods", req.GetMethods(), pdid.Nil); err != nil {
		return nil, err
	}

	return s.ApiKeyServiceServer.Patch(ctx, req)
}
