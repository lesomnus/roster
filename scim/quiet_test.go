package scim_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
)

// loud is the lines of what was said that are at `WARN` or `ERROR`.
func loud(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.Contains(l, "level=ERROR") || strings.Contains(l, "level=WARN") {
			out = append(out, l)
		}
	}

	return out
}

// TestABearerThatIsNoTokenIsRefusedAndSaysNothing: a bearer that cannot be a
// key is `401` in the words a bearer that is no tenant key gets, roster is
// not asked, and the log says nothing.
//
// `bearer` took any bytes after `rt_`, and gRPC will not send a header that
// holds some of them: `rt_café` was answered `500`, *something went wrong
// here*, with an `ERROR` line -- a line anybody who reaches the listener
// could write as often as they liked. A key is `rt_` and RFC 6750's
// `b64token` letters.
func TestABearerThatIsNoTokenIsRefusedAndSaysNothing(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	g := newGate()
	base := d.gated(t, g)
	me := "/" + rstr.MeService_ServiceDesc.ServiceName + "/Get"
	d.log.lines()

	for _, token := range []string{"rt_café", "rt_a\"b", "rt_", "rt_=abc", "rt_abc def"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+scim.Base+"/Users", nil)
		x.NoError(err)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		x.NoError(err)
		res.Body.Close()
		x.Equal(http.StatusUnauthorized, res.StatusCode, "%q", token)
	}
	x.Zero(g.asked(me), "roster was asked who a bearer that is no key is")
	x.Empty(d.log.lines())

	// And a key's letters are all taken.
	x.Equal(http.StatusOK, ask(base, d.key, http.MethodGet, scim.Base+"/Users"))
}

// TestAnAskerWhoHangsUpIsNoError: a request whose asker goes before it is
// answered leaves no line at `WARN` or `ERROR`, and nothing is written to
// it.
//
// It was `scim: unexpected … context canceled` at `ERROR`, and a directory
// that gives up on a slow page is ordinary. What the log is loud about is
// what this deployment has to mend.
func TestAnAskerWhoHangsUpIsNoError(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	erinId := d.someone(t, "erin", erinOid)
	g := newGate()
	base := d.gated(t, g)
	x.Equal(http.StatusOK, ask(base, d.key, http.MethodGet, scim.Base+"/Users/"+erinId))
	d.log.lines()

	g.close("/"+rstr.HolderService_ServiceDesc.ServiceName+"/Get", d.key)
	ctx, hangUp := context.WithCancel(t.Context())
	asked := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+scim.Base+"/Users/"+erinId, nil)
		if err != nil {
			asked <- err

			return
		}
		req.Header.Set("Authorization", "Bearer "+d.key)
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			res.Body.Close()
		}
		asked <- err
	}()
	g.wait(t, 1)
	hangUp()
	x.ErrorIs(<-asked, context.Canceled)

	// The endpoint is done with it before roster is.
	g.serving.Wait()
	g.release()
	x.Empty(loud(d.log.lines()))
}

// TestTheLogNamesAnAttributeAndNotWhatAPathSaid: what is not kept is said in
// the log by the attribute's name, and not by the path the directory wrote,
// which may hold an address: `emails[value eq "erin@contoso.example"]` was
// written whole, where the manual says no address is.
func TestTheLogNamesAnAttributeAndNotWhatAPathSaid(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	erinId := d.someone(t, "erin", erinOid)
	d.log.lines()

	code, got, _ := d.call(t, d.key, http.MethodPatch, scim.Base+"/Users/"+erinId, map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{{"op": "remove", "path": `emails[value eq "erin@contoso.example"]`}},
	})
	x.Equal(http.StatusOK, code, "%v", got)

	var said []string
	for _, l := range d.log.lines() {
		if strings.Contains(l, "not kept here") {
			said = append(said, l)
		}
	}
	x.Len(said, 1)
	x.Contains(said[0], "attributes=[emails]")
	x.NotContains(said[0], "erin@")
}

// TestAMethodAPathDoesNotTakeIs405: a path the endpoint serves, asked by a
// method it does not take, is `405` with `Allow`, in SCIM's error body. It
// was `404`, *nothing is served here but /scim/v2*, which is not so of
// `/scim/v2/Users`. A path it does not serve is still `404`.
func TestAMethodAPathDoesNotTakeIs405(t *testing.T) {
	d := stand(t)
	x := require.New(t)
	erinId := d.someone(t, "erin", erinOid)

	for _, tc := range []struct{ method, path, allow string }{
		{http.MethodPost, "/Users/" + erinId, "GET, HEAD, PUT, PATCH, DELETE"},
		{http.MethodDelete, "/Users", "GET, HEAD, POST"},
		{http.MethodPut, "/Groups", "GET, HEAD, POST"},
		{http.MethodPost, "/Schemas", "GET, HEAD"},
	} {
		code, got, h := d.call(t, d.key, tc.method, scim.Base+tc.path, nil)
		x.Equal(http.StatusMethodNotAllowed, code, "%s %s: %v", tc.method, tc.path, got)
		x.Equal(tc.allow, h.Get("Allow"), "%s %s", tc.method, tc.path)
		x.Equal("405", got["status"])
		x.Equal("application/scim+json", h.Get("Content-Type"))
	}

	code, got, _ := d.call(t, d.key, http.MethodGet, scim.Base+"/Bulk", nil)
	x.Equal(http.StatusNotFound, code, "%v", got)
	x.Equal("nothing is served here but "+scim.Base, got["detail"])
	code, _, _ = d.call(t, d.key, http.MethodGet, scim.Base+"/Users/"+erinId+"/more", nil)
	x.Equal(http.StatusNotFound, code)
}
