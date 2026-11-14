package scim_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/scim"
)

// What Microsoft's SCIM Validator sent, the times its page was run against
// this endpoint: a run a file under `testdata/validator`, a request a line, in
// the order they were answered -- asked again here, and held to what the page
// wanted.
//
// # Why beside what its tutorial says
//
// `TestWhatMicrosoftsValidatorAsks` is written from the tutorial, and the page
// sends what the tutorial does not say: one number under every `type` there
// is, a `manager` who is a word and nobody's id, `"primary": "true"`, a PATCH
// of no operations, a second DELETE of what it deleted. The first run found
// each of those by being refused, and none is something to learn twice.
//
// # Where a file comes from
//
// The workflow `scim-check` stands a roster up for the page and leaves what it
// was asked as its artifact (`docs/scim.md` § *Microsoft's validator*):
//
//	./scripts/scim-check.sh --fixture scim-check/requests.jsonl > scim/testdata/validator/<the day>.jsonl
//
// which keeps of each request the method, the path, the body, the status it was
// `answered` with, the id of what it `made` and how many a list `found`. Read
// against the page's report, two things are then added by hand to the rows it
// failed:
//
//   - `wants`, the status the page wanted where roster answered another;
//   - `finds`, how many it wanted found.
//
// A row with neither is held to what roster answered that day, which the page
// took. So a file says what the page sent, and what passing it is -- as far as
// a status and a count say it. What the page read out of an answer and did
// not find is held where it can be said of every answer ([readable]), and
// otherwise by the test that asks it by name (`TestWhatMicrosoftsValidatorAsks`).
//
// # What is not the same as that day
//
// The ids, which are made again: one a row `made` is looked for in the rows
// after it and written as the one made now. And the order, which here is one
// request at a time -- the page sends a dozen at once, and what that asks is
// asked by [TestTheValidatorAsksAtOnce].
//
// One thing is added to what the page sent, as `scripts/scim-check.sh` adds it
// in front of roster: an `externalId` on a person made without one.
type sent struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
	Text   string          `json:"text"`

	Answered int    `json:"answered"`
	Made     string `json:"made"`
	Found    *int   `json:"found"`

	Wants int  `json:"wants"`
	Finds *int `json:"finds"`
}

// readable is whether an answer is one the page reads: no object of it begins
// with `$ref` and goes on to say anything else.
//
// What the second run found, and no status says: the page answered *the
// schema of the fetched group is invalid* to the one group it read with
// anybody in it, whose members were written `$ref` first ([reference]). So
// every answer of every run is held to it here, whichever asks for one next.
func readable(answer []byte) error {
	if len(bytes.TrimSpace(answer)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(answer))

	var value func() error
	value = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('['):
			for dec.More() {
				if err := value(); err != nil {
					return err
				}
			}
			_, err := dec.Token()

			return err

		case json.Delim('{'):
			first := ""
			for n := 0; dec.More(); n++ {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				if n == 0 {
					first, _ = key.(string)
				}
				if n == 1 && first == "$ref" {
					return fmt.Errorf("an object begins with `$ref` and goes on to say %q: a reader of references takes it for one, and for nothing else", key)
				}
				if err := value(); err != nil {
					return err
				}
			}
			_, err := dec.Token()

			return err
		}

		return nil
	}

	return value()
}

// validatorRuns is every run kept, by its file.
func validatorRuns(t *testing.T) map[string][]sent {
	t.Helper()
	x := require.New(t)

	files, err := filepath.Glob(filepath.Join("testdata", "validator", "*.jsonl"))
	x.NoError(err)
	x.NotEmpty(files, "testdata/validator holds the runs")

	out := map[string][]sent{}
	for _, name := range files {
		f, err := os.Open(name)
		x.NoError(err)

		var rows []sent
		lines := bufio.NewScanner(f)
		lines.Buffer(nil, 1<<20)
		for lines.Scan() {
			if strings.TrimSpace(lines.Text()) == "" {
				continue
			}
			var row sent
			x.NoError(json.Unmarshal(lines.Bytes(), &row), "%s: %s", name, lines.Text())
			rows = append(rows, row)
		}
		x.NoError(lines.Err())
		x.NoError(f.Close())
		x.NotEmpty(rows, name)

		out[strings.TrimSuffix(filepath.Base(name), ".jsonl")] = rows
	}

	return out
}

// withExternalId is a User's body with an `externalId`, where it had none.
func withExternalId(t *testing.T, body string) string {
	t.Helper()

	var u map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(body), &u))
	if _, ok := u["externalId"]; ok {
		return body
	}
	v, err := json.Marshal(guid(t))
	require.NoError(t, err)
	u["externalId"] = v

	b, err := json.Marshal(u)
	require.NoError(t, err)

	return string(b)
}

func TestWhatMicrosoftsValidatorSent(t *testing.T) {
	for name, rows := range validatorRuns(t) {
		t.Run(name, func(t *testing.T) {
			d := stand(t)
			x := require.New(t)

			// An id of that day, and the one made for it now.
			now := map[string]string{}
			today := func(v string) string {
				for was, is := range now {
					v = strings.ReplaceAll(v, was, is)
				}

				return v
			}

			for i, row := range rows {
				path := today(row.Path)
				x.True(strings.HasPrefix(path, scim.Base), "row %d: %s is not under %s", i+1, path, scim.Base)

				var body any
				switch {
				case len(row.Body) > 0:
					v := today(string(row.Body))
					if row.Method == http.MethodPost && path == scim.Base+"/Users" {
						v = withExternalId(t, v)
					}
					body = v
				case row.Text != "":
					body = today(row.Text)
				}

				want := row.Answered
				if row.Wants != 0 {
					want = row.Wants
				}
				code, said, _ := d.raw(t, d.key, row.Method, path, body)
				var got map[string]any
				if len(bytes.TrimSpace(said)) > 0 {
					x.NoError(json.Unmarshal(said, &got), "row %d: %s", i+1, said)
				}
				x.Equal(want, code, "row %d: %s %s\n> %s\n< %v", i+1, row.Method, path, body, got)
				x.NoError(readable(said), "row %d: %s %s\n< %s", i+1, row.Method, path, said)

				if row.Made != "" {
					id, _ := got["id"].(string)
					x.NotEmpty(id, "row %d: %s %s made something that day", i+1, row.Method, path)
					now[row.Made] = id
				}

				finds := row.Found
				if row.Finds != nil {
					finds = row.Finds
				}
				if finds != nil && code == http.StatusOK {
					x.EqualValues(*finds, got["totalResults"], "row %d: %s %s\n< %v", i+1, row.Method, path, got)
				}
			}
		})
	}
}

// TestTheValidatorAsksAtOnce: the page runs its tests side by side, so what
// arrives is a dozen creates in the same few milliseconds -- every one of them
// the first group this tenant was asked to make, or the first person -- and
// each is answered as it would be alone.
//
// What stands to break is whatever is made the first time it is needed: the
// role a group's maker holds in it (`core.Manages`) is one a tenant has one
// of, and eight groups made at once each find it missing.
func TestTheValidatorAsksAtOnce(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	// ask is [deployment.call] where a failure is handed back, since it is
	// not this goroutine's to end the test.
	ask := func(method, path string, body any) (int, string, error) {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
		req, err := http.NewRequestWithContext(t.Context(), method, d.scim.URL+path, bytes.NewReader(b))
		if err != nil {
			return 0, "", err
		}
		req.Header.Set("Authorization", "Bearer "+d.key)
		req.Header.Set("Content-Type", "application/scim+json; charset=utf-8")

		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer res.Body.Close()
		out, err := io.ReadAll(res.Body)

		return res.StatusCode, string(out), err
	}

	const each = 12
	type answer struct {
		what string
		code int
		body string
		err  error
	}
	answers := make([]answer, 2*each)

	var wg sync.WaitGroup
	for i := range each {
		g := map[string]any{
			"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:Group"},
			"displayName": fmt.Sprintf("AT ONCE %s", made(t)),
		}
		u := validatorUser(t)

		wg.Add(2)
		go func() {
			defer wg.Done()
			code, body, err := ask(http.MethodPost, scim.Base+"/Groups", g)
			answers[2*i] = answer{"a group", code, body, err}
		}()
		go func() {
			defer wg.Done()
			code, body, err := ask(http.MethodPost, scim.Base+"/Users", u)
			answers[2*i+1] = answer{"a person", code, body, err}
		}()
	}
	wg.Wait()

	for _, a := range answers {
		x.NoError(a.err, a.what)
		x.Equal(http.StatusCreated, a.code, "%s: %s", a.what, a.body)
	}
}
