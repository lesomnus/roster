// Package vouchtest is an authenticator, for a test that needs one.
//
// # Why it is a package and not a test file
//
// Because two packages need it and neither can import the other's tests. The
// arithmetic of WebAuthn is checked where the verifier is, in `server/vouch`,
// and the ceremony end to end is checked where a database is, in `cmd` -- and
// the thing both need is something that holds a private key and signs a
// challenge with it.
//
// # And why it is written rather than recorded
//
// A captured registration and assertion would pin one recording and nothing
// else. What these tests are about is a **relationship** -- this key signed
// this challenge, and the counter moved -- and only a thing that can sign on
// demand can be asked to sign the wrong challenge, or the right one twice.
package vouchtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/require"
)

const (
	// RelyingParty is the domain a credential is registered under, and Origin
	// is where the browser was. A test that used one string for both would pass
	// while the verifier compared the wrong pair.
	RelyingParty = "contoso.example"
	Origin       = "https://contoso.example"
)

// Authenticator is a WebAuthn authenticator, as much of one as a test needs.
type Authenticator struct {
	key   *ecdsa.PrivateKey
	id    []byte
	count uint32
}

func New(t *testing.T) *Authenticator {
	t.Helper()

	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	id := make([]byte, 16)
	_, err = rand.Read(id)
	require.NoError(t, err)

	return &Authenticator{key: k, id: id}
}

// cose is the public key as the attestation carries it: COSE_Key, EC2, P-256.
func (a *Authenticator) cose(t *testing.T) []byte {
	t.Helper()

	x := a.key.PublicKey.X.FillBytes(make([]byte, 32))
	y := a.key.PublicKey.Y.FillBytes(make([]byte, 32))

	b, err := cbor.Marshal(map[int]any{
		1:  2,  // kty: EC2
		3:  -7, // alg: ES256
		-1: 1,  // crv: P-256
		-2: x,
		-3: y,
	})
	require.NoError(t, err)

	return b
}

// authData is the authenticator data: the relying party hash, the flags, the
// counter, and — for a registration — the credential itself.
func (a *Authenticator) authData(t *testing.T, attested bool) []byte {
	t.Helper()

	sum := sha256.Sum256([]byte(RelyingParty))

	// UP, and AT when a credential is attached. UV is deliberately off: a key
	// tapped without a PIN is a legitimate second factor and `Register` says so.
	flags := byte(0x01)
	if attested {
		flags |= 0x40
	}

	out := append([]byte{}, sum[:]...)
	out = append(out, flags)
	out = binary.BigEndian.AppendUint32(out, a.count)

	if attested {
		out = append(out, make([]byte, 16)...) // AAGUID
		out = binary.BigEndian.AppendUint16(out, uint16(len(a.id)))
		out = append(out, a.id...)
		out = append(out, a.cose(t)...)
	}

	return out
}

func clientData(t *testing.T, kind, challenge string) []byte {
	t.Helper()

	b, err := json.Marshal(map[string]any{
		"type":      kind,
		"challenge": challenge,
		"origin":    Origin,
	})
	require.NoError(t, err)

	return b
}

// register is what `navigator.credentials.create()` answers with, wrapped in
// the envelope roster takes.
func (a *Authenticator) Register(t *testing.T, challenge string) []byte {
	t.Helper()

	client := clientData(t, "webauthn.create", challenge)

	att, err := cbor.Marshal(map[string]any{
		// `none`, which is what a browser sends unless an app asked for more --
		// and asking for more is a question about which authenticators a
		// deployment trusts, which `Register` says is a policy.
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": a.authData(t, true),
	})
	require.NoError(t, err)

	return envelope(t, challenge, map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.id),
		"rawId": base64.RawURLEncoding.EncodeToString(a.id),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(client),
			"attestationObject": base64.RawURLEncoding.EncodeToString(att),
		},
	})
}

// assert is what `navigator.credentials.get()` answers with, at the counter the
// authenticator is now at.
func (a *Authenticator) Assert(t *testing.T, challenge string) []byte {
	t.Helper()

	a.count++

	return a.AssertAt(t, challenge, a.count)
}

// assertAt is the same, at a counter named rather than advanced -- for the key
// that reports zero forever.
func (a *Authenticator) AssertAt(t *testing.T, challenge string, at uint32) []byte {
	t.Helper()

	a.count = at

	return a.AssertFor(t, challenge, challenge)
}

// assertFor signs one challenge and says another in the envelope, which is what
// an assertion collected somewhere else looks like when it is offered here.
func (a *Authenticator) AssertFor(t *testing.T, signed, expected string) []byte {
	t.Helper()

	client := clientData(t, "webauthn.get", signed)
	data := a.authData(t, false)

	// What a WebAuthn signature is over: the authenticator data, then the hash
	// of the client data. Which is why saying a different challenge in the
	// envelope cannot be papered over -- the challenge is inside the bytes that
	// were signed.
	sum := sha256.Sum256(client)
	over := append(append([]byte{}, data...), sum[:]...)
	digest := sha256.Sum256(over)

	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	require.NoError(t, err)

	return envelope(t, expected, map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.id),
		"rawId": base64.RawURLEncoding.EncodeToString(a.id),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(client),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(data),
			"signature":         base64.RawURLEncoding.EncodeToString(sig),
		},
	})
}

func envelope(t *testing.T, challenge string, res map[string]any) []byte {
	t.Helper()

	b, err := json.Marshal(map[string]any{
		"rp_id":     RelyingParty,
		"origins":   []string{Origin},
		"challenge": challenge,
		"response":  res,
	})
	require.NoError(t, err)

	return b
}

func Challenge(t *testing.T) string {
	t.Helper()

	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)

	return base64.RawURLEncoding.EncodeToString(b)
}
