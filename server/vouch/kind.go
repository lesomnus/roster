package vouch

import (
	"crypto/rand"
	"slices"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// randRead is `crypto/rand` behind a name, so that a test can be sure what it
// is asserting about.
var randRead = rand.Read

// A kind is checked its own way, and **burns its own way**.
//
// # The finding this exists for
//
// [Burn] costs one argon2 comparison, unconditionally, because until now every
// kind stored an argon2 hash. D14 built that: *an unknown person, a person with
// no credential of this kind and a wrong secret are one response, and the first
// two burn so that they take as long as the third.*
//
// A TOTP comparison is three HMAC-SHA1s and a decrypt -- microseconds. So the
// moment a second kind exists, a `totp` verify against somebody who has none
// costs forty milliseconds and one against somebody who has one costs nothing,
// and the sign of the difference is **inverted** from what D14 built. That is a
// cleaner oracle than the one D14 closed, and it answers the question D21 built
// its whole shape around: does this person have a second factor.
//
// So the cost belongs to the kind rather than to the service. Each verifier
// burns what its own comparison would have cost.

// verifier is how one kind of secret is checked.
type verifier interface {
	// Compare answers whether `secret` is what `stored` was made from.
	//
	// `since` is the last step this credential accepted and `step` is the one
	// this comparison consumed -- zero for a kind that counts nothing, which is
	// every kind but `totp`.
	Compare(stored, secret []byte, since int64) (ok bool, step int64, err error)

	// Burn does the work Compare would have done, for somebody who is not here.
	Burn(secret []byte)
}

// verifierOf answers how a kind is checked, and refuses one this deployment
// cannot check at all.
//
// A refusal here is `Unimplemented` rather than a `no`, and that is deliberate:
// a deployment with no keyring answering "wrong code" to every TOTP attempt is
// a deployment where nobody can tell a misconfiguration from a mistake, and the
// person it happens to is the one who cannot sign in.
func (s *Server) verifierOf(kind string) (verifier, error) {
	switch kindOf(kind) {
	case KindPassword:
		return password{}, nil

	case KindTotp:
		if s.keys.Current == "" {
			return nil, status.Error(codes.Unimplemented,
				"this deployment holds no key to read a seed with, so it cannot check a second factor")
		}

		return Totp{Keys: s.keys}, nil

	case KindWebAuthn:
		// No keyring, and that is the difference worth noticing beside the
		// line above. A TOTP seed has to be read back, so a deployment that
		// cannot wrap one cannot hold one; a credential public key is public
		// and there is nothing to wrap. So this kind works on every
		// deployment, including the one with no `vouch.keys` at all.
		return WebAuthn{}, nil

	default:
		return nil, status.Errorf(codes.InvalidArgument, "kind: %q is not something this checks", kind)
	}
}

// Begins answers whether a kind can be the **first** thing somebody proves.
//
// A password can, and so can a link: each is something a person presents before
// anybody has asked them for anything else. A second factor cannot. It is what
// is asked *after* one, and six digits on their own are six digits.
//
// # The two places that assumed otherwise
//
// Nothing asked this, and both halves of the app quietly answered yes. `Verify`
// takes a kind and checks it, so `Verify(who, "totp", code)` proved a factor and
// [Server.answer] set `ok` the moment there was nothing left to prove -- and for
// somebody whose only credential is a seed, there never was. A person in that
// state signed in with a six-digit code inside a thirty-second window, and the
// call that confirmed a freshly enrolled seed was the call that let them in.
//
// `server/core` reached the same state from the other side. The rule that
// refuses to take away somebody's last way in counted every credential, so a
// person with one provider and one seed could have the provider unlinked: the
// count said one was left, and the one that was left could not let them in. The
// two are the same mistake, which is why they are now the same sentence.
//
// # Why it is not a column
//
// It is a property of the kind and not of the row, for the reason
// [Server.verifierOf] gives about its own question: what a kind *is* is settled
// by what this package can do with it, and a column would be a second answer --
// one that every row written before the question existed gets wrong.
func Begins(kind string) bool {
	switch kindOf(kind) {
	case KindPassword, KindLink:
		return true

	default:
		return false
	}
}

// A note on `webauthn`, which is the one kind where this is a **choice** rather
// than a fact.
//
// A security key tapped as a second factor cannot begin a sign-in, and a
// passkey with user verification can -- it proves possession and that somebody
// unlocked it, which is exactly what a password proves and more. The two are
// the same kind here and are told apart by a flag in the assertion, not by the
// column.
//
// So this answers **no** for both, which is the conservative direction: it can
// only refuse things that would otherwise have worked, never permit one that
// would not. What it costs is that somebody whose only credential is a passkey
// cannot sign in with it, and that `server/core` will not let their last
// provider be unlinked -- both of which are refusals rather than holes.
//
// Making it depend on the assertion is a further decision and wants its own
// reason: `Begins` is asked of a **kind**, before anything has been presented,
// and an answer that depended on what arrived would be a different function in
// a different place.

// begun answers whether anything proved so far could have started this.
func begun(satisfied []string) bool {
	return slices.ContainsFunc(satisfied, Begins)
}

// errAlone is what somebody is told when the only thing they have proved is a
// second factor and there is nothing else left to prove.
//
// It costs nothing to say plainly. Reaching it means the secret was **right**,
// so whoever is told already holds the seed, and what they are told is the true
// thing -- there is nothing here for it to be second to -- rather than that
// their own code is wrong, which is an answer nobody can act on.
var errAlone = status.Error(codes.FailedPrecondition,
	"a second factor is not a way in on its own; this account has nothing for it to be second to")

// settable refuses a kind [Server.Set] must not write down.
//
// # What was wrong with writing whatever arrived
//
// `Set` put the `kind` column in as it was handed over, and it is the only
// entry point here that did: `Verify` and `Continue` refuse a kind
// [Server.verifierOf] does not know, `Reset` refuses anything but a password,
// `Enrol` anything but a second factor.
//
// The row that let through is not inert and cannot be taken back. `factors`
// offers every confirmed credential somebody has, so from then on every framed
// sign-in offers a kind nothing can check; `answer` sets `ok` only when there
// is nothing left to prove, so it never does; and `Continue` refuses the very
// kind it was just offered. Meanwhile `CredentialService` is unregistered and
// closed to the batch, `Reset` refuses the kind and so does `Enrol` -- so no
// call on any plane can delete it. One mistyped kind in an admin console is a
// person who needs a shell on the database to sign in again.
//
// # Why it asks verifierOf rather than holding a list
//
// A second list is a second thing to update, and the one that is forgotten is
// the one that decides what may be stored. What may be written is exactly what
// something here can later check, so that is the question this asks.
//
// `totp` and `webauthn` are the kinds that are known and still not this call's.
// `Set` argon2-hashes what it is handed: a seed has to be read back and a
// credential public key has to be parsed, so either row would be a second
// factor that can never answer, and `Enrol` is the act that makes one.
// `vouch.proto` said so under `Enrol` and nothing enforced it. Refused as
// `InvalidArgument` whether or not this deployment holds a key, because which
// act a caller is doing is not a fact about the deployment.
// It is a package function rather than a method because the caller that hashes
// a secret is `server/core`'s `Credential.Set` now, not this service, and a
// keyring is exactly what the answer does not turn on: the two kinds this
// refuses are refused before anything that would need one, and what is left is
// checked with a plaintext compare. `verifierOf` is the method form's authority
// on which kinds are known; the ones it recognises are `password`, `totp` and
// `webauthn`, and with the last two struck out here `password` is what remains
// -- a new plaintext-checkable kind is a change in both places, and the note is
// here so the second place is not forgotten.
func Settable(kind string) error {
	if k := kindOf(kind); k == KindTotp || k == KindWebAuthn {
		return status.Errorf(codes.InvalidArgument,
			"kind: %q is not something to set; a second factor is Enrol", k)
	}
	if kindOf(kind) != KindPassword {
		return status.Errorf(codes.InvalidArgument, "kind: %q is not something this checks", kind)
	}

	return nil
}

// password is argon2id, and is what every deployment has.
type password struct{}

func (password) Compare(stored, secret []byte, _ int64) (bool, int64, error) {
	ok, err := Compare(stored, secret)

	return ok, 0, err
}

func (password) Burn(secret []byte) { Burn(secret) }

// Totp is a wrapped seed and a code.
//
// Exported, unlike [password], because the interesting properties of this one
// are testable without a database and are the ones worth pinning: a spent step,
// the drift window, and that the comparison costs the same whatever it answers.
type Totp struct{ Keys Keyring }

func (v Totp) Compare(stored, secret []byte, since int64) (bool, int64, error) {
	seed, err := v.Keys.Unwrap(stored)
	if err != nil {
		return false, 0, err
	}

	ok, step := totpVerify(seed, string(secret), since, time.Now())

	return ok, step, nil
}

// Burn is the decrypt and the three HMACs a real comparison would have done.
//
// Against a seed this deployment made rather than a constant, so that the
// unwrap costs what an unwrap costs. What is being matched is the **time**, and
// the time is the same whatever it was made from -- which is the sentence
// [Burn] already uses about its own dummy.
func (v Totp) Burn(secret []byte) {
	stored := v.dummy()
	if stored == nil {
		return
	}

	_, _, _ = v.Compare(stored, secret, 0)
}

// dummy is a wrapped seed for somebody who has no second factor: made on each
// attempt, and kept nowhere.
//
// Made rather than kept because there is nowhere to keep it. `Totp` is a value
// built per call from the keyring -- see [Server.verifierOf] -- so a seed held
// on it would live for one comparison anyway, and a package-level one would be
// wrapped with whichever of a process's two deployments got there first, which
// unwraps under the other as an error rather than as a decrypt.
//
// What it costs is a seed and an AES wrap per refusal, which is microseconds
// against the milliseconds this exists to spend. What it buys is that a person
// with no second factor costs what a person with one costs.
func (v Totp) dummy() []byte {
	seed, err := TotpSeed()
	if err != nil {
		return nil
	}

	stored, err := v.Keys.Wrap(seed)
	if err != nil {
		return nil
	}

	return stored
}
