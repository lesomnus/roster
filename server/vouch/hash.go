package vouch

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Params is what one hash costs.
//
// The defaults are OWASP's first choice for argon2id -- 19 MiB, two passes,
// one lane. The memory is the part that matters: it is what makes a graphics
// card no better at this than a server, and lowering it to make sign-in feel
// quicker is the one change here that quietly undoes the rest.
type Params struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
	KeyLen  uint32
	SaltLen uint32
}

var Default = Params{
	Time:    2,
	Memory:  19 * 1024,
	Threads: 1,
	KeyLen:  32,
	SaltLen: 16,
}

// KindPassword is the kind every deployment has, and what an empty `kind`
// means.
const KindPassword = "password"

var (
	// ErrMalformed is a stored hash this cannot read.
	//
	// It is an error rather than "no", because "no" would mean somebody with
	// the right password is refused and nothing says why.
	ErrMalformed = errors.New("vouch: the stored verifier cannot be read")

	b64 = base64.RawStdEncoding
)

// Hash makes the verifier stored for a secret, with [Default].
func Hash(secret []byte) ([]byte, error) { return Default.Hash(secret) }

// Hash makes the verifier stored for a secret.
//
// What comes back is the PHC string -- `$argon2id$v=19$m=…,t=…,p=…$salt$hash`
// -- which carries the parameters it was made with. That is why [Default] can
// change without invalidating a single stored row: [Compare] reads the cost
// from the hash in front of it rather than from whatever this process was
// built with, so an old row keeps verifying at its old cost and a rotation is
// a thing that can happen gradually.
func (p Params) Hash(secret []byte) ([]byte, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("vouch: no salt: %w", err)
	}

	sum := argon2.IDKey(secret, salt, p.Time, p.Memory, uint8(p.Threads), p.KeyLen)

	return fmt.Appendf(nil, "$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(sum)), nil
}

// Compare answers whether `secret` is what `encoded` was made from.
//
// The comparison is constant time. A byte-by-byte one leaks how much of a hash
// was right through how long it took to say no, which over enough attempts is
// the hash.
func Compare(encoded, secret []byte) (bool, error) {
	p, salt, want, err := parse(encoded)
	if err != nil {
		return false, err
	}

	got := argon2.IDKey(secret, salt, p.Time, p.Memory, p.Threads, uint32(len(want)))

	return subtle.ConstantTimeCompare(want, got) == 1, nil
}

// Burn does the work a real comparison would have done, for somebody who is not
// here.
//
// Without it, "no such person" answers in microseconds and "wrong password"
// answers in however long argon2 takes, and the difference is a way to ask this
// service whether an account exists. Every refusal costs the same because every
// refusal does the same work.
//
// The dummy is made once, on the first attempt against somebody unknown, rather
// than at startup -- a deployment where that never happens should not pay for
// it.
func Burn(secret []byte) {
	if v := dummy(); v != nil {
		_, _ = Compare(v, secret)
	}
}

var dummy = sync.OnceValue(func() []byte {
	// The value is nobody's secret and does not need to be one: what is being
	// matched is the **time**, and the time is the same whatever it was made
	// from.
	v, err := Hash([]byte("this is not anybody's password"))
	if err != nil {
		return nil
	}

	return v
})

func parse(encoded []byte) (Params, []byte, []byte, error) {
	fs := strings.Split(string(encoded), "$")
	if len(fs) != 6 || fs[0] != "" || fs[1] != "argon2id" {
		return Params{}, nil, nil, ErrMalformed
	}

	var v int
	if _, err := fmt.Sscanf(fs[2], "v=%d", &v); err != nil || v != argon2.Version {
		return Params{}, nil, nil, ErrMalformed
	}

	p := Params{}
	if _, err := fmt.Sscanf(fs[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return Params{}, nil, nil, ErrMalformed
	}

	salt, err := b64.DecodeString(fs[4])
	if err != nil {
		return Params{}, nil, nil, ErrMalformed
	}
	sum, err := b64.DecodeString(fs[5])
	if err != nil || len(sum) == 0 {
		return Params{}, nil, nil, ErrMalformed
	}

	return p, salt, sum, nil
}
