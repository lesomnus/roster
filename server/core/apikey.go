package core

import (
	"context"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
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
// name another app's Rpcs, which roster has no descriptors for and should not
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

	// And **whose** key it is, which the methods do not say.
	//
	// A key resolves to its holder, so calls made with it are made as them --
	// which is a credential for that person, written by whoever minted it. The
	// methods check alone left the helpdesk able to mint a key on the
	// administrator's holder carrying only `Vouch.Set`, a method they hold: the
	// key acts as the administrator, `mayReach` sees a caller writing their own
	// credential, and the next call sets a password they chose on the account
	// they could not reach. See [Core.mayWriteAWayIn].
	if err := s.mayWriteAWayIn(ctx, "holder", req.GetHolder()); err != nil {
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

// Issue makes a key for somebody and answers with it once -- the write
// `Issue.IssueKey` was, on the entity. It makes the secret with `crypto/rand`
// and the plane's prefix, stores the hash through [coreApiKey.Add] so the two
// escalation rules run (nobody hands out a method they do not hold; nobody
// writes a way into an account wider than their own), and answers the token the
// one time it is readable.
//
// The prefix is `WithPrefix`'s, one per stack, so a caller cannot ask the
// customer port for a key of the deployment's own kind. See `server/keys`.
func (s coreApiKey) Issue(ctx context.Context, req *app.ApiKeyIssueRequest) (*app.ApiKeyIssueResponse, error) {
	if req.GetAlias() == "" {
		return nil, status.Error(codes.InvalidArgument, "a name for the key")
	}
	if len(req.GetMethods()) == 0 {
		// Refused rather than defaulted in either direction. Everything hands
		// out more than was asked for; nothing mints a key that silently does
		// not work.
		return nil, status.Error(codes.InvalidArgument, "methods: a key that allows nothing opens no door")
	}
	if s.prefix == "" {
		// A stack assembled without `WithPrefix` cannot say which plane a key
		// belongs to, so it mints none rather than an unprefixed one.
		return nil, status.Error(codes.Unimplemented,
			"this server was not told which kind of key it mints")
	}

	ref, err := s.whoseKey(ctx, req)
	if err != nil {
		return nil, err
	}

	// Which kind is a fact about which server answered, and not a field. See
	// [Core.prefix].
	token, sum, err := keys.Mint(s.prefix)
	if err != nil {
		return nil, err
	}

	add := app.ApiKeyAddRequest_builder{
		Holder:  ref,
		Alias:   req.GetAlias(),
		Secret:  sum,
		Methods: req.GetMethods(),
	}
	if v := req.GetExpires(); v != nil {
		add.DateExpires = v
	}

	// Through this layer's own `Add`, so the escalation rules run: `roster key
	// add` goes around them through `Ungated`, where there is no frame at all,
	// which is the deployment doing its own work rather than anybody asking.
	v, err := s.Add(ctx, add.Build())
	if err != nil {
		return nil, err
	}

	// The hash never leaves, even here. `Add` answered through `Next()`, which
	// is below the layer that clears `secret` on the way out -- and that layer
	// clears a top-level `ApiKey` answer, not one nested in this response -- so
	// this clears it, the way the token is the one thing that is readable and
	// exactly once. See `Credential.secret`.
	v.SetSecret(nil)

	return app.ApiKeyIssueResponse_builder{Token: token, Key: v}.Build(), nil
}

// whoseKey resolves whom a minted key is for, the same pair `Issue.IssueKey`
// took and told apart by the plane: a `holder` reference on the data plane
// (`rt_`), a `service` alias created if absent on the control plane (`rk_`).
func (s coreApiKey) whoseKey(ctx context.Context, req *app.ApiKeyIssueRequest) (*app.HolderRef, error) {
	service, ref := req.GetService(), req.GetHolder()
	byName := service != ""
	byRef := ref != nil

	switch {
	case byName && byRef:
		return nil, status.Error(codes.InvalidArgument,
			"a service and a holder name whose key this is two ways; give one")

	case s.prefix == keys.PrefixTenant:
		if !byRef {
			return nil, status.Error(codes.InvalidArgument,
				"holder: whose key this is; `service` is the other plane's, where there is one tenant")
		}

		// Read back through the wall, so a reference this caller cannot see is a
		// NotFound rather than a key minted into a tenant they have no business
		// in. `Add` would narrow it too; this is so the refusal says which field.
		v, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref}.Build())
		if err != nil {
			return nil, err
		}

		return app.HolderRef_builder{Id: v.GetId()}.Build(), nil

	case byRef:
		return nil, status.Error(codes.InvalidArgument,
			"holder: this plane has one tenant, so a key is for a `service` by name")

	case !byName:
		return nil, status.Error(codes.InvalidArgument, "service: whose key this is")
	}

	return s.serviceHolder(ctx, service)
}

// serviceHolder is a control-plane service by alias, made if it is not there --
// because a service is not something set up on purpose before it is needed,
// which is what `roster key add` already decided.
func (s coreApiKey) serviceHolder(ctx context.Context, alias string) (*app.HolderRef, error) {
	ts, err := s.Next().Tenant().List(ctx, app.TenantListRequest_builder{Size: 1}.Build())
	if err != nil {
		return nil, err
	}
	if len(ts.GetItems()) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "this deployment has no owner")
	}
	tenant := ts.GetItems()[0].GetId()

	byAlias := app.HolderRef_builder{
		Slug: app.HolderRefBySlug_builder{
			Alias:  z.Ptr(alias),
			Tenant: app.TenantRef_builder{Id: tenant}.Build(),
		}.Build(),
	}.Build()

	v, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{Ref: byAlias}.Build())
	if err == nil {
		return app.HolderRef_builder{Id: v.GetId()}.Build(), nil
	}
	if status.Code(err) != codes.NotFound {
		return nil, err
	}

	made, err := s.Next().Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant}.Build(),
		Alias:  alias,
	}.Build())
	if err != nil {
		return nil, err
	}

	return app.HolderRef_builder{Id: made.GetId()}.Build(), nil
}

// Get, List and Erase are served on every port now, not only the one that
// manages the deployment's own keys. What closed them on the others was the
// verifier in `secret`, and the answer to that is the layer roster has for
// every secret -- the sink strips it on the way out -- so a person lists their
// own keys and ends one by reference, and `Holder.RevokeKey`, which existed
// because there was no served road to a key row, is gone: a second name for
// the same rows (CLAUDE.md, *Overlay before service*). The rule that stays is
// the one every credential read and write meets: `mayReach` on the row's
// holder, self passing, nobody wider than the caller.
func (s coreApiKey) Get(ctx context.Context, req *app.ApiKeyGetRequest) (*app.ApiKey, error) {
	if err := s.reaches(ctx, req.GetRef()); err != nil {
		return nil, err
	}

	return s.ApiKeyServiceServer.Get(ctx, req)
}

func (s coreApiKey) List(ctx context.Context, req *app.ApiKeyListRequest) (*app.ApiKeyListResponse, error) {
	for _, f := range req.GetFilters() {
		if f.GetHolder() == nil {
			continue
		}
		holder, err := s.holderOf(ctx, f.GetHolder())
		if err != nil {
			return nil, err
		}
		if err := s.mayReach(ctx, "holder", holder); err != nil {
			return nil, err
		}
	}

	return s.ApiKeyServiceServer.List(ctx, req)
}

func (s coreApiKey) Erase(ctx context.Context, req *app.ApiKeyRef) (*app.ApiKeyEraseResponse, error) {
	if err := s.reaches(ctx, req); err != nil {
		if status.Code(err) == codes.NotFound {
			return app.ApiKeyEraseResponse_builder{}.Build(), nil
		}

		return nil, err
	}

	return s.ApiKeyServiceServer.Erase(ctx, req)
}

// reaches is `mayReach` on the holder of the key a reference names, read
// through the wall first so a key outside the caller's tenant is `NotFound`
// before anybody is compared.
func (s coreApiKey) reaches(ctx context.Context, ref *app.ApiKeyRef) error {
	v, err := s.ApiKeyServiceServer.Get(ctx, app.ApiKeyGetRequest_builder{
		Ref:    ref,
		Select: app.ApiKeySelect_builder{Holder: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return err
	}
	if len(v.GetHolder().GetId()) == 0 {
		// The deployment's own key hangs off a service, and a service is not
		// somebody whose reach is compared: the control port's own rules stand.
		return nil
	}
	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return err
	}

	return s.mayReach(ctx, "ref", holder)
}
