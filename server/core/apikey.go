package core

import (
	"context"
	"slices"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
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
	// The app it is for ([coreApiKey.forApp]), whose methods it names.
	of, err := s.forApp(ctx, req.GetHolder(), req.GetApp())
	if err != nil {
		return nil, err
	}

	// `pdid.Nil`: a key names no site, so whoever holds it is narrowed by
	// whatever narrows its holder and by nothing else. Writing one is therefore
	// a tenant-wide grant, and somebody who holds a method only in a site may
	// not put it on a key. And at the key's app: what it names is spent there,
	// and only what the minter holds there covers it.
	if err := s.mayGrant(ctx, "methods", req.GetMethods(), pdid.Nil, of, pdid.Nil); err != nil {
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
//
// At the key's own app, which a patch cannot change (`ApiKey.app` is
// immutable): read off the row where the patch names methods.
func (s coreApiKey) Patch(ctx context.Context, req *app.ApiKeyPatchRequest) (*app.ApiKey, error) {
	of := pdid.Nil
	if len(req.GetMethods()) > 0 {
		v, err := s.ApiKeyServiceServer.Get(ctx, app.ApiKeyGetRequest_builder{
			Ref:    req.GetRef(),
			Select: app.ApiKeySelect_builder{App: app.HolderSelect_builder{}.Build()}.Build(),
		}.Build())
		if err != nil {
			return nil, err
		}
		if of, err = idOr(v.GetApp().GetId()); err != nil {
			return nil, err
		}
	}
	if err := s.mayGrant(ctx, "methods", req.GetMethods(), pdid.Nil, of, pdid.Nil); err != nil {
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
	if v := req.GetDateExpired(); v != nil && !v.AsTime().After(time.Now()) {
		// As a delegation is refused one (`keys.Delegate`): a caller that
		// computed an expiry and got the sign wrong would otherwise be
		// answered a token, once, that had already stopped working.
		return nil, status.Error(codes.InvalidArgument, "date_expired: a key cannot have already expired")
	}
	if s.prefix == "" {
		// A stack assembled without `WithPrefix` cannot say which plane a key
		// belongs to, so it mints none rather than an unprefixed one.
		return nil, status.Error(codes.Unimplemented,
			"this server was not told which kind of key it mints")
	}

	// The control plane's keys are the deployment's own, for no app; refused
	// here before a holder is named and made.
	if req.GetApp() != nil && s.prefix == keys.PrefixDeployment {
		return nil, noAppHere()
	}

	// What the key may call, asked before whose it is, at the app it is for.
	// On the control plane naming a holder makes them ([Core.holderNamed]),
	// and `Add` below asks this again where it has always asked it -- after:
	// a key that was refused left behind the holder it had named, on a plane
	// where a holder is a caller (#274). So nobody is made for a key that will
	// not be.
	of := pdid.Nil
	if req.GetApp() != nil {
		var err error
		if of, err = s.forApp(ctx, req.GetHolder(), req.GetApp()); err != nil {
			return nil, err
		}
	}
	if err := s.mayGrant(ctx, "methods", req.GetMethods(), pdid.Nil, of, pdid.Nil); err != nil {
		return nil, err
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
	if v := req.GetDateExpired(); v != nil {
		add.DateExpired = v
	}
	if of != pdid.Nil {
		add.App = app.HolderRef_builder{Id: of.Bytes()}.Build()
	}

	// A key somebody issues themselves carries the sign-in they issued it
	// with, inherited rather than started again; one issued for somebody else
	// carries none, because that person proved nothing. Which is what keeps a
	// key from laundering a sign-in into a fresh one: minting is not proving
	// (`ApiKey.date_authenticated`).
	at, err := s.authenticatedFor(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !at.IsZero() {
		add.DateAuthenticated = timestamppb.New(at)
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

// forApp is the app a key is for (`ApiKey.app`), [pdid.Nil] for roster's
// own, and refuses one it may not be: on the control plane, whose keys are
// the deployment's and for no app; and one that is not somebody of the
// tenant whose key it is, live -- and that is all that is asked of it.
//
// # Read past the wall
//
// Minting a key is the person's own, and a person reads no holder but
// themselves: read as they read one, the app is nobody to them, and nobody
// could mint a key for an app. So it is read from the rows ([Resident]), of
// the tenant of whose key it is and of no other, which a caller who may not
// see that tenant's people learns nothing more by than that the name is
// somebody's there. A stack that cannot say, and the box, read it as the
// caller reads one.
func (s coreApiKey) forApp(ctx context.Context, holder, ref *app.HolderRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}
	if s.prefix == keys.PrefixDeployment {
		return pdid.Nil, noAppHere()
	}

	who, err := s.tenantOfHolder(ctx, holder)
	if err != nil {
		return pdid.Nil, err
	}
	if _, framed := frame.From(ctx); framed && s.rules.Resident != nil {
		return s.rules.Resident(ctx, who, ref)
	}

	whose, err := s.tenantOfHolder(ctx, ref)
	if err != nil {
		return pdid.Nil, err
	}
	if err := tenantsAgree("app", who, whose); err != nil {
		return pdid.Nil, err
	}

	return s.appOf(ctx, ref)
}

// noAppHere is the refusal of a key for an app on the control plane.
func noAppHere() error {
	return pderr.Invalidf("app",
		"a key of this plane is the deployment's own, presented to roster, and is for no app; a key for an app is a tenant's")
}

// authenticatedFor is the sign-in a key for `ref` inherits: the caller's own,
// when the key is the caller's, and none otherwise.
func (s coreApiKey) authenticatedFor(ctx context.Context, ref *app.HolderRef) (time.Time, error) {
	f, ok := frame.From(ctx)
	if !ok || f.Actor.IsZero() {
		return time.Time{}, nil
	}
	whose, err := s.holderOf(ctx, ref)
	if err != nil || whose != f.Actor {
		return time.Time{}, err
	}

	return keys.Authenticated(ctx, s.Next(), f.Actor)
}

// whoseKey resolves whom a minted key is for, the same pair `Issue.IssueKey`
// took and told apart by the plane: a `holder` reference on the data plane
// (`rt_`), a `holder_alias` created if absent on the control plane (`rk_`).
func (s coreApiKey) whoseKey(ctx context.Context, req *app.ApiKeyIssueRequest) (*app.HolderRef, error) {
	// One of the two or neither: they are a `oneof`, so a request cannot
	// carry both.
	alias, ref := req.GetHolderAlias(), req.GetHolder()
	byName := alias != ""
	byRef := ref != nil

	switch {
	case s.prefix == keys.PrefixTenant:
		if !byRef {
			return nil, status.Error(codes.InvalidArgument,
				"holder: whose key this is; `holder_alias` is the other plane's, where there is one tenant")
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
			"holder: this plane has one tenant, so a key is for a `holder_alias`")

	case !byName:
		return nil, status.Error(codes.InvalidArgument, "holder_alias: whose key this is")
	}

	return s.holderNamed(ctx, alias)
}

// holderNamed is a control-plane holder by alias, made if it is not there --
// because a caller of this deployment's own is not a row set up on purpose
// before it is needed, which is what `roster control key add` already decided.
//
// On `Core` rather than on this file's layer, because `Credential.Issue` names
// a new holder the same way and for the same reason: a console that has just
// typed somebody's name has thereby created them.
func (s Core) holderNamed(ctx context.Context, alias string) (*app.HolderRef, error) {
	ts, err := s.Next().Tenant().List(ctx, app.TenantListRequest_builder{Size: 1}.Build())
	if err != nil {
		return nil, err
	}
	if len(ts.GetItems()) == 0 {
		return nil, status.Error(codes.Unimplemented, "this deployment has no owner")
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
// the same rows (CLAUDE.md, *Overlay before service*). A write of one -- ending
// it -- meets the rule every write of a way in meets: `mayReach` on the row's
// holder, self passing, nobody wider than the caller. A read meets
// [Core.mayReadOf].
//
// And a key found by its verifier is read by the app it is for, as a
// delegation is read by the app it was issued to: that is `Introspect`
// finding the key an app was presented, which answers it to that app alone
// (`server/keys`). No port serves a read by the verifier (`cmd`,
// `NotByVerifier`), so this is that read and no other.
//
// # And the keys that are for an app are that app's rows
//
// `Get`, `List` and `Erase` are among roster's verbs held for one app
// ([ForAnApp]), and a key is an app's row when its `app` is that app. So
// whoever holds the verb for Foo -- Foo itself, bound `app-administrator` for
// itself by `roster app install`, and Foo's administrators -- reads the keys
// for Foo of anybody, and ends one whoever's it is: what it takes away is a
// way in at Foo and nowhere else. A key of roster's, or for another app, is
// not theirs by it: not seen (`cmd.Apps`), and not ended ([Core.mayWriteOfApp]).
// Minting is not among them, and is the person's.
func (s coreApiKey) Get(ctx context.Context, req *app.ApiKeyGetRequest) (*app.ApiKey, error) {
	whose, of, err := s.whoseFor(ctx, req.GetRef())
	if err != nil {
		return nil, err
	}
	if !s.spentBy(ctx, req.GetRef(), of) {
		if err := s.mayReadKey(ctx, "ref", app.ApiKeyService_Get_FullMethodName, whose, of); err != nil {
			return nil, err
		}
	}

	return s.ApiKeyServiceServer.Get(ctx, req)
}

// heldForOnly is whether the caller holds `method` for the app `of` itself
// (`Binding.app`). Not across the tenant: a key's read held across it answers
// somebody their own, and its erase held across it is held to the reach rule,
// as they always were.
func (s coreApiKey) heldForOnly(ctx context.Context, method string, of pdid.Id) (bool, error) {
	apps, _, _, err := s.heldFor(ctx, method)

	return slices.Contains(apps, of), err
}

// mayReadKey is [Core.mayReadOf] of a key's holder, or the caller holding the
// read `method` for the app the key is for.
func (s coreApiKey) mayReadKey(ctx context.Context, field, method string, whose, of pdid.Id) error {
	err := s.mayReadOf(ctx, field, whose)
	if err == nil || of == pdid.Nil || status.Code(err) != codes.PermissionDenied {
		return err
	}
	if held, herr := s.heldForOnly(ctx, method, of); herr != nil || held {
		return herr
	}

	return err
}

// spentBy is whether a key found by its verifier is read by the app it is
// for ([coreApiKey.Get]).
func (s coreApiKey) spentBy(ctx context.Context, ref *app.ApiKeyRef, of pdid.Id) bool {
	f, ok := frame.From(ctx)

	return ok && len(ref.GetSecret()) > 0 && of != pdid.Nil && !f.Actor.IsZero() && f.Actor == of
}

func (s coreApiKey) List(ctx context.Context, req *app.ApiKeyListRequest) (*app.ApiKeyListResponse, error) {
	mine, err := s.onlyMine(ctx)
	if err != nil {
		return nil, err
	}
	if mine != nil {
		// Or the keys for an app they hold the read for, of anybody: the wall
		// answers those and their own (`cmd.Apps`), and a filter naming
		// somebody else is the keys of theirs for that app.
		apps, _, _, err := s.heldFor(ctx, app.ApiKeyService_List_FullMethodName)
		if err != nil {
			return nil, err
		}
		if len(apps) > 0 {
			mine = nil
		}
	}
	if mine != nil {
		// Their own, whatever was asked for: a filter that names nobody is
		// given them, and one that names somebody else is refused.
		req = proto.CloneOf(req)
		fs := req.GetFilters()
		if len(fs) == 0 {
			fs = []*app.ApiKeyFilter{{}}
		}
		for _, f := range fs {
			if f.GetHolder() != nil {
				holder, err := s.holderOf(ctx, f.GetHolder())
				if err != nil {
					return nil, err
				}
				if err := s.mayReadOf(ctx, "holder", holder); err != nil {
					return nil, err
				}
			}
			f.SetHolder(mine)
		}
		req.SetFilters(fs)
	}

	res, err := s.ApiKeyServiceServer.List(ctx, req)
	if err != nil {
		return nil, err
	}

	// Without the verifier, on every stack. The served ones strip it on the way
	// out already; the unwalled one does not, because `keys.lookup` reads it
	// there -- with `Get`, by the hash. Nothing finds a key by listing, so a
	// list has no use for it, and `roster api-key ls -o json` on the box was
	// printing every key's.
	for _, v := range res.GetItems() {
		v.SetSecret(nil)
	}

	return res, nil
}

// Erase is a way in taken away, and meets [coreApiKey.reaches]; or it is a
// key for an app ended by whoever holds `Erase` for that app, whoever's it
// is ([coreApiKey.Get]).
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
//
// Somebody's own is theirs to end. One for an app the caller holds `Erase`
// for is theirs to end too, and nothing else is asked of it. Anything else,
// to somebody who holds `Erase` for an app and not across the tenant, is not
// that app's, and is refused naming it ([Core.mayWriteOfApp]).
func (s coreApiKey) reaches(ctx context.Context, ref *app.ApiKeyRef) error {
	holder, of, err := s.whoseFor(ctx, ref)
	if err != nil || holder == pdid.Nil {
		return err
	}
	if f, ok := frame.From(ctx); ok && f.Actor == holder {
		return s.mayReach(ctx, "ref", holder)
	}
	if of != pdid.Nil {
		held, err := s.heldForOnly(ctx, app.ApiKeyService_Erase_FullMethodName, of)
		if err != nil || held {
			return err
		}
	}
	if err := s.mayWriteOfApp(ctx, "ref", app.ApiKeyService_Erase_FullMethodName, "key", of, pdid.Nil); err != nil {
		// Or what they hold in a site reaches it: a key is in no site, and
		// such a grant reaches it by its holder, read as that grant alone
		// reads it ([Core.byASiteAlone]). Reaching it by neither is the
		// refusal above.
		if status.Code(err) != codes.PermissionDenied {
			return err
		}
		_, sites, _, herr := s.heldFor(ctx, app.ApiKeyService_Erase_FullMethodName)
		if herr != nil {
			return herr
		}
		if len(sites) == 0 || s.byASiteAlone(ctx, func(ctx context.Context) error { _, _, err := s.whoseFor(ctx, ref); return err }) != nil {
			return err
		}
	}

	return s.mayReach(ctx, "ref", holder)
}

// whose is the holder a key hangs off, and nobody for the deployment's own:
// that hangs off a holder of the control plane, who is not somebody whose
// standing is compared or asked -- the control port's own rules stand.
func (s coreApiKey) whose(ctx context.Context, ref *app.ApiKeyRef) (pdid.Id, error) {
	whose, _, err := s.whoseFor(ctx, ref)

	return whose, err
}

// whoseFor is [coreApiKey.whose], and the app the key is for (`ApiKey.app`),
// [pdid.Nil] for roster's own.
func (s coreApiKey) whoseFor(ctx context.Context, ref *app.ApiKeyRef) (pdid.Id, pdid.Id, error) {
	v, err := s.ApiKeyServiceServer.Get(ctx, app.ApiKeyGetRequest_builder{
		Ref: ref,
		Select: app.ApiKeySelect_builder{
			Holder: app.HolderSelect_builder{}.Build(),
			App:    app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, pdid.Nil, err
	}
	of, err := idOr(v.GetApp().GetId())
	if err != nil || len(v.GetHolder().GetId()) == 0 {
		return pdid.Nil, of, err
	}
	whose, err := pdid.From(v.GetHolder().GetId())

	return whose, of, err
}
