package cli

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestTheLoginAppsListenerHasClocks: the listener Hydra sends every browser to
// puts a clock and a size on what a stranger controls, as the account app's
// does.
//
// `serveLogin` served with `http.Server{Handler: …}` and nothing else, so a
// request's headers could take for ever to arrive and be a megabyte when they
// did: a connection opened and left half way through a header was kept until
// the process stopped. The account app's listener got its clocks in #277 and
// this one, which faces the same internet, had not (#278).
//
// The idle clock is the third and is not sat out here: it is two minutes.
func TestTheLoginAppsListenerHasClocks(t *testing.T) {
	x := require.New(t)
	ctx, stop := context.WithCancel(t.Context())

	// The listener is handed over, so the test knows where it is; nothing
	// behind it answers, and nothing here asks anything that would need it to.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)

	done := make(chan error, 1)
	go func() {
		done <- serveLogin(cmd.Held(ctx, l), cmd.LoginConfig{
			Addr:           l.Addr().String(),
			Roster:         "127.0.0.1:1",
			Insecure:       true,
			Hydra:          cmd.HydraConfig{Admin: "http://127.0.0.1:1"},
			Key:            "rk_nobody",
			InsecureCookie: true,
		})
	}()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Error("the login app did not stop when it was told to")
		}
	})

	dial := func() net.Conn {
		c, err := net.Dial("tcp", l.Addr().String())
		x.NoError(err)
		t.Cleanup(func() { c.Close() })

		return c
	}

	// Half a header, and then nothing: begun now, read at the end, so that the
	// wait is spent once.
	half := dial()
	_, err = io.WriteString(half, "GET / HTTP/1.1\r\nHost: login.test\r\nX-Slow: ")
	x.NoError(err)
	began := time.Now()

	// More header than is read. Go's own bound is a megabyte, so this was
	// read whole and the request answered.
	big := dial()
	_, err = io.WriteString(big, "GET / HTTP/1.1\r\nHost: login.test\r\nX-Much: "+strings.Repeat("a", 256<<10)+"\r\n\r\n")
	x.NoError(err)
	x.NoError(big.SetReadDeadline(time.Now().Add(10 * time.Second)))
	res, err := http.ReadResponse(bufio.NewReader(big), nil)
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusRequestHeaderFieldsTooLarge, res.StatusCode, "a quarter of a megabyte of header was read")

	// And the connection left half way is hung up on, in about the ten seconds
	// a header has. It was kept: the read below ran out its own clock instead.
	x.NoError(half.SetReadDeadline(began.Add(25 * time.Second)))
	_, err = half.Read(make([]byte, 1))
	var timeout net.Error
	x.False(errors.As(err, &timeout) && timeout.Timeout(),
		"a connection half way through a header was still held after %s", time.Since(began).Round(time.Second))
	x.ErrorIs(err, io.EOF)
	x.Less(time.Since(began), 20*time.Second)
}
