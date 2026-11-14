package scim_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// TestWhatIsRefusedIsSaidInTheLog: a refusal is answered to the directory, and
// is a line in this process's log as well -- the method, the path, the status
// and what was said -- since whoever runs the deployment reads this log and
// not the directory's.
//
// It left none. Somebody the directory could not suspend, an address it could
// not take and a roster the endpoint could not reach were each an answer and
// nothing else, and the third is the one nobody but this deployment can mend.
func TestWhatIsRefusedIsSaidInTheLog(t *testing.T) {
	d := stand(t)
	x := require.New(t)

	// refused is the lines that say a refusal, of what was said since the
	// last time it was asked.
	refused := func() []string {
		var out []string
		for _, line := range d.log.lines() {
			if strings.Contains(line, "scim: refused") {
				out = append(out, line)
			}
		}

		return out
	}

	code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", erin())
	x.Equal(http.StatusCreated, code, "%v", got)
	id := got["id"].(string)
	x.Empty(refused(), "a write that was made is not a refusal")

	t.Run("somebody who is taken", func(t *testing.T) {
		x := require.New(t)

		code, got, _ := d.call(t, d.key, http.MethodPost, scim.Base+"/Users", erin())
		x.Equal(http.StatusConflict, code, "%v", got)

		lines := refused()
		x.Len(lines, 1)
		x.Contains(lines[0], "method=POST")
		x.Contains(lines[0], "path="+scim.Base+"/Users")
		x.Contains(lines[0], "status=409")
		x.Contains(lines[0], "scimType=uniqueness")
	})

	t.Run("a suspension that is not the directory's to lift", func(t *testing.T) {
		x := require.New(t)

		k, err := pdid.Parse(id)
		x.NoError(err)
		_, err = d.s.Ungated.Holder().Disable(t.Context(), rstr.HolderDisableRequest_builder{
			Ref: rstr.HolderRef_builder{Id: k.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, map[string]any{
			"Operations": []map[string]any{{"op": "replace", "path": "active", "value": true}},
		})
		x.Equal(http.StatusForbidden, code, "%v", got)

		lines := refused()
		x.Len(lines, 1)
		x.Contains(lines[0], "method=PATCH")
		x.Contains(lines[0], "path="+scim.Base+"/Users/"+id, "the path names who, by identifier")
		x.Contains(lines[0], "status=403")
		x.Contains(lines[0], "detail=")
	})

	t.Run("a body that is not one", func(t *testing.T) {
		x := require.New(t)

		code, _, _ := d.raw(t, d.key, http.MethodPatch, scim.Base+"/Users/"+id, `{"schemas":[]}`)
		x.Equal(http.StatusBadRequest, code)

		lines := refused()
		x.Len(lines, 1)
		x.Contains(lines[0], "status=400")
		x.Contains(lines[0], "scimType=invalidSyntax")
	})

	// Not refusals: what a directory is told of what is not there, which it
	// asks as a matter of course, and a key that is nobody's, which anybody
	// who reaches the listener can cause.
	t.Run("but not what is not there, nor a key that is nobody's", func(t *testing.T) {
		x := require.New(t)

		code, _, _ := d.call(t, d.key, http.MethodGet, scim.Base+"/Users/"+guid(t), nil)
		x.Equal(http.StatusNotFound, code)

		code, _, h := d.call(t, "rt_nobodys", http.MethodGet, scim.Base+"/Users/"+id, nil)
		x.Equal(http.StatusUnauthorized, code)
		x.NotEmpty(h.Get("WWW-Authenticate"))

		x.Empty(d.log.lines(), "nothing is said of either")
	})
}

// TestARosterThatDoesNotAnswerIsSaidInTheLog: the endpoint answers `503` when
// it cannot reach roster, to every call, and says why where the deployment
// reads it -- the one refusal that is the deployment's own to mend, and the
// only place the reason is written down.
//
// A `scim:` block with no `insecure: true` in front of a listener with no TLS
// is this, and was a directory failing every cycle beside a log that said
// nothing.
func TestARosterThatDoesNotAnswerIsSaidInTheLog(t *testing.T) {
	x := require.New(t)

	log := &said{}
	// A roster that speaks plain gRPC, dialled as one that speaks TLS.
	d := stand(t)
	e, err := scim.New(scim.Config{Roster: d.rpc, Log: slog.New(slog.NewTextHandler(log, nil))})
	x.NoError(err)
	t.Cleanup(func() { e.Close() })
	front := httptest.NewServer(e)
	t.Cleanup(front.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, front.URL+scim.Base+"/Users", nil)
	x.NoError(err)
	req.Header.Set("Authorization", "Bearer "+d.key)
	res, err := http.DefaultClient.Do(req)
	x.NoError(err)
	x.NoError(res.Body.Close())
	x.Equal(http.StatusServiceUnavailable, res.StatusCode)

	lines := log.lines()
	x.Len(lines, 1, "%v", lines)
	x.Contains(lines[0], "level=WARN")
	x.Contains(lines[0], "scim: roster did not answer")
	x.Contains(lines[0], "path="+scim.Base+"/Users")
	x.Contains(lines[0], "err=", "the reason, which is nowhere else")
}
