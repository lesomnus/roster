package account_test

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTheTerminalsBeginningIsWhatTheManualSays keeps `docs/usage/ways-in.md`
// § *For an app* honest the mechanical way, as `login/contract_test.go` keeps
// the Login App's routes.
//
// # Why this table
//
// `POST /device/begin` is a request a third party writes against since a
// terminal's sign-in names the app: an app's page beginning one for the
// person in front of it, `loft auth login`. So its fields and what each
// refusal is are the contract, and both directions cost somebody: a field or
// a refusal the table names that the front door does not is a client written
// against nothing, and one the front door has that the table does not is a
// shape nobody promised to keep.
//
// # Why it reads source
//
// For the reason the Login App's test does: the question is which fields the
// request is read into and which refusals the function can answer, which is a
// fact about two functions -- `deviceBegin` and `offered` -- and the refusals
// themselves are driven by `device_test.go`.
func TestTheTerminalsBeginningIsWhatTheManualSays(t *testing.T) {
	x := require.New(t)

	doc, err := os.ReadFile(filepath.Join("..", "docs", "usage", "ways-in.md"))
	x.NoError(err)
	section := sectionOf(string(doc), "#### For an app")
	x.NotEmpty(section, "docs/usage/ways-in.md no longer has the section this test is about")

	fields, refusals := map[string]bool{}, map[string]bool{}
	cell := regexp.MustCompile("^`([^`]+)`$")
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| ---") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		switch {
		case cells[0] == "field" || cells[0] == "status":
		case len(cells) == 2:
			m := cell.FindStringSubmatch(cells[0])
			x.NotNil(m, "a field that is not written as one: %q", line)
			fields[m[1]] = true
		case len(cells) == 3:
			status, words := cell.FindStringSubmatch(cells[0]), cell.FindStringSubmatch(cells[1])
			x.NotNil(status, "a status that is not written as one: %q", line)
			x.NotNil(words, "words that are not written as them: %q", line)
			refusals[status[1]+" "+words[1]] = true
		}
	}
	x.NotEmpty(fields, "the section has no table of fields")
	x.NotEmpty(refusals, "the section has no table of refusals")

	src, err := os.ReadFile("device.go")
	x.NoError(err)
	begin := funcOf(string(src), "func (a *App) deviceBegin(")
	offered := funcOf(string(src), "func (a *App) offered(")
	x.NotEmpty(begin, "account/device.go has no deviceBegin")
	x.NotEmpty(offered, "account/device.go has no offered")

	in := regexp.MustCompile("(?s)var in struct \\{(.*?)\\n\\t\\}").FindStringSubmatch(begin)
	x.NotNil(in, "deviceBegin reads its body into no `in`")
	read := map[string]bool{}
	for _, m := range regexp.MustCompile("`json:\"([^\",]+)").FindAllStringSubmatch(in[1], -1) {
		read[m[1]] = true
	}
	x.Equal(fields, read, "the fields the manual names are not the ones the request is read into")

	statuses := map[string]int{
		"StatusBadRequest":          http.StatusBadRequest,
		"StatusNotFound":            http.StatusNotFound,
		"StatusInternalServerError": http.StatusInternalServerError,
		"StatusNotImplemented":      http.StatusNotImplemented,
		"StatusServiceUnavailable":  http.StatusServiceUnavailable,
	}
	answered := map[string]bool{}
	for _, m := range regexp.MustCompile(`http\.Error\(w, "([^"]+)", http\.(Status\w+)\)`).FindAllStringSubmatch(begin+offered, -1) {
		code, ok := statuses[m[2]]
		x.True(ok, "a refusal answered %s, which this test does not know the number of", m[2])
		answered[strconv.Itoa(code)+" "+m[1]] = true
	}
	x.Equal(refusals, answered, "the refusals the manual names are not the ones the beginning answers")
}

// sectionOf is a document from a heading to the next heading of any depth.
func sectionOf(doc, heading string) string {
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		return ""
	}
	rest := doc[i+len(heading)+2:]
	if j := regexp.MustCompile(`\n#{1,4} `).FindStringIndex(rest); j != nil {
		rest = rest[:j[0]]
	}

	return rest
}

// funcOf is the source of one top-level function, from its signature to the
// closing brace at the start of a line.
func funcOf(src, signature string) string {
	i := strings.Index(src, signature)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n}\n"); j >= 0 {
		rest = rest[:j]
	}

	return rest
}
