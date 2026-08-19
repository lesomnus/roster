package vouch

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// The first secret roster holds that it has to be able to **read back**.
//
// # Why that is a new category and worth a file
//
// Everything else here is a verifier. A password is compared against an argon2
// hash and the store never learns the password; an api key and a delegation are
// found by a digest of themselves. In each case a copy of the database is a
// copy of things nobody can use.
//
// A TOTP seed is not that. Computing the code somebody is about to type means
// holding the seed itself, so the row **is** the secret, and a copy of the
// database is a copy of every second factor in the deployment. `Credential`
// already keeps it off the wire and out of the trail -- `(payday.field).secret`
// plus an unregistered service -- and neither of those helps against a backup
// somebody walked off with.
//
// So it is wrapped, with a key this deployment holds somewhere the database is
// not. That is the whole of what the key buys and it is worth being exact about
// it: it does not protect against a compromised process, which has the key in
// memory by construction. It protects against a copy of the rows.
//
// # Rotation, and what a lost key costs
//
// Ciphertext carries the **name** of the key that made it, so a deployment may
// hold several and roll forward: new rows take the current key and old rows go
// on reading with whichever one made them. Nothing re-wraps in the background,
// deliberately -- a sweep that rewrites every credential row is a sweep whose
// half-finished state is a deployment that cannot verify anybody.
//
// A key that is gone is every TOTP factor in the deployment gone with it. There
// is no recovery and there should not be one: a wrapped seed a deployment can
// unwrap without its key is a seed that was not wrapped. What that costs is an
// operator resetting second factors, which is what D28's surface is for.

// ErrNoKey is a deployment asked to hold a seed with nowhere to put it.
var ErrNoKey = errors.New("vouch: this deployment holds no key to wrap a seed with")

// ErrWrongKey is a stored seed whose key this deployment does not have.
//
// Told apart from [ErrMalformed] because they mean different things to whoever
// is reading the log: one is a row this build cannot parse and the other is a
// key that was rotated away or never configured.
var ErrWrongKey = errors.New("vouch: the key this seed was wrapped with is not configured")

// Keyring is what this deployment can unwrap, and which key it wraps with.
//
// A map rather than one key, because rotation is the only reason to have a name
// on a ciphertext at all -- and a deployment with one key writes one entry and
// never thinks about it again.
type Keyring struct {
	// Current is the name of the key new seeds are wrapped with. Empty is a
	// deployment that does not hold second factors, and asking it to is
	// [ErrNoKey] rather than a silent plaintext.
	Current string

	// By is every key this deployment can read with, by name.
	By map[string][]byte
}

// NewKeyring reads keys as a deployment writes them: `name:base64`, current
// first.
func NewKeyring(vs []string) (Keyring, error) {
	k := Keyring{By: map[string][]byte{}}
	for _, v := range vs {
		name, raw, ok := strings.Cut(v, ":")
		if !ok || name == "" {
			return Keyring{}, fmt.Errorf("vouch: a key is `name:base64`, and %q is not", v)
		}

		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return Keyring{}, fmt.Errorf("vouch: %s: %w", name, err)
		}
		if len(b) != 32 {
			// AES-256 and nothing else. A key of another length is a
			// configuration mistake rather than a choice, and accepting one
			// would make the strength of this a thing nobody wrote down.
			return Keyring{}, fmt.Errorf("vouch: %s: a key is 32 bytes, and this is %d", name, len(b))
		}

		if k.Current == "" {
			k.Current = name
		}
		k.By[name] = b
	}

	return k, nil
}

// Wrap seals a seed with the current key.
//
// The stored form is `name.nonce+ciphertext`, base64 -- the name in front and
// in the clear, because unwrapping has to know which key before it can do
// anything, and a key's *name* is not a secret.
func (k Keyring) Wrap(seed []byte) ([]byte, error) {
	if k.Current == "" {
		return nil, ErrNoKey
	}

	g, err := gcm(k.By[k.Current])
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("vouch: no nonce: %w", err)
	}

	// The name is authenticated as well as carried, so a ciphertext relabelled
	// to point at a weaker key does not open.
	sealed := g.Seal(nonce, nonce, seed, []byte(k.Current))

	return fmt.Appendf(nil, "%s.%s", k.Current, base64.RawStdEncoding.EncodeToString(sealed)), nil
}

// Unwrap opens a stored seed, with whichever key made it.
func (k Keyring) Unwrap(stored []byte) ([]byte, error) {
	name, rest, ok := strings.Cut(string(stored), ".")
	if !ok || name == "" {
		return nil, ErrMalformed
	}

	key, ok := k.By[name]
	if !ok {
		return nil, ErrWrongKey
	}

	sealed, err := base64.RawStdEncoding.DecodeString(rest)
	if err != nil {
		return nil, ErrMalformed
	}

	g, err := gcm(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.NonceSize() {
		return nil, ErrMalformed
	}

	seed, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], []byte(name))
	if err != nil {
		return nil, ErrMalformed
	}

	return seed, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vouch: %w", err)
	}

	return cipher.NewGCM(b)
}
