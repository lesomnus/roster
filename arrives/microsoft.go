package arrives

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Any Microsoft account, through one connection.
//
// # Why the issuer is not the issuer
//
// A connection names one issuer, and discovery refuses a document whose
// `issuer` is not the URL it was fetched from. That is right for one Entra
// directory, `https://login.microsoftonline.com/<tenant>/v2.0`. *Sign in with
// Microsoft* for anybody is Microsoft's authority for more than one directory:
// `common` (any work, school or personal account) or `organizations` (any
// work or school one). Its document says
// `https://login.microsoftonline.com/{tenantid}/v2.0`, placeholder and all,
// and each token names the directory its person is in, as `iss` and as `tid`.
//
// So that one difference is accepted, and only that one: the document's
// issuer is the connection's with `{tenantid}` where it says `common` or
// `organizations`. Each token is then held to the template with its own `tid`
// in it, which is how Microsoft says a token from such an authority is
// checked. `consumers`, personal accounts only, is not this shape -- its
// document names the one directory personal accounts are all in -- and is
// written as that directory's issuer instead.
//
// # What an address from Microsoft is worth
//
// Microsoft sends no `email_verified`. What it sends instead, when the app
// registration asks for it, is `xms_edov`: that the address's domain is one
// the person's directory has proved it owns. Without it, any directory's
// administrator may put any address on their own users, and a relying party
// that reads the bare `email` of a token from any directory hands out
// whichever account carries it -- the mistake called nOAuth. So an address is
// verified on `email_verified` or on `xms_edov`, the provider's word in either
// language, and on nothing else ([domainVerified]).
//
// And `xms_edov` only where Microsoft is the provider. It is Microsoft's word
// in Microsoft's token; in anybody else's it is a claim whatever put it there
// meant by it, which may be a user's own attribute mapped into the token. A
// connection to any other issuer is believed on `email_verified` alone
// ([Providers.microsoft]).

// tenantID is the placeholder Microsoft's document puts where a directory goes.
const tenantID = "{tenantid}"

// microsoftHosts are where the Microsoft identity platform signs people in and
// issues their tokens: its global cloud, and the national clouds of the United
// States government and of China.
var microsoftHosts = []string{
	"login.microsoftonline.com",
	"login.microsoftonline.us",
	"login.partner.microsoftonline.cn",
	"login.chinacloudapi.cn",
}

// microsoft is whether an issuer is the Microsoft identity platform's, which is
// what decides whether a token's `xms_edov` is anybody's word.
//
// By the host and over https, the way [Providers.gitHub] reads GitHub off an
// issuer: a path is the issuer's to choose, and an issuer anybody stands up
// may mirror Microsoft's paths -- the fake in `internal/idptest` does.
func (p *Providers) microsoft(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return false
	}
	if p.Microsoft != "" {
		at, err := url.Parse(p.Microsoft)

		return err == nil && at.Scheme == u.Scheme && strings.EqualFold(at.Host, u.Host)
	}

	return u.Scheme == "https" && slices.ContainsFunc(microsoftHosts, func(h string) bool { return strings.EqualFold(h, u.Host) })
}

// anyDirectory is the issuer Microsoft's document names for an authority of
// more than one directory, and empty for an issuer that is not one.
func anyDirectory(issuer string) string {
	at := strings.TrimSuffix(issuer, "/")
	for _, word := range []string{"/common/v2.0", "/organizations/v2.0"} {
		if base, ok := strings.CutSuffix(at, word); ok && strings.Contains(base, "://") {
			return base + "/" + tenantID + "/v2.0"
		}
	}

	return ""
}

// discoverAt is what an issuer publishes, accepting the one difference between
// the connection's issuer and its document's that is Microsoft's design rather
// than a mistake.
func discoverAt(ctx context.Context, issuer string) (*oidc.Provider, error) {
	v, err := oidc.NewProvider(ctx, issuer)
	var mismatch *oidc.IssuerMismatchError
	if !errors.As(err, &mismatch) {
		return v, err
	}
	want := anyDirectory(issuer)
	if want == "" || mismatch.Discovered != want {
		return nil, err
	}

	// Told which issuer to expect, go-oidc checks nothing about the document
	// it fetches, so the second read is held to what the first one said.
	v, err = oidc.NewProvider(oidc.InsecureIssuerURLContext(ctx, want), issuer)
	if err != nil {
		return nil, err
	}
	if documented(v) != want {
		return nil, fmt.Errorf("the document at %s named another issuer when it was read again", issuer)
	}

	return v, nil
}

// directoriesOf is the template a connection's tokens are held to, when its
// issuer is an authority of more than one directory, and empty otherwise.
func directoriesOf(issuer string, v *oidc.Provider) string {
	t := documented(v)
	if t == strings.TrimSuffix(issuer, "/") || t != anyDirectory(issuer) {
		return ""
	}

	return t
}

// documented is the issuer a provider's document says.
func documented(v *oidc.Provider) string {
	var doc struct {
		Issuer string `json:"issuer"`
	}
	if v.Claims(&doc) != nil {
		return ""
	}

	return doc.Issuer
}

// inDirectory holds a token from an authority of more than one directory to
// the directory it says it is from: its `iss` is the template with its own
// `tid` in it.
func inDirectory(id *oidc.IDToken, template string) error {
	var claims struct {
		Tid string `json:"tid"`
	}
	if err := id.Claims(&claims); err != nil {
		return err
	}
	if !guid(claims.Tid) {
		return errors.New("the token names no directory it is from (`tid`)")
	}
	if want := strings.Replace(template, tenantID, claims.Tid, 1); id.Issuer != want {
		return fmt.Errorf("the token is from %q and says it is from directory %s, whose issuer is %q", id.Issuer, claims.Tid, want)
	}

	return nil
}

// guid is whether a string is a directory's identifier as Microsoft writes
// one: 8-4-4-4-12 hexadecimal digits.
func guid(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, r := range v {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
				return false
			}
		}
	}

	return true
}

// domainVerified is Microsoft's `xms_edov`, which arrives as `true` in one
// token and as `"1"` in another; anything else, and its absence, is no.
func domainVerified(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case string:
		return v == "1" || strings.EqualFold(v, "true")
	case float64:
		return v == 1
	}

	return false
}
