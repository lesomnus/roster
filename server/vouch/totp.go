package vouch

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// KindTotp is the second factor every authenticator app can hold.
const KindTotp = "totp"

// What RFC 6238 leaves open and what an authenticator app has already decided.
//
// None of these is a preference. HMAC-SHA1, a thirty-second step and six digits
// are what Google Authenticator and everything shaped like it implement, and a
// deployment that chose SHA-256 because it is a better hash would have a second
// factor nobody's phone can produce. They are constants rather than
// configuration for that reason: the range of correct values has one element.
const (
	totpStep   = 30 * time.Second
	totpDigits = 6

	// Drift of exactly one step either way, and no knob.
	//
	// Somebody's phone is a few seconds off and the code they are reading is
	// the previous one; that is what this is for. Widening it multiplies the
	// guess space, which interacts with [MaxFailures] -- and a deployment that
	// widened it to fix "codes keep failing" would be papering over a host
	// whose clock is wrong, which is the actual fault and a silent one.
	totpDrift = 1

	// Twenty bytes, which is RFC 4226's recommendation and the length every
	// app expects. The minimum it permits is sixteen.
	totpSeedLen = 20
)

// totpSeed is a new secret somebody's phone can hold, unwrapped.
func totpSeed() ([]byte, error) {
	b := make([]byte, totpSeedLen)
	if _, err := randRead(b); err != nil {
		return nil, fmt.Errorf("vouch: no seed: %w", err)
	}

	return b, nil
}

// TotpUri is the `otpauth://` a QR code carries.
//
// Base32, uppercase and **unpadded**: padding is legal and some apps refuse it,
// which is the kind of interoperability fact that is only ever discovered by
// somebody standing in front of a phone that will not scan.
func TotpUri(issuer, account string, seed []byte) string {
	q := url.Values{}
	q.Set("secret", base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", strconv.Itoa(totpDigits))
	q.Set("period", strconv.Itoa(int(totpStep/time.Second)))

	// The label is `issuer:account`, which is what apps show in their list.
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + q.Encode()
}

// CodeAt is the code for one step.
//
// Exported so that a test can be about the verifier rather than about the
// clock: a test that waited thirty seconds to see a code expire would be a test
// nobody runs.
func CodeAt(seed []byte, step int64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(step))

	m := hmac.New(sha1.New, seed)
	m.Write(b[:])
	sum := m.Sum(nil)

	// The dynamic truncation RFC 4226 specifies: the low nibble of the last
	// byte picks where to read four bytes from, and the top bit is masked off
	// so the result is positive whatever the platform thinks of signs.
	o := sum[len(sum)-1] & 0x0f
	v := (uint32(sum[o])&0x7f)<<24 | uint32(sum[o+1])<<16 | uint32(sum[o+2])<<8 | uint32(sum[o+3])

	return fmt.Sprintf("%0*d", totpDigits, v%pow10(totpDigits))
}

func pow10(n int) uint32 {
	v := uint32(1)
	for range n {
		v *= 10
	}

	return v
}

// totpVerify answers whether a code is one this seed produces near now, and
// which step it was.
//
// # Every step in the window is tried, always
//
// It does not stop at the first match, and that is not an oversight: returning
// early makes the time taken say **which** step matched, and the near steps are
// where a code somebody is about to use lives. Three HMACs are microseconds and
// the comparison is constant time, so the answer costs the same whatever it is.
//
// # A step that has been spent is not accepted again
//
// D20 puts this in roster's half in as many words -- *a TOTP step that has been
// used must not work twice, and the only place that can be recorded is the
// row.* Without it a code read over somebody's shoulder is good for the rest of
// its thirty seconds, which is most of what an attacker holding one needs.
func totpVerify(seed []byte, code string, since int64, at time.Time) (bool, int64) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false, 0
	}

	now := at.Unix() / int64(totpStep/time.Second)

	ok, step := false, int64(0)
	for d := int64(-totpDrift); d <= totpDrift; d++ {
		s := now + d
		if subtle.ConstantTimeCompare([]byte(CodeAt(seed, s)), []byte(code)) == 1 {
			ok, step = true, s
		}
	}
	if !ok {
		return false, 0
	}
	if step <= since {
		// Spent, or older than one already spent. Refused as an ordinary
		// mismatch: a caller told the difference would learn that the code was
		// real, which is worth something to whoever is holding it.
		return false, 0
	}

	return true, step
}
