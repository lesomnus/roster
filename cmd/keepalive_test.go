package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"

	"github.com/lesomnus/roster/cmd"
)

// TestWhereNothingIsSaidOfKeepaliveAFollowerMayAsk: roster passed gRPC's
// keepalive options only where `server.keepalive` said something, and no file
// it ships says anything. gRPC's own are a server that asks after two hours
// and a client allowed to ask once every five minutes, so a follower that
// asked whether an idle stream was still there was hung up on,
// `too_many_pings`, and could not tell a connection that died from a quiet
// tenant without it.
//
// Where the file leaves a key unsaid, the server asks an idle connection every
// minute and waits twenty seconds for the answer, and a client may ask every
// twenty seconds: below the thirty the manual tells a follower to dial with. A
// value a deployment says is kept as it said it.
func TestWhereNothingIsSaidOfKeepaliveAFollowerMayAsk(t *testing.T) {
	t.Run("nothing said", func(t *testing.T) {
		x := require.New(t)
		k := cmd.Config{}.ServerKeepalive()
		x.Equal(time.Minute, k.Time, "how long the server waits before asking an idle connection")
		x.Equal(20*time.Second, k.Timeout, "how long it waits for the answer")
		x.Equal(20*time.Second, k.MinTime, "how often a client may ask")
		x.Less(k.MinTime, 30*time.Second, "a follower dialling as the manual says would be hung up on")
		x.Zero(k.MaxConnectionAge)
		x.Zero(k.MaxConnectionAgeGrace)
	})

	t.Run("something said", func(t *testing.T) {
		x := require.New(t)
		var c cmd.Config
		c.Server.Keepalive = config.KeepaliveConfig{
			Time:             2 * time.Hour,
			MinTime:          5 * time.Minute,
			MaxConnectionAge: 30 * time.Minute,
		}
		k := c.ServerKeepalive()
		x.Equal(2*time.Hour, k.Time, "a value said is kept")
		x.Equal(5*time.Minute, k.MinTime, "a value said is kept")
		x.Equal(20*time.Second, k.Timeout, "a key left unsaid beside them is still the default")
		x.Equal(30*time.Minute, k.MaxConnectionAge)
		x.Zero(k.MaxConnectionAgeGrace, "nothing is defaulted for the grace")
	})
}

// TestAnAgeWithNoGraceIsSaidAtStart: `keepalive.max_connection_age` asks a
// client to move once its connection is that old, and gRPC then waits for a
// stream already open for as long as `max_connection_age_grace` says, which is
// for ever where nothing does. So an age alone ends no sync stream, which the
// manual and a comment said it moved. `serve` says so once as it starts, as a
// warning naming the key; nothing is defaulted for the grace.
func TestAnAgeWithNoGraceIsSaidAtStart(t *testing.T) {
	const what = "max_connection_age_grace"

	t.Run("an age and no grace", func(t *testing.T) {
		x := require.New(t)
		said := servedSaying(t, func(c *cmd.Config) {
			c.Server.Keepalive.MaxConnectionAge = 30 * time.Minute
		}).lines(t, what)
		x.Len(said, 1, "said once, as serve starts")
		x.Equal("WARN", said[0]["level"])
		x.Contains(said[0]["msg"], "server.keepalive.max_connection_age", "with the key that was said")
	})

	t.Run("an age and a grace", func(t *testing.T) {
		said := servedSaying(t, func(c *cmd.Config) {
			c.Server.Keepalive.MaxConnectionAge = 30 * time.Minute
			c.Server.Keepalive.MaxConnectionAgeGrace = time.Minute
		}).lines(t, what)
		require.Empty(t, said)
	})

	t.Run("neither", func(t *testing.T) {
		require.Empty(t, servedSaying(t, nil).lines(t, what))
	})
}
