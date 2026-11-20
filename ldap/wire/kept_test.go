package wire_test

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/ldap/wire"
)

// keeper is a handler that keeps something of whoever binds: a name of its
// own spelling, and a value it will know them by. One password of its keeps
// both and then refuses.
type keeper struct {
	mu   sync.Mutex
	conn *wire.Conn
	who  *int
}

func (k *keeper) Bind(ctx context.Context, c *wire.Conn, req wire.BindRequest) wire.Result {
	who := new(int)
	c.Keep("cn=as the tree spells it", who)

	k.mu.Lock()
	k.conn, k.who = c, who
	k.mu.Unlock()

	if string(req.Password) == "kept and then refused" {
		return wire.Refuse(wire.InvalidCredentials, "")
	}

	return wire.Ok
}

func (k *keeper) Search(ctx context.Context, c *wire.Conn, req wire.SearchRequest, w *wire.Search) wire.Result {
	return wire.Ok
}

// TestWhatAHandlerKeepsOfABindIsTheConnections: the name a connection is bound
// as and what its handler knows of whoever bound are one thing, kept together
// and gone together.
//
// A connection kept the request's own spelling of a name and nothing else, so
// a handler had nowhere to keep who had bound and no way to say they no longer
// may -- which is how a connection went on being somebody's after they were
// stopped (#281). What a handler keeps is answered back to it, names the
// connection, and is dropped by the handler, by a bind that fails and by the
// next bind; and a drop that names something no longer kept changes nothing,
// since the search that asks for one runs beside the loop that binds.
func TestWhatAHandlerKeepsOfABindIsTheConnections(t *testing.T) {
	x := require.New(t)

	k := &keeper{}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	x.NoError(err)
	s := &wire.Server{Handler: k}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { l.Close(); s.Close() })

	c, err := ldap.DialURL("ldap://" + l.Addr().String())
	x.NoError(err)
	t.Cleanup(func() { c.Close() })

	whoami := func() string {
		who, err := c.WhoAmI(nil)
		x.NoError(err)

		return who.AuthzID
	}
	kept := func() (*wire.Conn, *int) {
		k.mu.Lock()
		defer k.mu.Unlock()

		return k.conn, k.who
	}

	x.NoError(c.Bind("CN=As The Client Spelled It", "secret"))
	conn, first := kept()
	x.Equal("dn:cn=as the tree spells it", whoami(), "the name the handler kept is the connection's")
	x.Same(first, conn.Kept())

	// A drop of something that is not what is kept: the connection is as it was.
	conn.Drop(new(int))
	x.Equal("dn:cn=as the tree spells it", whoami())
	x.Same(first, conn.Kept())

	// A bind made since, and a drop that still names the one before it.
	x.NoError(c.Bind("cn=somebody", "secret"))
	conn, second := kept()
	conn.Drop(first)
	x.Equal("dn:cn=as the tree spells it", whoami(), "a drop undid a bind made after what it named")
	x.Same(second, conn.Kept())

	// The drop of what is kept: nobody's, as a connection that never bound is.
	conn.Drop(second)
	x.Empty(whoami())
	x.Nil(conn.Kept())
	_, bound := conn.Bound()
	x.False(bound)

	// And what a handler kept before it refused is not a bind.
	x.NoError(c.Bind("cn=somebody", "secret"))
	err = c.Bind("cn=somebody", "kept and then refused")
	x.True(ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials), "%v", err)
	conn, _ = kept()
	x.Empty(whoami(), "a refused bind left the connection somebody's")
	x.Nil(conn.Kept())
}
