package arrives

import (
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/roster/internal/idptest"
)

// Two directories, the way Microsoft writes theirs.
const (
	contosoTid  = "72f988bf-86f1-41af-91ab-2d7cd011db47"
	fabrikamTid = "1a2b3c4d-5e6f-4081-92a3-b4c5d6e7f809"
)

// Which issuers are an authority of more than one directory: the two words
// Microsoft uses for one, as the last segment before `v2.0`, and nothing
// else.
func TestAnAuthorityOfMoreThanOneDirectoryIsSaidByItsIssuer(t *testing.T) {
	x := require.New(t)

	x.Equal("https://login.microsoftonline.com/{tenantid}/v2.0", anyDirectory("https://login.microsoftonline.com/common/v2.0"))
	x.Equal("https://login.microsoftonline.com/{tenantid}/v2.0", anyDirectory("https://login.microsoftonline.com/organizations/v2.0/"))

	// Personal accounts only is one directory, whose document names it.
	x.Empty(anyDirectory("https://login.microsoftonline.com/consumers/v2.0"))
	x.Empty(anyDirectory("https://login.microsoftonline.com/" + contosoTid + "/v2.0"))
	x.Empty(anyDirectory("https://login.microsoftonline.com/common"))
	x.Empty(anyDirectory("https://accounts.google.com"))
	x.Empty(anyDirectory("/common/v2.0"))
}

// The document is accepted with the template in it only where the issuer is
// an authority of more than one directory, and the template is then what
// every token is held to; anywhere else the issuer is the issuer.
func TestDiscoveryAcceptsMicrosoftsTemplateAndNothingElse(t *testing.T) {
	ctx := t.Context()
	idp := idptest.New(t, "app")

	for _, word := range []string{"common", "organizations"} {
		v, err := discoverAt(ctx, idp.URL+"/"+word+"/v2.0")
		require.NoError(t, err, word)
		require.Equal(t, idp.URL+"/{tenantid}/v2.0", directoriesOf(idp.URL+"/"+word+"/v2.0", v), word)
	}

	// `consumers` names one directory, which is not the template: a mismatch
	// like any other, refused as it was.
	_, err := discoverAt(ctx, idp.URL+"/consumers/v2.0")
	var mismatch *oidc.IssuerMismatchError
	require.ErrorAs(t, err, &mismatch)

	// Which is why personal accounts are that directory's issuer, and that is
	// one directory.
	v, err := discoverAt(ctx, idp.URL+"/"+idptest.Consumers+"/v2.0")
	require.NoError(t, err)
	require.Empty(t, directoriesOf(idp.URL+"/"+idptest.Consumers+"/v2.0", v))

	v, err = discoverAt(ctx, idp.URL)
	require.NoError(t, err)
	require.Empty(t, directoriesOf(idp.URL, v))
}

// TestATokenIsFromTheDirectoryItSays: with the issuer check off for an
// authority of more than one directory, what stands in for it is that the
// token's `iss` is the template with the token's own `tid` in it.
func TestATokenIsFromTheDirectoryItSays(t *testing.T) {
	ctx := t.Context()
	idp := idptest.New(t, "app")
	v, err := discoverAt(ctx, idp.URL+"/common/v2.0")
	require.NoError(t, err)
	template := directoriesOf(idp.URL+"/common/v2.0", v)
	verifier := v.Verifier(&oidc.Config{ClientID: "app", SkipIssuerCheck: true})

	check := func(claims map[string]any) error {
		t.Helper()
		token := map[string]any{"aud": "app", "sub": "AAAA", "exp": 4102444800, "iat": 1700000000}
		for k, v := range claims {
			token[k] = v
		}
		id, err := verifier.Verify(ctx, idp.Sign(t, token))
		require.NoError(t, err)

		return inDirectory(id, template)
	}

	require.NoError(t, check(map[string]any{"iss": idp.URL + "/" + contosoTid + "/v2.0", "tid": contosoTid}))
	require.NoError(t, check(map[string]any{"iss": idp.URL + "/" + idptest.Consumers + "/v2.0", "tid": idptest.Consumers}))

	err = check(map[string]any{"iss": idp.URL + "/" + fabrikamTid + "/v2.0", "tid": contosoTid})
	require.ErrorContains(t, err, "says it is from directory")
	err = check(map[string]any{"iss": idp.URL + "/" + contosoTid + "/v2.0"})
	require.ErrorContains(t, err, "names no directory")
	err = check(map[string]any{"iss": idp.URL + "/x/v2.0", "tid": "x"})
	require.ErrorContains(t, err, "names no directory")
	err = check(map[string]any{"iss": "https://elsewhere.example/" + contosoTid + "/v2.0", "tid": contosoTid})
	require.ErrorContains(t, err, "says it is from directory")
}

// Microsoft sends `xms_edov` as `true` in one token and as `"1"` in another,
// and its absence is no.
func TestMicrosoftsWordForAnAddressIsReadInBothItsSpellings(t *testing.T) {
	for _, c := range []struct {
		v    any
		want bool
	}{
		{true, true},
		{"1", true},
		{"true", true},
		{"True", true},
		{float64(1), true},

		{nil, false},
		{false, false},
		{"0", false},
		{"false", false},
		{"", false},
		{"yes", false},
		{float64(0), false},
		{map[string]any{}, false},
	} {
		require.Equal(t, c.want, domainVerified(c.v), "%#v", c.v)
	}
}

func TestADirectoryIsAGuid(t *testing.T) {
	x := require.New(t)
	x.True(guid(contosoTid))
	x.True(guid(idptest.Consumers))
	x.True(guid("72F988BF-86F1-41AF-91AB-2D7CD011DB47"))

	x.False(guid(""))
	x.False(guid("common"))
	x.False(guid("72f988bf86f141af91ab2d7cd011db47"))
	x.False(guid("{72f988bf-86f1-41af-91ab-2d7cd011db4}"))
	x.False(guid("72f988bf-86f1-41af-91ab-2d7cd011db4g"))
	x.False(guid("72f988bf-86f1-41af-91ab/2d7cd011db47"))
}
