// Package quote says back in a refusal what a caller sent, to a bound.
//
// A refusal that quotes what it was sent is the most useful one there is --
// *this* is what was wrong -- and it is also a message the log keeps an
// attribute of for every refused call. Quoted whole, a value of a megabyte was
// a megabyte of every answer and of every line of the log, at the caller's
// choosing. The start of it and its length say what was wrong just as well.
//
// One function, and every sentence that quotes a caller's value calls it, in
// every package that refuses one: written twice, the second copy is the one
// that forgets the bound.
package quote

import (
	"fmt"
	"strconv"
	"strings"
)

// Most is how many bytes of a value are said back.
const Most = 80

// Sent is `v` quoted, cut at [Most] bytes where it is longer, and then with
// its length.
func Sent(v string) string {
	if len(v) <= Most {
		return strconv.Quote(v)
	}

	return strconv.Quote(strings.ToValidUTF8(v[:Most], "")) + fmt.Sprintf("... (%d bytes)", len(v))
}
