package scim

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestAValueIsReadInOnePassAndANumberAsWritten: a value a directory sends is
// decoded once and walked, and a number is kept as it was written.
//
// `text` marshalled what was inside a list or an object and parsed it again
// at every level, so a value of 8 KiB nested four thousand deep took 3.6 s
// and allocated 1.3 GB, and 4.4 GB nested in objects; and it read a JSON number through a
// float, so an `employeeNumber` of `9007199254740993` was kept as
// `9007199254740992`. Nothing it took is refused now, and nothing it refused
// is taken. What is held of the cost is the bytes allocated, which a run
// measures the same on any machine; no clock is read.
func TestAValueIsReadInOnePassAndANumberAsWritten(t *testing.T) {
	x := assert.New(t)

	for raw, want := range map[string]string{
		`"Erin Kim"`:           "Erin Kim",
		`"  Erin  "`:           "Erin",
		`1001`:                 "1001",
		`9007199254740993`:     "9007199254740993",
		`12345678901234567890`: "12345678901234567890",
		`true`:                 "true",
		`null`:                 "",
		`[]`:                   "",
		`["a", "b"]`:           "a",
		`[{"value": "v"}]`:     "v",
		`{"value": 7}`:         "7",
		`{"value": null}`:      "",
	} {
		got, err := text(json.RawMessage(raw))
		x.NoError(err, raw)
		x.Equal(want, got, raw)
	}
	for _, raw := range []string{`{"other": "x"}`, `{}`, `[{}]`, `not json`} {
		_, err := text(json.RawMessage(raw))
		x.Error(err, raw)
	}

	const depth = 4000
	for _, raw := range []string{
		strings.Repeat("[", depth) + `"x"` + strings.Repeat("]", depth),
		strings.Repeat(`{"value":`, depth) + `"x"` + strings.Repeat("}", depth),
	} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got, err := text(json.RawMessage(raw))
		runtime.ReadMemStats(&after)
		x.NoError(err)
		x.Equal("x", got)
		x.Less(after.TotalAlloc-before.TotalAlloc, uint64(8<<20), "read once, and not again at every level")
	}
}
