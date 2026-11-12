package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/cmd"
)

// TestAHeldListenerIsTheOneListenedOn: a listener handed over (`cmd.Held`) is
// what `serve` listens on at its address -- the same socket, so the port is
// never free between the caller binding it and the server taking it -- once,
// and every other address is bound as it always was.
func TestAHeldListenerIsTheOneListenedOn(t *testing.T) {
	x := require.New(t)
	l, addr := listening(t)
	ctx := cmd.Held(t.Context(), l)

	got, err := cmd.Listen(ctx, addr)
	x.NoError(err)
	x.Same(l, got, "the listener handed over, and not the port bound again")

	// Once: a second server asking for the address binds it, and finds it the
	// first one's.
	_, err = cmd.Listen(ctx, addr)
	x.ErrorContains(err, "address already in use")

	other, err := cmd.Listen(ctx, "127.0.0.1:0")
	x.NoError(err)
	x.NotEqual(addr, other.Addr().String())
	x.NoError(other.Close())

	// And nothing held is nothing changed.
	plain, err := cmd.Listen(t.Context(), "127.0.0.1:0")
	x.NoError(err)
	x.NoError(plain.Close())
}
