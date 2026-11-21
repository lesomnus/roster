package arrives

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

// What a deployment may write in `private_hosts`, and what each entry names: a
// name is that host and whatever it resolves to, an address is itself, and a
// network is every address in it. Nothing else is read, and nothing named is
// nothing reached.
func TestAPrivateHostIsANameAnAddressOrANetwork(t *testing.T) {
	x := require.New(t)

	h, err := ParsePrivateHosts([]string{"IdP.Corp.Example.", " 10.20.0.5 ", "10.30.0.0/16", "::1", "fd00:20::/32"})
	x.NoError(err)

	for _, c := range []struct {
		host, ip string
		want     bool
	}{
		// The name, however it is spelled and whatever it came to.
		{"idp.corp.example", "10.99.0.1", true},
		{"IDP.corp.example.", "192.168.4.4", true},
		{"idp.corp.example", "127.0.0.1", true},

		// And no name beside it: not one under it, and not one that only
		// came to the same address.
		{"login.idp.corp.example", "10.99.0.1", false},
		{"corp.example", "10.99.0.1", false},
		{"elsewhere.example", "10.99.0.1", false},

		// An address is itself, by any name and by none.
		{"10.20.0.5", "10.20.0.5", true},
		{"elsewhere.example", "10.20.0.5", true},
		{"elsewhere.example", "::ffff:10.20.0.5", true},
		{"10.20.0.6", "10.20.0.6", false},

		// A network is everything in it and nothing beside it.
		{"elsewhere.example", "10.30.200.7", true},
		{"elsewhere.example", "10.31.0.1", false},
		{"elsewhere.example", "fd00:20::9", true},
		{"elsewhere.example", "fd00:21::9", false},
		{"localhost", "::1", true},
		{"localhost", "127.0.0.1", false},
	} {
		x.Equal(c.want, h.holds(c.host, netip.MustParseAddr(c.ip)), "%s at %s", c.host, c.ip)
	}

	// A deployment that said nothing named nothing, and a front door of its
	// own reaches the internet and no further.
	var none PrivateHosts
	x.False(none.holds("localhost", netip.MustParseAddr("127.0.0.1")))
	p := New(nil, nil)
	x.True(p.reaches("login.example", netip.MustParseAddr("20.190.160.1")))
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "fd00:ec2::254"} {
		x.False(p.reaches("login.example", netip.MustParseAddr(ip)), ip)
	}

	// And what it names after it was made is what is asked, as its GitHub is.
	p.PrivateHosts = h
	x.True(p.reaches("idp.corp.example", netip.MustParseAddr("10.99.0.1")))
	x.False(p.reaches("login.example", netip.MustParseAddr("169.254.169.254")))

	// What is not a host is refused where it is written, not read as
	// something else: a URL, a host with its port, a pattern, nothing at all.
	for _, v := range []string{
		"https://idp.corp.example", "idp.corp.example:8443", "idp.corp.example/realms/x",
		"*.corp.example", "[::1]", "10.0.0.0/33", "idp corp", "", " ",
	} {
		_, err := ParsePrivateHosts([]string{v})
		x.Error(err, "%q was taken for a host", v)
	}
}
