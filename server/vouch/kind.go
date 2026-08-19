package vouch

import (
	"crypto/rand"
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

	default:
		return nil, status.Errorf(codes.InvalidArgument, "kind: %q is not something this checks", kind)
	}
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

// dummy is one wrapped seed, made on the first attempt against somebody who has
// no second factor rather than at startup.
//
// Per-server rather than a package `sync.OnceValue`, because it is wrapped with
// **this** deployment's key: one shared across two servers in a process would
// be wrapped with whichever keyring got there first, and unwrapping it under
// the other would cost an error rather than a decrypt.
func (v Totp) dummy() []byte {
	seed, err := totpSeed()
	if err != nil {
		return nil
	}

	stored, err := v.Keys.Wrap(seed)
	if err != nil {
		return nil
	}

	return stored
}
