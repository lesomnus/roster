package arrives

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/lesomnus/roster/internal/idptest"
)

// A connection is GitHub's when its issuer is, and nothing else makes it so:
// not its name, which is the operator's, and not a path on GitHub's host.
func TestAConnectionIsGitHubsWhenItsIssuerIs(t *testing.T) {
	x := require.New(t)

	p := &Providers{}
	x.Equal("https://api.github.com", p.gitHub("https://github.com"))
	x.Equal("https://api.github.com", p.gitHub("https://github.com/"))
	x.Empty(p.gitHub("https://github.com/login/oauth"))
	x.Empty(p.gitHub("https://accounts.google.com"))
	x.Empty(p.gitHub("https://github.example.com"))

	// A deployment on GitHub Enterprise Server names its own, whose API is
	// beside it -- and github.com is then not this front door's GitHub.
	p = &Providers{GitHub: "https://github.example.com/"}
	x.Equal("https://github.example.com/api/v3", p.gitHub("https://github.example.com"))
	x.Empty(p.gitHub("https://github.com"))
}

// What a GitHub connection asks for: the addresses always, as an OpenID one
// asks for `openid`, and once.
func TestAGitHubConnectionAsksForTheAddresses(t *testing.T) {
	x := require.New(t)

	cfg := gitHubRelying("https://github.com", "the-app", "", "https://account.example/callback", nil)
	x.Equal([]string{"user:email"}, cfg.Scopes)
	x.Equal("https://github.com/login/oauth/authorize", cfg.Endpoint.AuthURL)
	x.Equal("https://github.com/login/oauth/access_token", cfg.Endpoint.TokenURL)
	x.Equal(oauth2.AuthStyleInParams, cfg.Endpoint.AuthStyle, "the client's credentials go where GitHub says, and are not probed for")

	cfg = gitHubRelying("https://github.com", "the-app", "", "", []string{"read:org"})
	x.Equal([]string{"user:email", "read:org"}, cfg.Scopes)

	cfg = gitHubRelying("https://github.com", "the-app", "", "", []string{"read:org", "user:email"})
	x.Equal([]string{"read:org", "user:email"}, cfg.Scopes)
}

// TestSomebodyArrivesFromGitHub is the exchange and the two questions that
// replace an ID token, against a GitHub that answers the way GitHub does.
func TestSomebodyArrivesFromGitHub(t *testing.T) {
	ctx := t.Context()
	g := idptest.NewGitHub(t, "the-app", "the-secret")
	p := &Providers{own: ownClient(), anywhere: publicClient(public), GitHub: g.URL}

	cfg := gitHubRelying(g.URL, "the-app", "the-secret", "https://account.example/callback", nil)
	verifier := &Verifier{gitHub: p.gitHub(g.URL)}
	claim := func() (Caller, error) {
		t.Helper()

		return p.Claim(ctx, cfg, verifier, "the-code")
	}

	octocat := func() {
		g.User = map[string]any{
			"id": 583231, "login": "octocat", "name": "The Octocat",
			"avatar_url": g.URL + "/avatars/u/583231",
		}
		g.Emails = []map[string]any{
			{"email": "octo@example.com", "primary": false, "verified": true},
			{"email": "octocat@github.example", "primary": true, "verified": true},
		}
	}

	t.Run("by the account's id, named, at its primary address", func(t *testing.T) {
		x := require.New(t)
		octocat()

		who, err := claim()
		x.NoError(err)
		x.Equal("583231", who.Subject)
		x.Equal("The Octocat", who.Name)
		x.Equal("octocat@github.example", who.Email)
		x.True(who.Verified)
		x.Equal(g.URL+"/avatars/u/583231", who.Picture)
		x.Empty(who.sub, "there is no ID token whose `sub` this could have been keyed by")

		// The secret went in the form, to GitHub, and the token to its API.
		x.Equal("the-secret", g.Sent().Get("client_secret"))
		x.Equal([]string{
			"/api/v3/user Bearer " + idptest.GitHubToken,
			"/api/v3/user/emails Bearer " + idptest.GitHubToken,
		}, g.Calls()[len(g.Calls())-2:])
	})

	// A username is changed and then taken by somebody else; the id is not.
	t.Run("the same id is the same person whatever their login", func(t *testing.T) {
		x := require.New(t)
		octocat()
		g.User["login"] = "octocat-renamed"

		who, err := claim()
		x.NoError(err)
		x.Equal("583231", who.Subject)
	})

	t.Run("an account with no name is called by its login", func(t *testing.T) {
		x := require.New(t)
		octocat()
		g.User["name"] = nil

		who, err := claim()
		x.NoError(err)
		x.Equal("octocat", who.Name)
	})

	// The condition an invitation by address is read under: GitHub's word,
	// and nothing it did not say.
	t.Run("an address GitHub has not verified is not verified", func(t *testing.T) {
		x := require.New(t)
		octocat()
		g.Emails[1]["verified"] = false

		who, err := claim()
		x.NoError(err)
		x.Equal("octocat@github.example", who.Email)
		x.False(who.Verified)
	})

	t.Run("addresses GitHub will not say are none, and not a refusal", func(t *testing.T) {
		x := require.New(t)
		octocat()
		g.Emails = nil

		who, err := claim()
		x.NoError(err)
		x.Equal("583231", who.Subject)
		x.Empty(who.Email)
		x.False(who.Verified)
	})

	t.Run("an answer with no id is nobody", func(t *testing.T) {
		x := require.New(t)
		octocat()
		delete(g.User, "id")

		_, err := claim()
		x.ErrorContains(err, "no id")
	})

	// GitHub answers a code it did not hand out with a 200 whose body is an
	// error. Read by its status, that is a sign-in with no token.
	t.Run("a code GitHub did not hand out is refused, though it answered 200", func(t *testing.T) {
		x := require.New(t)
		octocat()

		_, err := p.Claim(ctx, cfg, verifier, "not-the-code")
		x.ErrorContains(err, "bad_verification_code")

		wrong := gitHubRelying(g.URL, "the-app", "not-the-secret", "", nil)
		_, err = p.Claim(ctx, wrong, verifier, "the-code")
		x.ErrorContains(err, "incorrect_client_credentials")
	})

	// The picture is on the provider's own host here, as GitHub Enterprise
	// Server keeps it: fetched with the token, and its URL not kept, since
	// no browser holds that token. github.com's is on a host of its own,
	// which is the public fetch `fill_test.go` is about.
	t.Run("a picture on GitHub's own host is fetched with the token", func(t *testing.T) {
		x := require.New(t)
		g.Avatar = photo(t)

		img, where, err := p.pictureAt(ctx, g.URL+"/avatars/u/583231", p.gitHub(g.URL),
			&oauth2.Token{AccessToken: idptest.GitHubToken, TokenType: "bearer"})
		x.NoError(err)
		x.Equal(g.Avatar, img)
		x.Empty(where)

		calls := g.Calls()
		x.Equal("/avatars/u/583231 Bearer "+idptest.GitHubToken, calls[len(calls)-1])
	})
}
