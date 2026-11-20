package wire

import (
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"time"
)

// What a listener holds at once, and for how long before a bind. Constants,
// as the bounds on a message are and for the reason given there (`read.go`).
const (
	// MaxConns is how many connections one server has open. A connection is
	// a descriptor, and where the directory is a block of `roster serve` it
	// is one of roster's own: where they are scarce a process has 4096 (an
	// older Linux's hard limit, which Go raises its own to), and a quarter
	// of that leaves the rest to the database and the ports roster answers
	// on. It is also what strangers cost all together at the most -- each
	// connection a goroutine and one message of [MaxMessageUnbound] being
	// read, so a hundred megabytes or so were every one of them full.
	//
	// It is the one number here a deployment may come to want to say: one
	// whose every host keeps a connection of its own open (SSSD does) has as
	// many clients as it has hosts. Such a deployment runs a second
	// `roster ldap serve` beside the first until this is a setting.
	MaxConns = 1024

	// MaxConnsPerAddr is how many of them one address has. It is for the
	// client that leaks its connections, which is then refused its next one
	// while everybody else is answered; it is not much of a guard against
	// somebody who means it, who has more addresses than one.
	//
	// A quarter of all of them, and not the ten or twenty one client needs,
	// because an address is not always a client: behind a proxy that does
	// not say who it speaks for, or behind one NAT, every client of a
	// deployment is one address, and this is then what they have between
	// them. 256 at once is the pools of two dozen applications and every
	// appliance of a site.
	MaxConnsPerAddr = 256

	// UnboundReadTimeout is how long a connection that has not bound has to
	// say each thing it says, the first included; [DefaultReadTimeout] is for
	// one that has. A client that is going to bind does so in the round trip
	// after it connects, or after a read of the root DSE and StartTLS, and
	// the handshake of LDAPS is inside this as well. Half a minute is a slow
	// link several times over, and a tenth of what somebody who has bound may
	// sit idle.
	UnboundReadTimeout = 30 * time.Second
)

// Serve accepts on `l` until it is closed, or broken.
//
// An accept that fails for a moment is waited out and asked again, as
// `net/http`'s loop does it: the usual one is a process out of descriptors,
// which passes when a connection anywhere in it is closed, and a listener
// that returned on it answered nobody from then on. An error that is not of
// a moment is returned, and it is the caller's to say what ends with it.
func (s *Server) Serve(l net.Listener) error {
	var wait time.Duration
	for {
		nc, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			var passing interface{ Temporary() bool }
			if !errors.As(err, &passing) || !passing.Temporary() {
				return err
			}

			// Five milliseconds, then twice as long each time up to a
			// second: soon enough to be unnoticed when it passes at once, and
			// not a loop that spins on a process that stays full.
			wait = min(max(2*wait, 5*time.Millisecond), time.Second)
			s.log().Warn("ldap: an accept failed, and is asked again", slog.Any("err", err), slog.Duration("in", wait))
			time.Sleep(wait)

			continue
		}
		wait = 0

		from, ok := s.admit(nc)
		if !ok {
			// With nothing said. Before a TLS handshake too, where the
			// listener is LDAPS: the handshake is at the first read, and this
			// connection is not read.
			_ = nc.Close()

			continue
		}
		go func() {
			defer s.leave(from)
			s.ServeConn(nc)
		}()
	}
}

// admit counts a connection in, or says it is one too many.
func (s *Server) admit(nc net.Conn) (from netip.Addr, ok bool) {
	from = origin(nc.RemoteAddr())

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.open >= MaxConns || s.from[from] >= MaxConnsPerAddr {
		// Said once a minute and not once a connection: what turns one away
		// turns away the thousand after it.
		s.refused++
		if now := time.Now(); now.Sub(s.said) >= time.Minute {
			s.log().Warn("ldap: connections turned away, there being as many as there may be",
				slog.Int("turned_away", s.refused), slog.String("last_from", from.String()),
				slog.Int("open", s.open), slog.Int("open_from_there", s.from[from]))
			s.refused, s.said = 0, now
		}

		return from, false
	}
	if s.from == nil {
		s.from = map[netip.Addr]int{}
	}
	s.open++
	s.from[from]++

	return from, true
}

func (s *Server) leave(from netip.Addr) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.open--
	if s.from[from]--; s.from[from] == 0 {
		delete(s.from, from)
	}
}

// origin is what a connection is counted as coming from: its address without
// the port, and for an IPv6 address the /64 it is in, since a host there has
// the whole of that to connect from and would otherwise be as many clients
// as it liked. A connection that is not TCP's is from nowhere, with every
// other such.
func origin(a net.Addr) netip.Addr {
	tcp, ok := a.(*net.TCPAddr)
	if !ok {
		return netip.Addr{}
	}
	ip, ok := netip.AddrFromSlice(tcp.IP)
	if !ok {
		return netip.Addr{}
	}
	ip = ip.Unmap()
	if ip.Is6() {
		if around, err := ip.Prefix(64); err == nil {
			return around.Addr()
		}
	}

	return ip
}
