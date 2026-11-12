package cmd

import (
	"context"
	"net"
	"sync"
)

// Listen is a TCP listener at `addr`, opened the way every listener `serve`
// opens is: with `net.Listen`, unless the context carries one already
// listening at that address ([Held]), which is then answered with instead.
//
// One function for every listener because a caller that has to know an
// address before the process it starts has bound it -- a test, which writes
// the address into the configuration and dials it afterwards -- has two ways to
// get one, and only one of them is safe. Picking a port by listening at `:0`
// and closing leaves it free until the server binds it again, and on a machine
// running other tests something else takes it first: the server is refused
// with `address already in use`, or another test's listener answers what was
// meant for this one -- both of which `TestServeOpensTheConsumersItIsToldTo`
// failed with on CI. Handed over still listening, the port is never free.
func Listen(ctx context.Context, addr string) (net.Listener, error) {
	if h, ok := ctx.Value(heldKey{}).(*listeners); ok {
		if l := h.take(addr); l != nil {
			return l, nil
		}
	}

	return net.Listen("tcp", addr)
}

// Held has [Listen] answer with these listeners, each for the address it is
// listening at, rather than opening one -- each once, so that two servers
// asking for one address do not share a socket.
//
// A listener `serve` never asked for is left open, as the caller handed it
// over: the caller closes it.
func Held(ctx context.Context, ls ...net.Listener) context.Context {
	h := &listeners{at: map[string]net.Listener{}}
	for _, l := range ls {
		h.at[l.Addr().String()] = l
	}

	return context.WithValue(ctx, heldKey{}, h)
}

type heldKey struct{}

type listeners struct {
	mu sync.Mutex
	at map[string]net.Listener
}

func (h *listeners) take(addr string) net.Listener {
	h.mu.Lock()
	defer h.mu.Unlock()

	l := h.at[addr]
	delete(h.at, addr)

	return l
}
