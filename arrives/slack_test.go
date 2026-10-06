package arrives

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeSlack is as much of Slack's Web API as a fill asks: who is under an
// address, and the picture they chose, on one host.
func fakeSlack(t *testing.T, img []byte) *httptest.Server {
	t.Helper()
	m := http.NewServeMux()
	var s *httptest.Server
	m.HandleFunc("/users.lookupByEmail", func(w http.ResponseWriter, r *http.Request) {
		answer := func(v any) {
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		switch r.Header.Get("authorization") {
		case "Bearer xoxb-test":
		case "Bearer xoxb-narrow":
			answer(map[string]any{"ok": false, "error": "missing_scope", "needed": "users:read.email", "provided": "users:read"})

			return
		default:
			answer(map[string]any{"ok": false, "error": "invalid_auth"})

			return
		}

		user := func(name string, custom bool, more map[string]any) map[string]any {
			v := map[string]any{"id": "U1", "profile": map[string]any{
				"real_name": name, "display_name": "nickname",
				"image_512": s.URL + "/512.png", "is_custom_image": custom,
			}}
			for k, x := range more {
				v[k] = x
			}

			return map[string]any{"ok": true, "user": v}
		}
		switch r.URL.Query().Get("email") {
		case "erin@contoso.example":
			answer(user("Erin Hart", true, nil))
		case "dana@contoso.example":
			answer(user("Dana Kim", false, nil))
		case "gone@contoso.example":
			answer(user("Gone", true, map[string]any{"deleted": true}))
		case "bot@contoso.example":
			answer(user("Bot", true, map[string]any{"is_bot": true}))
		default:
			answer(map[string]any{"ok": false, "error": "users_not_found"})
		}
	})
	m.HandleFunc("/512.png", func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("authorization"), "the workspace's token went with a picture")
		_, _ = w.Write(img)
	})
	s = httptest.NewServer(m)
	t.Cleanup(s.Close)

	return s
}

// Who Slack has under an address, and what of them a profile wants: the real
// name rather than the handle, a picture only when they chose one, and nobody
// at all for a deactivated account or a bot.
func TestSlackIsAskedForSomebodyByTheirAddress(t *testing.T) {
	ctx := t.Context()
	s := fakeSlack(t, photo(t))
	p := &Providers{own: ownClient(), anywhere: publicClient(public), SlackAPI: s.URL}

	t.Run("somebody, and the picture they chose", func(t *testing.T) {
		x := require.New(t)
		v, err := p.slackUser(ctx, "xoxb-test", "erin@contoso.example")
		x.NoError(err)
		x.Equal("Erin Hart", v.name, "the handle, where the name was asked for")
		x.Equal(s.URL+"/512.png", v.image)
	})
	t.Run("and Slack's placeholder is nobody's picture", func(t *testing.T) {
		x := require.New(t)
		v, err := p.slackUser(ctx, "xoxb-test", "dana@contoso.example")
		x.NoError(err)
		x.Equal("Dana Kim", v.name)
		x.Empty(v.image)
	})
	t.Run("and nobody is nobody", func(t *testing.T) {
		for _, email := range []string{"gone@contoso.example", "bot@contoso.example", "stranger@contoso.example"} {
			v, err := p.slackUser(ctx, "xoxb-test", email)
			require.NoError(t, err, email)
			require.Nil(t, v, email)
		}
	})
	t.Run("and a token short of a scope says which", func(t *testing.T) {
		_, err := p.slackUser(ctx, "xoxb-narrow", "erin@contoso.example")
		require.ErrorContains(t, err, "users:read.email")
	})
}

// A picture on the workspace's own API host is fetched as the API is and not
// kept as a URL; Slack's own pictures are somewhere else, fetched only over
// https from the internet, and kept where a browser can fetch them too.
func TestSlacksPictureIsFetchedTheWayAClaimsIs(t *testing.T) {
	ctx := t.Context()
	img := photo(t)

	t.Run("on the API's host", func(t *testing.T) {
		x := require.New(t)
		s := fakeSlack(t, img)
		p := &Providers{own: ownClient(), anywhere: publicClient(public), SlackAPI: s.URL}

		got, where, err := p.slackPicture(ctx, s.URL+"/512.png")
		x.NoError(err)
		x.Equal(img, got)
		x.Empty(where)
	})

	t.Run("where Slack keeps them", func(t *testing.T) {
		x := require.New(t)
		cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			x.Empty(r.Header.Get("authorization"))
			_, _ = w.Write(img)
		}))
		t.Cleanup(cdn.Close)

		// The test's own client, which trusts the test's certificate: the
		// refusal of an address inside the network is `fill_test.go`'s.
		p := &Providers{own: ownClient(), anywhere: cdn.Client(), SlackAPI: "https://slack.example/api"}
		got, where, err := p.slackPicture(ctx, cdn.URL+"/T1-U1-512.png")
		x.NoError(err)
		x.Equal(img, got)
		x.Equal(cdn.URL+"/T1-U1-512.png", where)

		got, where, err = p.slackPicture(ctx, "http://example.com/T1-U1-512.png")
		x.NoError(err)
		x.Nil(got, "fetched over plain http")
		x.Empty(where)
	})
}
