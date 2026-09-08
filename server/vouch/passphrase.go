package vouch

import (
	"crypto/rand"
	"encoding/base64"
)

// Passphrase is thirty-two bytes somebody can read out over a radio.
//
// Base64 rather than words, which is what `roster init` already prints, and it
// is worth knowing what that costs in the deployment this exists for: an
// operator reading one aloud will mis-hear a character, and there is no
// checksum. A word list would be kinder and is a change to make once, here,
// rather than differently in each place that generates one.
//
// It was two copies -- one for `Vouch.Reset` and one for
// `IssueService.IssuePassword` -- which is what a verb written twice looks like
// before anybody notices it is one verb. Both callers are `Credential.Issue`
// now, and this is exported because the layer that generates a password lives
// in `server/core` while the parameters a secret is made and checked with live
// here.
func Passphrase() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}
