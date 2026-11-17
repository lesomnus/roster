package login_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/login"
)

// formData is an answer as a page's `FormData` sends one: multipart.
func formData(t *testing.T, fields map[string]string) (string, *bytes.Buffer) {
	t.Helper()

	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	for k, v := range fields {
		require.NoError(t, w.WriteField(k, v))
	}
	require.NoError(t, w.Close())

	return w.FormDataContentType(), body
}

// TestAScreensAnswerIsReadToASize: the three screens that answer with a form --
// consent, sign-out, a device's code -- read what was posted to a size, as the
// sign-in's two forms always have.
//
// They read it with `r.FormValue`, which reads a form to Go's own ten
// megabytes and a multipart one onto the disk past thirty-two, for a route
// anybody may post to with no cookie: `POST /device` given far more than a
// code answered as if it were one, having read it all. Each form here is a
// challenge and a word, so what is read is a few kilobytes -- room for a
// challenge several times the size Hydra mints -- and more is 413 with nothing
// asked of Hydra.
func TestAScreensAnswerIsReadToASize(t *testing.T) {
	x := require.New(t)
	d := serveWith(t, login.Ask)
	b := d.browser(t)

	d.hydra.raise("c1", "contoso-web")
	_, code := d.signIn(t, b, "c1", "erin", password)
	x.Equal(http.StatusOK, code)
	d.hydra.asked("l1", "contoso-web", false)
	d.hydra.printed("d1", "WDJB-MJHT")

	// Well past an answer, and under what Go's server reads off a connection
	// after a handler has stopped reading -- so the refusal is read back from a
	// connection that was closed tidily, whatever the machine is doing.
	much := strings.Repeat("A", 64<<10)

	for path, answer := range map[string]url.Values{
		"/consent": {"consent_challenge": {"c1"}, "allow": {"1"}, "more": {much}},
		"/logout":  {"logout_challenge": {"l1"}, "allow": {"1"}, "more": {much}},
		"/device":  {"device_challenge": {"d1"}, "user_code": {"WDJB-MJHT"}, "more": {much}},
	} {
		res, err := b.PostForm(d.app.URL+path, answer)
		x.NoError(err)
		res.Body.Close()
		x.Equal(http.StatusRequestEntityTooLarge, res.StatusCode, path)
	}

	// The other shape a form is posted in, which was the one read onto a disk.
	kind, body := formData(t, map[string]string{"device_challenge": "d1", "user_code": "WDJB-MJHT", "more": much})
	res, err := b.Post(d.app.URL+"/device", kind, body)
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusRequestEntityTooLarge, res.StatusCode, "a multipart answer")

	// Nothing of them was acted on.
	_, claims := d.hydra.told()
	x.Nil(claims, "a consent was granted from a body read past its size")
	x.False(d.hydra.ended("l1"), "a sign-out was confirmed from a body read past its size")
	_, typed := d.hydra.sent("d1")
	x.False(typed, "a device's code was handed to hydra from a body read past its size")

	// And an answer the size of an answer is read as before: a challenge four
	// times what Hydra mints today, about two kilobytes, is still one.
	long := "d-" + strings.Repeat("c", 8<<10)
	d.hydra.printed(long, "WDJB-MJHT")
	res, err = b.PostForm(d.app.URL+"/device", url.Values{"device_challenge": {long}, "user_code": {"WDJB-MJHT"}})
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode)

	// In either shape. A page of a deployment's own that posts a `FormData` was
	// read before there was a size, and a size is no reason to stop reading it.
	d.hydra.printed("d2", "WDJB-MJHT")
	kind, body = formData(t, map[string]string{"device_challenge": "d2", "user_code": "WDJB-MJHT"})
	res, err = b.Post(d.app.URL+"/device", kind, body)
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusOK, res.StatusCode, "an answer posted as a `FormData` was not read")
	got, _ := d.hydra.sent("d2")
	x.Equal("WDJB-MJHT", got)
}
