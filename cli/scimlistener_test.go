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

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestTheEndpointsListenerHasClocks: the listener a directory's cloud reaches
// puts a clock and a size on what a stranger controls, as the account app's
// and the Login App's do.
//
// `serveScim` served with `http.Server{Handler: …}` and nothing else: a
// connection opened and left half way through a header was kept for as long
// as its asker liked, and a header could be a megabyte -- every one of them a
// descriptor of the process, which under `roster serve` is roster's own
// (#282). Nothing bounds the answer, as there: a page of people is read from
// roster while it is written.
//
// The idle clock is the third and is not sat out here: it is two minutes.
func TestTheEndpointsListenerHasClocks(t *testing.T) {
	x := require.New(t)
	ctx, stop := context.WithCancel(t.Context())

	// Nothing behind it answers, and nothing here asks anything that would
	// need it to.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)

	done := make(chan error, 1)
	go func() {
		done <- serveScim(cmd.Held(ctx, l), cmd.ScimConfig{
			Addr:     l.Addr().String(),
			Roster:   "127.0.0.1:1",
			Insecure: true,
		})
	}()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Error("the endpoint did not stop when it was told to")
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
	_, err = io.WriteString(half, "GET /scim/v2/Users HTTP/1.1\r\nHost: scim.test\r\nX-Slow: ")
	x.NoError(err)
	began := time.Now()

	// More header than is read. Go's own bound is a megabyte, so this was
	// read whole and the request answered.
	big := dial()
	_, err = io.WriteString(big, "GET /scim/v2/Users HTTP/1.1\r\nHost: scim.test\r\nX-Much: "+strings.Repeat("a", 256<<10)+"\r\n\r\n")
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

// TestAConcurrencyBelowNoughtIsRefusedAtStart: `scim.concurrency: -3` cannot
// be meant, and is refused where the file is read, naming the key, as an
// `audit:` block that cannot be meant is (#398). It was read as four with
// nothing said. Nought, or the key left out, is four.
func TestAConcurrencyBelowNoughtIsRefusedAtStart(t *testing.T) {
	t.Run("roster scim serve", func(t *testing.T) {
		x := require.New(t)
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		l, err := net.Listen("tcp", "127.0.0.1:0")
		x.NoError(err)
		defer l.Close()

		done := make(chan error, 1)
		go func() {
			done <- serveScim(cmd.Held(ctx, l), cmd.ScimConfig{
				Addr: l.Addr().String(), Roster: "127.0.0.1:1", Insecure: true, Concurrency: -3,
			})
		}()
		select {
		case err := <-done:
			x.ErrorContains(err, "scim.concurrency")
		case <-time.After(10 * time.Second):
			stop()
			<-done
			x.Fail("the endpoint started and served")
		}
	})

	t.Run("roster serve", func(t *testing.T) {
		x := require.New(t)
		drv, dsn := pdtest.DB(t)
		cdrv, cdsn := pdtest.DB(t)

		build := func(n int) error {
			s, err := cmd.Build(t.Context(), cmd.Config{
				Db:      config.DbConfig{Driver: drv, Dsn: dsn},
				Watch:   config.WatchConfig{Broker: config.BrokerMemory},
				Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
				Scim:    cmd.ScimConfig{Addr: "127.0.0.1:0", Concurrency: n},
			})
			if s != nil {
				s.Close()
			}

			return err
		}
		x.ErrorContains(build(-3), "scim.concurrency")
		x.NoError(build(0))
		x.NoError(build(4))
	})
}
