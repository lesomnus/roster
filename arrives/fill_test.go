package arrives

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/lesomnus/roster/internal/idptest"
)

// What a `picture` claim may point this process at. The claim is a URL
// somebody else wrote, and the answer has to be no for everything this process
// can reach and they cannot -- the cloud's metadata service above all.
func TestOnlyAnAddressOnTheInternetIsPublic(t *testing.T) {
	for _, c := range []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"20.190.160.1", true},
		{"2606:4700::1111", true},

		{"127.0.0.1", false},
		{"10.1.62.11", false},
		{"172.16.0.1", false},
		{"192.168.0.1", false},
		{"169.254.169.254", false},
		{"100.64.0.1", false},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"224.0.0.1", false},
		{"255.255.255.255", false},
		{"::1", false},
		{"::", false},
		{"fe80::1", false},
		{"fd00::1", false},
		{"::ffff:127.0.0.1", false},
		{"::ffff:10.0.0.1", false},
	} {
		require.Equal(t, c.want, public(netip.MustParseAddr(c.ip)), c.ip)
	}
}

// And it is the address dialled that is asked about, so a name that resolves
// inside the network is refused like the address would be.
func TestAPictureIsNotFetchedFromInsideTheNetwork(t *testing.T) {
	x := require.New(t)
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a picture was fetched from inside the network")
	}))
	t.Cleanup(s.Close)

	for _, at := range []string{s.URL, "https://localhost:" + must(url.Parse(s.URL)).Port()} {
		u := must(url.Parse(at + "/a.png"))
		_, err := get(t.Context(), publicClient(public), u, nil)
		x.ErrorContains(err, "is not an address on the internet", at)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}

	return v
}

func photo(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewGray(image.Rect(0, 0, 8, 8))))

	return b.Bytes()
}

// The two ways a provider serves a picture, and what is kept of each: the image
// always, and the URL only when a browser could fetch it as it is.
func TestAPictureIsReadTheWayItsProviderServesIt(t *testing.T) {
	ctx := t.Context()
	idp := idptest.New(t, "app")
	idp.Subject = "erin-at-entra"
	v, err := oidc.NewProvider(ctx, idp.URL)
	require.NoError(t, err)

	token := &oauth2.Token{AccessToken: idptest.AccessToken, TokenType: "Bearer"}
	p := &Providers{own: ownClient(), anywhere: publicClient(public)}

	// No `picture` in the token, a URL to one in userinfo, and the picture
	// fetched with the token -- which is why that URL is not kept.
	t.Run("Entra's way", func(t *testing.T) {
		x := require.New(t)
		idp.UserInfo = map[string]any{"picture": idp.URL + "/photo"}
		idp.Photo = photo(t)

		img, where, err := p.picture(ctx, v, Caller{Subject: idp.Subject, token: token})
		x.NoError(err)
		x.Equal(idp.Photo, img)
		x.Empty(where, "a URL only a token can fetch was kept for browsers")
	})

	t.Run("and with no photo, nothing", func(t *testing.T) {
		x := require.New(t)
		idp.Photo = nil

		img, where, err := p.picture(ctx, v, Caller{Subject: idp.Subject, token: token})
		x.NoError(err)
		x.Nil(img)
		x.Empty(where)
	})

	// OpenID Connect Core 5.3.2: userinfo about somebody else is not used.
	t.Run("and userinfo about somebody else is not believed", func(t *testing.T) {
		_, _, err := p.picture(ctx, v, Caller{Subject: "somebody-else", token: token})
		require.ErrorContains(t, err, "somebody other than who signed in")
	})

	t.Run("and a token refused for its own picture says which scope", func(t *testing.T) {
		_, _, err := p.picture(ctx, v, Caller{
			Subject: idp.Subject, Picture: idp.URL + "/photo",
			token: &oauth2.Token{AccessToken: "not-that-one", TokenType: "Bearer"},
		})
		require.ErrorContains(t, err, "User.Read")
	})

	// A `picture` in the token pointing anywhere else: fetched with no token,
	// and its URL kept, since a browser can fetch it there too.
	t.Run("a URL anybody can fetch", func(t *testing.T) {
		x := require.New(t)
		img := photo(t)
		s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			x.Empty(r.Header.Get("authorization"), "the token went to somebody else's host")
			_, _ = w.Write(img)
		}))
		t.Cleanup(s.Close)

		// The test's own client, which trusts the test's certificate and
		// would be refused 127.0.0.1 otherwise: the refusal is the test above,
		// and this one is about what is kept.
		p := &Providers{own: ownClient(), anywhere: s.Client()}

		got, where, err := p.picture(ctx, v, Caller{Subject: idp.Subject, Picture: s.URL + "/erin.png", token: token})
		x.NoError(err)
		x.Equal(img, got)
		x.Equal(s.URL+"/erin.png", where)

		// Not over https is not fetched, since no page served over https
		// would load it either.
		got, where, err = p.picture(ctx, v, Caller{Subject: idp.Subject, Picture: "http://example.com/erin.png", token: token})
		x.NoError(err)
		x.Nil(got)
		x.Empty(where)
	})
}
