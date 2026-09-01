package vouch

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

// KindWebAuthn is a security key or a passkey.
//
// # Why roster verifies it, when a public key is not a secret
//
// D14's rule -- a verifier must not travel, because a comparison done elsewhere
// puts the attempt counter and the lockout in two places that will disagree --
// does not apply here. A credential public key is public; handing it out costs
// nothing.
//
// What keeps verification here is the **signature counter**, and D20 says so:
// *it is state that has to move forward exactly once per assertion, and state
// belongs to whoever holds the row.* An app that verified assertions itself
// would have to keep that counter, which is a row, which is this.
//
// # What roster does not know, and takes as arguments
//
// The relying-party id, the origin and the challenge. Those are the
// browser-facing half: an app chose the challenge, served the page, and knows
// which origin the browser was at. D20 named all three.
//
// They arrive **inside the presented bytes** rather than as fields on
// `VouchVerifyRequest`, and that is deliberate. The request is generic across
// kinds; three fields that mean something for one kind and nothing for the
// others would be three fields every other kind has to explain. What a caller
// presents is already whatever the kind says it is -- a password for
// `password`, six digits for `totp` -- and for this it is the assertion plus
// the three facts that make it checkable.
const KindWebAuthn = "webauthn"

// stored is what the row holds for one authenticator.
//
// Not a secret, and it is in the `secret` column because that is where a
// credential's verifier lives and the column is declared `secret:` -- so it is
// kept out of the trail and off every answer. Neither matters for a public key
// and neither is wrong: what the column means is *what this row is checked
// against*, and this is that.
type stored struct {
	// Id is the credential identifier the authenticator chose, which is what a
	// browser is told to look for.
	Id []byte `json:"id"`

	// Key is the COSE-encoded public key, as the attestation carried it.
	Key []byte `json:"key"`
}

// presented is what a caller sends, for either ceremony.
//
// One shape for both, because both need the same three facts about the browser
// and differ only in which ceremony's response rides along.
type presented struct {
	// RelyingParty is the `rp.id` the app registered under -- a domain, not an
	// origin.
	RelyingParty string `json:"rp_id"`

	// Origin is where the browser actually was. More than one is allowed for a
	// deployment served under several names.
	Origins []string `json:"origins"`

	// Challenge is what the app sent and is expecting back, base64url as the
	// browser encodes it.
	Challenge string `json:"challenge"`

	// Response is the raw Json the browser's credential Api answered with.
	Response json.RawMessage `json:"response"`
}

func (p presented) valid() error {
	switch {
	case p.RelyingParty == "":
		return errors.New("rp_id: which relying party this was for")
	case len(p.Origins) == 0:
		return errors.New("origins: where the browser was")
	case p.Challenge == "":
		return errors.New("challenge: what was sent and is expected back")
	case len(p.Response) == 0:
		return errors.New("response: what the browser answered with")
	}

	return nil
}

func presentedIn(b []byte) (presented, error) {
	var v presented
	if err := json.Unmarshal(b, &v); err != nil {
		return v, fmt.Errorf("not a webauthn envelope: %w", err)
	}

	return v, v.valid()
}

// Algorithms is what a credential may be signed with.
//
// Named rather than left open, and it has to be: the verifier refuses a key
// whose algorithm is not on a list, so an empty list refuses everything. What
// is on it is what browsers and authenticators actually produce.
//
// **ES256 is the one that matters** -- every platform authenticator and every
// security key does it, and a deployment that offered only this would work
// everywhere. RS256 is here for the older Windows Hello stack, which produces
// it and nothing else on some machines. EdDSA is here because a handful of keys
// prefer it and refusing it would refuse them for no reason.
//
// What is **not** here is anything with SHA-1, and the omission is the
// statement: `AlgRS1` exists in the library and is in no deployment's interest.
//
// An app that wants a narrower set has to say so at the browser, in the
// `pubKeyCredParams` it asks for -- which is the browser-facing half, and is
// the app's for the reason D20 gives about the rest of it.
var Algorithms = []protocol.CredentialParameter{
	{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256},
	{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgRS256},
	{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgEdDSA},
}

// Registered is what an attestation leaves behind, and what a row is written
// from.
//
// Exported because `Enrol` is the caller and it lives one file over; the shape
// is not something an app names.
type Registered struct {
	// Stored is the row's verifier column.
	Stored []byte

	// Count is the sign counter as the authenticator reported it, which is the
	// number every later assertion has to exceed.
	Count int64
}

// Register checks an attestation and answers with what to keep.
//
// # What it verifies, and what it does not
//
// The client data against the challenge and the origin, the relying-party hash,
// that the user was present, and the attestation statement itself. What it does
// **not** do is check the attestation against a metadata service: that is a
// question about which authenticators a deployment trusts, which is a policy
// and is the app's -- the same line D20 draws about whether a second factor is
// required at all.
//
// # And user verification is not demanded
//
// A security key tapped without a PIN is a legitimate second factor, and
// demanding user verification here would refuse exactly that. Whether this
// deployment wants a passkey rather than a key is the flow's question, and the
// flow is where the browser is.
func Register(b []byte) (Registered, error) {
	v, err := presentedIn(b)
	if err != nil {
		return Registered{}, err
	}

	res, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(v.Response))
	if err != nil {
		return Registered{}, fmt.Errorf("attestation: %w", err)
	}

	if _, err := res.Verify(
		v.Challenge,
		v.RelyingParty,
		v.Origins,
		nil,
		// The strictest of the three, and it decides nothing here: a
		// cross-origin ceremony is refused a line below, so there is no top
		// origin to check. Named rather than left zero because the zero value
		// is an error in the verifier by design.
		protocol.TopOriginExplicitVerificationMode,
		false, // a cross-origin registration is not a thing this offers
		false, // user verification: see the note above
		true,  // user presence: somebody has to have touched it
		nil,   // no metadata service; which authenticators are trusted is a policy
		Algorithms,
	); err != nil {
		return Registered{}, fmt.Errorf("attestation: %w", err)
	}

	data := res.Response.AttestationObject.AuthData
	if len(data.AttData.CredentialID) == 0 || len(data.AttData.CredentialPublicKey) == 0 {
		return Registered{}, errors.New("attestation: carried no credential")
	}

	out, err := json.Marshal(stored{
		Id:  data.AttData.CredentialID,
		Key: data.AttData.CredentialPublicKey,
	})
	if err != nil {
		return Registered{}, err
	}

	return Registered{Stored: out, Count: int64(data.Counter)}, nil
}

// WebAuthn is a stored public key and an assertion.
type WebAuthn struct{}

// Compare verifies an assertion and answers with the counter it consumed.
//
// # The counter is the whole reason this is roster's
//
// An authenticator that keeps one increments it on every assertion, so a
// counter that did not move forward is a signature replayed or a device cloned.
// `since` is what the row holds and the answer is what to write back, which is
// the same shape `totp` uses for a spent step -- and it is the shape D20 argued
// verification belongs here for.
//
// **Zero on both sides is allowed**, and has to be: an authenticator is
// permitted to report no counter at all, and every assertion from one reads
// zero. Refusing that would refuse a large share of the keys people own. What
// is refused is a counter that has moved **backwards or not at all** on a
// device that reports one.
func (v WebAuthn) Compare(row, b []byte, since int64) (bool, int64, error) {
	var s stored
	if err := json.Unmarshal(row, &s); err != nil {
		return false, 0, fmt.Errorf("the stored credential cannot be read: %w", err)
	}

	p, err := presentedIn(b)
	if err != nil {
		return false, 0, nil
	}

	res, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(p.Response))
	if err != nil {
		return false, 0, nil
	}

	// The assertion has to be from **this** row's authenticator, which is the
	// one check the library takes on trust: it verifies a signature against
	// whatever key it is handed.
	if !bytes.Equal(res.RawID, s.Id) {
		return false, 0, nil
	}

	if err := res.Verify(
		p.Challenge,
		p.RelyingParty,
		"", // no appId: the U2F compatibility path is not offered
		p.Origins,
		nil,
		protocol.TopOriginExplicitVerificationMode,
		false,
		false,
		true,
		s.Key,
	); err != nil {
		return false, 0, nil
	}

	at := int64(res.Response.AuthenticatorData.Counter)
	if at != 0 || since != 0 {
		if at <= since {
			// Replayed, or a clone. Answered as an ordinary no, because a
			// caller told apart learns that a real assertion was made once.
			return false, 0, nil
		}
	}

	return true, at, nil
}

// Burn does the work Compare would have done, for somebody who is not here.
//
// `server/vouch/kind.go` says why each kind burns its own way: an argon2 burn
// against a signature check inverts the difference rather than closing it. A
// verification is one ECDSA operation, so this is one ECDSA operation.
//
// Against a key made here rather than a constant, for the reason `Totp.dummy`
// gives about its own: what is being matched is arithmetic, and the arithmetic
// costs the same whatever it is over.
func (v WebAuthn) Burn(b []byte) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return
	}

	sum := sha256.Sum256(b)
	_ = ecdsa.VerifyASN1(&k.PublicKey, sum[:], b)
}

// CredentialID is what a browser is told to look for, read off a stored row.
//
// For the half of the ceremony roster does not run: an app asking for an
// assertion has to name which credentials it will accept, and that list is a
// fact about the person which is what this app is.
func CredentialID(row []byte) (string, error) {
	var s stored
	if err := json.Unmarshal(row, &s); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(s.Id), nil
}
