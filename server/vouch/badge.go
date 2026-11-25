package vouch

import "sync"

// A PIN's verifier is the password's hash of it, sealed.
//
// # Why both
//
// A PIN is four digits, or a few more: ten thousand guesses, which a copy of
// the rows runs through in a second whatever argon2 costs. So the hash alone
// keeps nothing, and the row is sealed under the deployment's keyring the way
// a TOTP seed is (`seed.go`): a copy of the database is a copy of nothing
// anybody can open without the key. The hash under the seal is what keeps the
// PIN itself from anybody who has the key and a row -- a process's memory, an
// operator reading a backup with the key beside it.
//
// It is not a [verifier] of this package's `Verify`, on purpose. A badge is
// not a credential: nothing that signs anybody in reads one, and the one call
// that checks a PIN is `BadgeService/Verify` in `server/core`.

// SealPin is the verifier stored for a PIN: its hash, sealed with the
// current key. [ErrNoKey] where the keyring holds none.
func SealPin(keys Keyring, pin []byte) ([]byte, error) {
	if keys.Current == "" {
		return nil, ErrNoKey
	}

	sum, err := Hash(pin)
	if err != nil {
		return nil, err
	}

	return keys.Wrap(sum)
}

// ComparePin answers whether `pin` is what `stored` was sealed from, with
// whichever key sealed it -- so a keyring that rotated still opens what an
// older key sealed, as it does a seed.
func ComparePin(keys Keyring, stored, pin []byte) (bool, error) {
	sum, err := keys.Unwrap(stored)
	if err != nil {
		return false, err
	}

	return Compare(sum, pin)
}

// BurnPin does the work [ComparePin] would have done, for a badge that is
// nobody's -- an unseal and a hash -- so that a code nobody has costs what a
// wrong PIN costs.
//
// Against a verifier this keyring sealed, made once for each key it is
// current with: what is matched is the time, and the time is the same
// whatever was sealed.
func BurnPin(keys Keyring, pin []byte) {
	if keys.Current == "" {
		Burn(pin)
		return
	}

	if v := dummyPin(keys); v != nil {
		_, _ = ComparePin(keys, v, pin)
		return
	}

	Burn(pin)
}

// dummyPins is a sealed dummy for each current key, by its name and its
// bytes: a process may hold two deployments, and a dummy sealed under one
// opens under the other as an error rather than as an unseal.
var dummyPins sync.Map

func dummyPin(keys Keyring) []byte {
	at := keys.Current + "\x00" + string(keys.By[keys.Current])
	if v, ok := dummyPins.Load(at); ok {
		return v.([]byte)
	}

	v, err := SealPin(keys, []byte("this is not anybody's PIN"))
	if err != nil {
		return nil
	}

	got, _ := dummyPins.LoadOrStore(at, v)

	return got.([]byte)
}
