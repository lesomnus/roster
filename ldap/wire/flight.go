package wire

import (
	"context"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
)

// What one connection has going at once, and how long each part of it may
// take. Constants, as the bounds on a message are and for the reason given
// there (`read.go`).
const (
	// MaxInFlight is how many searches a connection that has bound has
	// running at once. Each is a goroutine and a walk of roster with the
	// directory's own key, and nothing counted them: five thousand ran at
	// once on one connection. A client that asks one thing at a time has
	// one. One that answers a whole host through a single connection, as
	// SSSD does, has as many as that host has lookups waiting, which is tens
	// on a busy one. A hundred is past that, and a hundred waiting are under
	// a megabyte.
	//
	// One over it is answered `busy` and the connection goes on, since the
	// loop has to keep reading for an abandon to reach what is running. A
	// connection that has not bound has none at all: [Server.ServeConn].
	MaxInFlight = 100

	// WriteTimeout is how long one message may take to be written. A write
	// waits only on a client that is not reading, and waited for good: every
	// search such a client had asked for stood in its write, with what it
	// had been asked, for as long as the client cared to stay. The largest
	// thing written is one entry, a group and its members, and half a minute
	// is that over the slowest link there is a reason to run a directory
	// over.
	WriteTimeout = 30 * time.Second

	// BindTimeout is how long the directory has to answer a bind. Nothing is
	// read while it does, so nothing else on the connection can end it, the
	// idle clock included. It is the thirty seconds roster gives a call of
	// its own (`server.timeout`), since a bind is one call to roster.
	BindTimeout = 30 * time.Second
)

// begin counts a search in, under the identifier an abandon will name it by,
// and says no to one more than a connection may have. It is the loop that
// calls it, before the search has a goroutine: what the next message on the
// connection says of this one -- an abandon, StartTLS -- finds it here.
//
// The search runs under the context returned, and `done` counts it out
// again: once, however often it is called, since by a second call the
// identifier may be another search's.
func (c *Conn) begin(ctx context.Context, id int64) (search context.Context, done func(), ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.inflight) >= MaxInFlight {
		return nil, nil, false
	}
	search, cancel := context.WithCancel(ctx)
	c.inflight[id] = cancel

	out := false

	return search, func() {
		cancel()
		c.mu.Lock()
		if !out {
			delete(c.inflight, id)
			out = true
		}
		c.mu.Unlock()
	}, true
}

// last writes the message that ends a search and counts the search out, as
// one step to everything else that writes.
//
// Counted out first, so that a client that has its answer finds the
// identifier free and nothing in flight: counted out after, the next message
// of a quick client was refused as naming a search still running, and its
// StartTLS as having one beside it. And under the lock a write takes, so
// that what the loop does on finding nothing in flight -- StartTLS, whose
// answer is a write -- comes after this answer on the wire and not into the
// middle of it.
func (c *Conn) last(out func(), p *ber.Packet) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	out()

	return c.send(p)
}

// outstanding is whether a search under this identifier is in flight.
func (c *Conn) outstanding(id int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, ok := c.inflight[id]

	return ok
}

func (c *Conn) abandon(id int64) {
	c.mu.Lock()
	cancel, ok := c.inflight[id]
	c.mu.Unlock()
	if ok {
		cancel()
	}
}

// abandonAll ends every search in flight, as an abandon of each would.
func (c *Conn) abandonAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, cancel := range c.inflight {
		cancel()
	}
}

func (c *Conn) busy() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.inflight)
}
