package core

import (
	"context"
	"encoding/base32"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
	"github.com/lesomnus/roster/server/vouch"
)

// coreCredential is the layer over the generated `CredentialService`.
//
// The service is served now -- see `cmd/serve`'s register/closed -- but its
// generated reads and raw writes stay closed by method, so what reaches the
// wire is these overlays and nothing that answers with a stored verifier. The
// hashing that made `Vouch.Set` a service rather than a `Credential.Add` lives
// here as a layer; this file is the beginning of moving that surface onto the
// entity it was always about. See `CLAUDE.md`, *Overlay before service, layer
// before overlay*.
type coreCredential struct {
	Core
	app.CredentialServiceServer
}

func (s Core) Credential() app.CredentialServiceServer {
	return coreCredential{s, s.Next().Credential()}
}

// Unlock opens an account too many wrong answers closed, without touching the
// secret. It is the operator write `Vouch.Unlock` was, now on the entity it is
// about and named by reference rather than by a sign-in form.
//
// Held to `mayReach`: a lockout you may clear is one you could have caused, so
// you may clear it for nobody whose permissions are not a subset of yours. That
// rule is `server/core`'s own -- the same `mayReach` every credential write
// meets -- so it is a line here rather than a service reaching back for it.
func (s coreCredential) Unlock(ctx context.Context, req *app.CredentialUnlockRequest) (*app.CredentialUnlockResponse, error) {
	kind := req.GetKind()
	if kind == "" {
		kind = vouch.KindPassword
	}

	v, err := s.Next().Credential().Get(ctx, app.CredentialGetRequest_builder{
		Ref: app.CredentialRef_builder{
			Kind: app.CredentialRefByKind_builder{
				Holder: req.GetRef(),
				Kind:   z.Ptr(kind),
			}.Build(),
		}.Build(),
		Select: app.CredentialSelect_builder{
			DateLocked:  z.Ptr(true),
			DateUpdated: z.Ptr(true),
			Holder:      app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayReach(ctx, "ref", holder); err != nil {
		return nil, err
	}

	was := v.GetDateLocked()
	if _, err := s.Next().Credential().Patch(ctx, app.CredentialPatchRequest_builder{
		Ref:            app.CredentialRef_builder{Id: v.GetId()}.Build(),
		Failures:       z.Ptr(int32(0)),
		DateLockedNull: z.Ptr(true),
		DateUpdated:    v.GetDateUpdated(),
	}.Build()); err != nil {
		return nil, err
	}

	res := app.CredentialUnlockResponse_builder{}
	if was != nil {
		res.WasLockedUntil = was
	}

	return res.Build(), nil
}

// Set writes somebody's secret to the one given, hashing it -- the operator
// write `Vouch.Set` was, now on the entity and named by a reference. The
// email/sign-in-form addressing stays with the recovery flow (option 2), so
// this takes a HolderRef and does no address lookup.
//
// A first password and a rotation are one call: absent, it is added; present,
// it is replaced and the lockout cleared. The three rules `Vouch.Set` carried
// travel with it -- the settable-kind check, the leaked-corpus refusal, and
// `mayReach` -- the last two now `server/core`'s own.
//
// **It is your own row or nothing.** Naming somebody else is refused here and
// pointed at `Vouch.Reset`, which is the verb for giving somebody a password
// they did not choose: it generates one, answers with it once, and voids what
// came before. Both used to be this method, held apart by `mayReach` -- and
// `mayReach` is the wrong shape for this one write. It asks whether the target
// is no wider than the caller, which protects an administrator from a junior
// and does nothing for an ordinary person, who is narrower than almost
// everybody. A password is the most persistent thing there is to write on
// somebody's row and it now has one door, which an operator walks through on
// purpose.
//
// Unframed callers are unaffected: `roster init`, `roster vouch set` and the
// Wasm sandbox reach the unwalled server, where there is nobody to refuse and
// the wiring is the control (`mayReach` reads the same way).
//
// `current` is required and verified first, a wrong one is counted like a
// wrong sign-in (`vouch.MaxFailures`, `vouch.LockFor`), and a locked row is not
// compared at all. That is what `ChangeMine` was for, folded back in here so
// that a person and an operator call one method about one row and only the
// layer knows the difference.
//
// # A first password of your own, and what it costs
//
// It is allowed, with no `current`, because there is nothing to hold. That was
// refused until now and the refusal was right on its own terms: a bearer that
// merely acts as somebody -- their session, a delegation -- can then set a
// password they never chose, and unlike the bearer it does not expire and they
// are not told. Temporary access becomes permanent access, quietly.
//
// What changed is the alternative. The refusal pointed at an operator or the
// recovery flow, and **recovery needs mail**, which most deployments never
// configure -- so in the common case it pointed at nothing, and somebody who
// signed in with a provider could not give themselves a password at all. That
// is a real cost against a modest risk: the bearer in question is the account
// app's own session cookie, and lifting one means XSS on that app or the
// person's device, at which point their live provider session is there too.
//
// The one thing that would close it without mail is a **fresh** credential --
// roster minted the delegation and knows when -- and `frame.Frame` does not
// carry which credential a call arrived on, only who it makes the caller. That
// is a payday change, and the note is here so the next reader knows the door
// exists rather than rediscovering the argument.
func (s coreCredential) Set(ctx context.Context, req *app.CredentialSetRequest) (*app.CredentialSetResponse, error) {
	if len(req.GetSecret()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "secret: must not be empty")
	}

	kind := req.GetKind()
	if kind == "" {
		kind = vouch.KindPassword
	}
	// Only a kind something can later check, which is `vouch.Settable`'s whole
	// subject: a second factor is `Enrol`'s and a kind nothing checks is a row
	// no call on any plane can take back. See its comment.
	if err := vouch.Settable(kind); err != nil {
		return nil, err
	}

	// A leaked secret is refused before anything is read or hashed -- a fact
	// about the secret, not about the person, so the refusal cannot depend on
	// whether they exist.
	// Length before the corpus and before the hash: the cheapest refusal
	// first, and the ceiling is what keeps the hash from being made to cost
	// whatever a caller likes.
	if kind == vouch.KindPassword {
		switch n := len(req.GetSecret()); {
		case n < s.password.MinLength:
			return nil, status.Errorf(codes.InvalidArgument, "secret: at least %d characters", s.password.MinLength)
		case n > MaxSecretLength:
			return nil, status.Errorf(codes.InvalidArgument, "secret: at most %d characters", MaxSecretLength)
		}
	}

	if s.breached != nil {
		bad, err := s.breached(ctx, req.GetSecret())
		if err != nil {
			return nil, status.Error(codes.Internal, "whether this secret is known cannot be answered just now")
		}
		if bad {
			return nil, status.Error(codes.FailedPrecondition,
				"this one is in a corpus of leaked passwords; pick another")
		}
	}

	sum, err := vouch.Hash(req.GetSecret())
	if err != nil {
		return nil, status.Error(codes.Internal, "the secret cannot be stored just now")
	}

	// Read the person before writing, so the escalation rule has an id to
	// compare and an Add cannot point its edge at somebody who is gone.
	who, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.HolderSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	holder, err := pdid.From(who.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayReach(ctx, "ref", holder); err != nil {
		return nil, err
	}

	// Whose row this is decides one thing: your own asks for the password you
	// hold, anybody else's must not be asked for it.
	// Whose row this is decides what has to be proved. Your own asks for the
	// password you hold; anybody else's is an operator write and must not be
	// asked for it, and `mayReach` below is what holds that to somebody no
	// wider than the caller.
	//
	// Whether a **caller** may name anybody else at all is not decided here:
	// that is [Self], a layer above this one, which `Vouch.Reset` enters below.
	f, framed := frame.From(ctx)
	own := framed && !f.Actor.IsZero() && f.Actor == holder
	if !own && len(req.GetCurrent()) != 0 {
		return nil, status.Error(codes.InvalidArgument,
			"current: is for your own password; naming somebody else, leave it out")
	}

	ref := app.HolderRef_builder{Id: who.GetId()}.Build()
	byKind := app.CredentialRef_builder{
		Kind: app.CredentialRefByKind_builder{Holder: ref, Kind: z.Ptr(kind)}.Build(),
	}.Build()

	// The stored verifier is read here, in process, and only for the caller's
	// own row -- the wire method that would answer it (`Get`) is closed.
	v, err := s.Next().Credential().Get(ctx, app.CredentialGetRequest_builder{
		Ref: byKind,
		Select: app.CredentialSelect_builder{
			DateUpdated: z.Ptr(true),
			Secret:      z.Ptr(own || s.password.NoReuse > 0),
			Previous:    z.Ptr(s.password.NoReuse > 0),
			Failures:    z.Ptr(own),
			DateLocked:  z.Ptr(own),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}
		// None yet: add it, and for your own row that means with nothing
		// proved. There is nothing to prove -- see the method comment for what
		// is being accepted and why the alternative was worse. `current` is
		// ignored rather than refused: a page that sent one has not done
		// anything wrong, it has guessed about a row it cannot read.
		if _, err := s.Next().Credential().Add(ctx, app.CredentialAddRequest_builder{
			Holder: ref,
			Kind:   kind,
			Secret: sum,
		}.Build()); err != nil {
			return nil, err
		}

		return &app.CredentialSetResponse{}, nil
	}

	if own {
		// Refused here rather than by the comparison below, which would count
		// it as a wrong answer: a page that sends nothing has not guessed, and
		// somebody holding the session could otherwise lock the account out by
		// sending nothing repeatedly. There **is** a password now -- the branch
		// above is the case where there is not.
		if len(req.GetCurrent()) == 0 {
			return nil, status.Error(codes.PermissionDenied,
				"current: your own password is changed by proving the one you hold")
		}
		if err := s.reauth(ctx, v, req.GetCurrent()); err != nil {
			return nil, err
		}
	}

	// Replace it, clearing the lockout -- somebody who set it is not who the
	// lockout was protecting against -- under the version read, so a concurrent
	// write is reported rather than lost.
	patch := app.CredentialPatchRequest_builder{
		Ref:            app.CredentialRef_builder{Id: v.GetId()}.Build(),
		Secret:         sum,
		Failures:       z.Ptr(int32(0)),
		DateLockedNull: z.Ptr(true),
		DateRotated:    timestamppb.Now(),
		DateUpdated:    v.GetDateUpdated(),
	}
	if kind == vouch.KindPassword && s.password.NoReuse > 0 {
		// Against the one held and the ones before it, compared the way a
		// sign-in compares -- which is what makes this cost a hash per
		// remembered password and is the price of the setting. After the
		// re-authentication above, so a wrong current password is counted
		// before anything is said about the new one.
		previous, err := s.unused(v, req.GetSecret())
		if err != nil {
			return nil, err
		}
		patch.Previous = previous
	}

	if _, err := s.Next().Credential().Patch(ctx, patch.Build()); err != nil {
		return nil, err
	}

	return &app.CredentialSetResponse{}, nil
}

// Issue makes a password nobody chose, answers with it once, and ends every
// session the person had.
//
// It is `Vouch.Reset` and `IssueService.IssuePassword`, which were one verb on
// two services -- see `credential_svc.ext.proto` for why each was somewhere
// else and why neither reason survived. What is here that was in neither: the
// write is this layer's own [coreCredential.Set], so the length rule, the
// leaked corpus, `NoReuse`, `date_rotated` and `mayReach` all run.
// `IssuePassword` wrote through the generated verbs and had none of them.
//
// # It is somebody else's row, and that is a rule rather than a habit
//
// Refused for your own, and pointed at `Set`. The two are the halves of one
// column and the line between them is *what did you have to know*: `Set` asks
// for the password you hold, and that is what stops a credential which merely
// **acts as** somebody -- a session, a delegation lifted from an app -- from
// turning temporary access into a password of its own choosing that does not
// expire and that they are never told about. `Issue` asks for nothing, so
// letting it name the caller would be that same door with no lock on it.
//
// `Vouch.Reset` refused it too, but by accident and in the wrong words: it
// wrote through `Set`, so an operator asking for a fresh password of their own
// was answered *current: your own password is changed by proving the one you
// hold* -- about a request that has no `current` in it and never could. The
// refusal was right and unreadable. It is one line here now, and it says which
// verb to use.
//
// # Both writes, or neither
//
// D26 left the invalidation out of `Set` deliberately -- somebody changing
// their own password should not be signed out of everything with nothing having
// said so -- and it belongs to this act, which is the one recovery from a
// takeover goes through. `Vouch.Reset` did it after the fact and best effort,
// because failing the whole call would have left the caller unsure which half
// happened. Inside [Core.only] there is no such half: a stack built on a driver
// runs both in one transaction, and one that was rebound onto somebody else's
// runs inside theirs. A stack with neither -- the admin port, which is built
// with no `On` -- is where the old best-effort shape remains, and it is written
// here rather than discovered.
func (s coreCredential) Issue(ctx context.Context, req *app.CredentialIssueRequest) (*app.CredentialIssueResponse, error) {
	kind := req.GetKind()
	if kind == "" {
		kind = vouch.KindPassword
	}
	if kind != vouch.KindPassword {
		// A second factor is `Enrol`, which generates a seed and answers with it
		// once -- the same shape as this and a different act, because what
		// somebody does with a seed is scan it rather than read it out.
		return nil, status.Errorf(codes.InvalidArgument,
			"kind: %q is not a password; a second factor is Enrol", kind)
	}

	// Whoever this is about, resolved **here** and once, before the passphrase
	// is made -- so a call about nobody costs nothing, and so both writes below
	// are about the same person by construction rather than by agreement.
	// `Vouch.Reset` resolved twice and the second one forgot the address form,
	// which left a reset by email changing the password and no sessions.
	ref, err := s.whosePassword(ctx, req)
	if err != nil {
		return nil, err
	}

	if f, ok := frame.From(ctx); ok && !f.Actor.IsZero() {
		who, err := pdid.From(ref.GetId())
		if err != nil {
			return nil, err
		}
		if who == f.Actor {
			return nil, status.Error(codes.PermissionDenied,
				"a password of your own is Set, which asks for the one you hold; this one asks for nothing")
		}
	}

	secret, err := vouch.Passphrase()
	if err != nil {
		return nil, status.Error(codes.Internal, "a secret cannot be made just now")
	}

	if err := s.only(ctx, ref.GetId(), func(next app.Server) error {
		// This layer again over the transaction's server, so `Set`'s rules run
		// inside it rather than beside it. `Next()` alone would be the bare
		// server, where a credential write has no rules at all.
		in := s.over(next)
		if _, err := (coreCredential{in, next.Credential()}).Set(ctx, app.CredentialSetRequest_builder{
			Ref:    ref,
			Kind:   kind,
			Secret: []byte(secret),
		}.Build()); err != nil {
			return err
		}

		// And everything issued before now is void: a password reset that
		// leaves the old sessions alive is not a reset, and this is where
		// recovery from a takeover happens.
		_, err := in.Holder().Invalidate(ctx, app.HolderInvalidateRequest_builder{Ref: ref}.Build())

		return err
	}); err != nil {
		return nil, err
	}

	return app.CredentialIssueResponse_builder{Secret: secret}.Build(), nil
}

// whosePassword resolves whom a password is issued for -- the three ways
// `Vouch.Reset` and `IssuePassword` took between them, told apart by the plane.
//
// `ApiKey.Issue`'s `whoseKey` is the same function one file over and the same
// reasoning: which plane a caller is on is `WithPrefix`, a fact about the stack
// that answered, so `service` off the control plane and `ref`/`email` on it are
// refused by the wiring rather than by a flag.
func (s coreCredential) whosePassword(ctx context.Context, req *app.CredentialIssueRequest) (*app.HolderRef, error) {
	service, ref, email := req.GetService(), req.GetRef(), req.GetEmail()
	byName, byRef, byMail := service != "", ref != nil, email != nil

	switch {
	case (byName && byRef) || (byName && byMail) || (byRef && byMail):
		return nil, status.Error(codes.InvalidArgument,
			"whose password this is is given more than one way; give one")

	case s.prefix == "":
		// A stack assembled without `WithPrefix` cannot say which plane it is,
		// and the two differ in whether a name matching nobody is a new
		// operator or a typo. It issues nothing rather than guess.
		return nil, status.Error(codes.Unimplemented,
			"this server was not told which plane it answers for")

	case s.prefix == keys.PrefixTenant:
		switch {
		case byName:
			return nil, status.Error(codes.InvalidArgument,
				"service: a bare alias is one person only where there is one tenant, which is the "+
					"control plane; here somebody is a `ref` or an `email` of theirs")

		case byMail:
			// Through the wall like every other read here, so an address in a
			// tenant this caller cannot see is a NotFound rather than a
			// password issued into it.
			v, err := s.Next().Email().Get(ctx, app.EmailGetRequest_builder{
				Ref:    email,
				Select: app.EmailSelect_builder{Holder: app.HolderSelect_builder{}.Build()}.Build(),
			}.Build())
			if err != nil {
				return nil, err
			}

			return app.HolderRef_builder{Id: v.GetHolder().GetId()}.Build(), nil

		case !byRef:
			return nil, status.Error(codes.InvalidArgument, "ref: whose password this is")
		}

		// Read back through the wall, so a reference this caller cannot see is
		// a NotFound. `put` would narrow it too; this is so the refusal names
		// the field.
		w, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref}.Build())
		if err != nil {
			return nil, err
		}

		return app.HolderRef_builder{Id: w.GetId()}.Build(), nil

	case byRef || byMail:
		return nil, status.Error(codes.InvalidArgument,
			"this plane has one tenant, so somebody is a `service` by name")

	case !byName:
		return nil, status.Error(codes.InvalidArgument, "service: whose password this is")
	}

	return s.serviceHolder(ctx, service)
}

// reauth is the proof a caller gives before their own password is replaced:
// the one they hold, compared timing-safe against the stored verifier.
//
// A wrong answer is a wrong sign-in and is counted as one -- the same
// `MaxFailures` and `LockFor` as `Verify`, on the same columns -- so a stolen
// delegation cannot guess its way past this any faster than past the sign-in
// form. `ChangeMine` compared without counting, which was a hole this closes.
// A locked row is refused before it is compared, for the reason the sign-in
// refuses one: the lock is what the comparison's cost was buying.
func (s coreCredential) reauth(ctx context.Context, v *app.Credential, current []byte) error {
	if until := v.GetDateLocked(); until != nil && until.AsTime().After(time.Now()) {
		return status.Errorf(codes.PermissionDenied,
			"locked until %s after too many wrong answers", until.AsTime().Format(time.RFC3339))
	}

	same, err := vouch.Compare(v.GetSecret(), current)
	if err != nil {
		return status.Error(codes.Internal, "the stored password cannot be read")
	}
	if same {
		return nil
	}

	n := v.GetFailures() + 1
	patch := app.CredentialPatchRequest_builder{
		Ref:         app.CredentialRef_builder{Id: v.GetId()}.Build(),
		Failures:    z.Ptr(n),
		DateUpdated: v.GetDateUpdated(),
	}
	if n >= s.lockout.Failures {
		patch.DateLocked = timestamppb.New(time.Now().Add(s.lockout.For))
		patch.Failures = z.Ptr(int32(0))
	}
	// Best effort, like `Verify`'s: a count lost to a concurrent write is a
	// worse thing to fail the call over than to under-count.
	_, _ = s.Next().Credential().Patch(ctx, patch.Build())

	return status.Error(codes.PermissionDenied, "the current password is not the one held")
}

// Enrol makes a second factor and answers with it once -- the write `Vouch.Enrol`
// was, on the entity it writes a row to. A `totp` seed is generated, wrapped
// with the deployment's key and answered once (the row itself is the secret, so
// it cannot go through `Set`, which hashes); a `webauthn` public key is checked
// and kept, with nothing to answer.
//
// The seed is wrapped with `server/core`'s keyring, which is the same one
// `server/vouch` reads a code back with -- handed to the layer by
// `core.WithKeyring` so the crypto stays in `server/vouch` and only the
// orchestration is here. A deployment that holds no key refuses a `totp`, since
// a seed it cannot wrap is one nothing can ever read back.
//
// Held to `mayReach` like every credential write: adding a way in for somebody
// is one of the two things they sign in with, so you may enrol a factor for
// nobody whose permissions are not a subset of yours. The row goes in
// unconfirmed (`Verify` moves its step), which is `server/vouch`'s to enforce.
// enrol is `Enrol`'s work: a factor made for `ref`, answered as a seed and URI
// (both empty for webauthn). One path whoever `ref` is -- `mayReach` passes for
// a caller writing their own and refuses one wider than the caller -- which is
// why there is no self-only twin of it; see the service comment in
// `credential_svc.ext.proto`.
func (s coreCredential) enrol(ctx context.Context, ref *app.HolderRef, kind, name, issuer string, attestation []byte) (string, string, error) {
	switch kind {
	case vouch.KindTotp:
		if len(attestation) > 0 {
			return "", "", pderr.Invalidf("attestation",
				"a seed is made here; a request carrying one has not decided which ceremony it is doing")
		}
		if s.keyring.Current == "" {
			return "", "", status.Error(codes.Unimplemented,
				"this deployment holds no key to wrap a seed with, so it cannot hold a second factor")
		}

	case vouch.KindWebAuthn:
		if len(attestation) == 0 {
			return "", "", pderr.Invalidf("attestation",
				"an authenticator makes this one; roster is handed the public half")
		}

	default:
		// A password is `Set` or `Reset`, and neither is a thing a phone or a
		// key holds. Refused rather than routed: a caller asking for one here
		// has misunderstood which act they are doing.
		return "", "", status.Errorf(codes.InvalidArgument,
			"kind: %q is not something to enrol; a password is Set or Reset", kind)
	}

	// Read the person before writing, for the alias the URI carries and the id
	// the escalation rule compares -- and through `Next()`, so a caller who
	// cannot see them cannot enrol a way into their account either.
	who, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    ref,
		Select: app.HolderSelect_builder{Alias: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return "", "", err
	}

	holder, err := pdid.From(who.GetId())
	if err != nil {
		return "", "", err
	}
	if err := s.mayReach(ctx, "ref", holder); err != nil {
		return "", "", err
	}

	on := app.HolderRef_builder{Id: who.GetId()}.Build()

	if kind == vouch.KindWebAuthn {
		// Checked before it is written: an attestation nobody verified is a row
		// that answers to whoever sent it.
		v, err := vouch.Register(attestation)
		if err != nil {
			return "", "", pderr.Invalidf("attestation", "%s", err)
		}

		if _, err := s.Next().Credential().Add(ctx, app.CredentialAddRequest_builder{
			Holder: on,
			Kind:   vouch.KindWebAuthn,
			Name:   name,
			Secret: v.Stored,

			// The counter the authenticator reported, so the first assertion has
			// something to exceed. Registering **is** the proof here, so unlike a
			// seed there is no unconfirmed state to be in.
			LastStep: v.Count,
		}.Build()); err != nil {
			return "", "", err
		}

		// The private half never left the authenticator, so there is nothing to
		// answer with.
		return "", "", nil
	}

	seed, err := vouch.TotpSeed()
	if err != nil {
		return "", "", status.Error(codes.Internal, "a seed cannot be made just now")
	}

	stored, err := s.keyring.Wrap(seed)
	if err != nil {
		return "", "", status.Error(codes.Internal, "a seed cannot be stored just now")
	}

	if _, err := s.Next().Credential().Add(ctx, app.CredentialAddRequest_builder{
		Holder: on,
		Kind:   vouch.KindTotp,
		Name:   name,
		Secret: stored,
	}.Build()); err != nil {
		return "", "", err
	}

	if issuer == "" {
		issuer = "roster"
	}

	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed),
		vouch.TotpUri(issuer, who.GetAlias(), seed), nil
}

// Enrol makes a second factor and answers with it once -- the write `Vouch.Enrol`
// was, on the entity it writes a row to, named by a reference. See [coreCredential.enrol].
func (s coreCredential) Enrol(ctx context.Context, req *app.CredentialEnrolRequest) (*app.CredentialEnrolResponse, error) {
	seed, uri, err := s.enrol(ctx, req.GetRef(), req.GetKind(), req.GetName(), req.GetIssuer(), req.GetAttestation())
	if err != nil {
		return nil, err
	}

	return app.CredentialEnrolResponse_builder{Seed: seed, Uri: uri}.Build(), nil
}

// Erase takes a credential away -- a second factor somebody no longer has, from
// the person's own account screen or an operator's.
//
// Closed on the wire until now, along with the rest of the raw verbs, and the
// only one of them with nothing wrong with it: it answers with no verifier and
// takes none. What it needs is the layer every credential write has --
// `mayReach` on the row's holder, self passing -- and two rules of its own. A
// **password** is never removed, only replaced (`Set`), because a removed
// password with no other way in strands somebody, and one with another way in
// is a downgrade nobody asked for by that name. And a kind that **begins** a
// sign-in (`vouch.Begins`) meets D42's rule like an identity does: not the last
// way in, counted and written under one lock.
func (s coreCredential) Erase(ctx context.Context, req *app.CredentialRef) (*app.CredentialEraseResponse, error) {
	v, err := s.CredentialServiceServer.Get(ctx, app.CredentialGetRequest_builder{
		Ref: req,
		Select: app.CredentialSelect_builder{
			Kind:   z.Ptr(true),
			Holder: app.HolderSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Erasing what is not there succeeds, which is `Erase`'s rule.
			return app.CredentialEraseResponse_builder{}.Build(), nil
		}

		return nil, err
	}
	if v.GetKind() == vouch.KindPassword {
		return nil, status.Error(codes.FailedPrecondition,
			"a password is replaced, not removed: Set a new one")
	}
	holder, err := pdid.From(v.GetHolder().GetId())
	if err != nil {
		return nil, err
	}
	if err := s.mayReach(ctx, "ref", holder); err != nil {
		return nil, err
	}

	var out *app.CredentialEraseResponse
	err = s.only(ctx, v.GetHolder().GetId(), func(next app.Server) error {
		if vouch.Begins(v.GetKind()) {
			if err := notTheirLastCredential(ctx, next, v.GetHolder().GetId(), v.GetId()); err != nil {
				return err
			}
		}

		w, err := next.Credential().Erase(ctx, req)
		if err != nil {
			return err
		}
		out = w

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// notTheirLastCredential is D42's count for a credential that begins a
// sign-in: an identity, or another credential that begins one, has to remain.
func notTheirLastCredential(ctx context.Context, next app.Server, holder, erasing []byte) error {
	ref := app.HolderRef_builder{Id: holder}.Build()

	ids, err := next.Identity().List(ctx, app.IdentityListRequest_builder{
		Filters: []*app.IdentityFilter{app.IdentityFilter_builder{Holder: ref}.Build()},
	}.Build())
	if err != nil {
		return err
	}
	if len(ids.GetItems()) > 0 {
		return nil
	}

	creds, err := next.Credential().List(ctx, app.CredentialListRequest_builder{
		Filters: []*app.CredentialFilter{app.CredentialFilter_builder{Holder: ref}.Build()},
	}.Build())
	if err != nil {
		return err
	}
	for _, c := range creds.GetItems() {
		if !bytesEq(c.GetId(), erasing) && vouch.Begins(c.GetKind()) {
			return nil
		}
	}

	return status.Error(codes.FailedPrecondition,
		"this is the only way they can sign in; give them another before taking it away")
}

// unused refuses a new password that is the one held or one of the last
// `NoReuse` before it, and answers with what `previous` becomes: the one held
// in front, the rest behind it, cut to the number kept.
func (s coreCredential) unused(v *app.Credential, secret []byte) ([][]byte, error) {
	was := append([][]byte{v.GetSecret()}, v.GetPrevious()...)
	for _, sum := range was {
		if len(sum) == 0 {
			continue
		}
		same, err := vouch.Compare(sum, secret)
		if err != nil {
			return nil, status.Error(codes.Internal, "a stored password cannot be read")
		}
		if same {
			return nil, status.Errorf(codes.FailedPrecondition,
				"this one was used before; the last %d are refused", s.password.NoReuse)
		}
	}
	if len(was) > s.password.NoReuse {
		was = was[:s.password.NoReuse]
	}

	return was, nil
}
