package arrives

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// What a sign-in says about somebody, kept where their profile has nothing.
//
// # Why at a sign-in
//
// It is the one moment anything here holds what a directory says about
// somebody, and for a picture the one moment anything can fetch it. Entra hands
// its photo only to a caller with a token for Microsoft Graph, and the token a
// sign-in is handed is used once and dropped: keeping it means `offline_access`
// and a refresh token in a database, which is a credential to the directory
// held for the sake of a photograph. So the front door that has the token
// reads the picture, and roster keeps it (`Holder.portrait`).
//
// # Why only the blanks
//
// `HolderService.Fill` is the reason, and the grant. A directory fills in what
// nobody has said, and what somebody has said stays: a name they corrected, a
// picture they chose. Somebody who wants the directory's again empties the
// field, and their next sign-in fills it.

// fillTimeout bounds the whole of [Providers.Fill], which runs inside somebody's
// sign-in.
const fillTimeout = 10 * time.Second

// maxPicture is the most a picture may weigh. It is roster's limit
// (`HolderPortrayRequest.image`), so one larger is not fetched only to be
// refused.
const maxPicture = 2 << 20

// maxPictureURL is the longest `profile.picture` roster keeps.
const maxPictureURL = 2048

// Fill gives somebody's profile what their tenant says to fill it with, where
// it has nothing (`TenantProfile`): nothing at all for a tenant that did not
// say `fill`; the provider's word -- the token's `name` as their display name,
// and their picture -- by default; and Slack's for a tenant that names a
// workspace (`slack.go`).
//
// Asked at every sign-in through a provider, so the tenant's decision is read
// where it is made, and taking effect is the next sign-in rather than the
// front door's next restart. Then two calls to roster at most, and usually
// one that writes nothing: the first fills the name and answers whether they
// are pictureless, which is the one thing worth knowing before asking a
// directory for a photograph.
//
// It never decides a sign-in. What it answers is for the app to log: the
// person is signed in either way, and their next sign-in tries again.
func (p *Providers) Fill(ctx context.Context, holder pdid.Id, who Caller) error {
	ctx, cancel := context.WithTimeout(ctx, fillTimeout)
	defer cancel()

	t, err := p.roster.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
		Ref:    rstr.TenantRef_builder{Id: who.Tenant.Bytes()}.Build(),
		Select: rstr.TenantSelect_builder{Config: proto.Bool(true)}.Build(),
	}.Build())
	if err != nil {
		return err
	}
	how := t.GetConfig().GetProfile()
	if !how.GetFill() {
		return nil
	}

	ref := rstr.HolderRef_builder{Id: holder.Bytes()}.Build()
	if at := how.GetSlackSecretRef(); at != "" {
		// The deployment's secret, named by the deployment (`server/core`
		// refuses a tenant writing one), and read the way a connection's is.
		if p.secret == nil {
			return errors.New("slack: this front door was given no way to read a secret reference")
		}
		token, err := p.secret(at)
		if err != nil {
			return fmt.Errorf("slack: %w", err)
		}

		return p.fillFromSlack(ctx, ref, token, who)
	}

	res, err := p.roster.Holder().Fill(ctx, rstr.HolderFillRequest_builder{Ref: ref, DisplayName: who.Name}.Build())
	if err != nil || !res.GetPictureless() {
		return err
	}

	_, v, err := p.discover(ctx, who.Tenant, who.Provider)
	if err != nil {
		return err
	}
	img, where, err := p.picture(ctx, v, who)
	if err != nil || (len(img) == 0 && where == "") {
		return err
	}
	_, err = p.roster.Holder().Fill(ctx, rstr.HolderFillRequest_builder{Ref: ref, Picture: where, Image: img}.Build())
	if status.Code(err) == codes.InvalidArgument && where != "" {
		// An image roster will not keep, from somewhere a browser fetches as
		// it is: the URL alone is still their picture.
		_, err = p.roster.Holder().Fill(ctx, rstr.HolderFillRequest_builder{Ref: ref, Picture: where}.Build())
	}

	return err
}

// picture is somebody's picture, and where it is when a browser can fetch it
// there too.
//
// Where comes from the token's `picture`, and from userinfo when the token
// said nothing -- which is Entra's way. A picture a person does not have is
// nothing and no error.
//
// # Where the token goes
//
// To the provider's own API, and nowhere else: a picture on the host its
// userinfo answers from is fetched with the token, as Entra's must be, and is
// never a URL roster keeps, since no browser has that token. Anywhere else is
// fetched without it, over https, and only from an address on the internet --
// a `picture` claim is a URL somebody else wrote, and this process can reach
// what its author cannot.
func (p *Providers) picture(ctx context.Context, v *oidc.Provider, who Caller) ([]byte, string, error) {
	var err error
	at := who.Picture
	if at == "" {
		if at, err = p.userinfo(ctx, v, who); err != nil || at == "" {
			return nil, "", err
		}
	}
	u, err := url.Parse(at)
	if err != nil || u.Host == "" {
		return nil, "", fmt.Errorf("picture %q: not a URL", at)
	}

	if own(u, v.UserInfoEndpoint()) {
		img, err := get(ctx, p.own, u, who.token)

		return img, "", err
	}
	if u.Scheme != "https" {
		return nil, "", nil
	}

	img, err := get(ctx, p.anywhere, u, nil)
	if err != nil || len(img) == 0 {
		return nil, "", err
	}
	if len(at) > maxPictureURL || u.User != nil {
		return img, "", nil
	}

	return img, at, nil
}

// userinfo is the `picture` the provider's userinfo answers with, asked with
// the token the sign-in was handed.
func (p *Providers) userinfo(ctx context.Context, v *oidc.Provider, who Caller) (string, error) {
	if v.UserInfoEndpoint() == "" || who.token == nil {
		return "", nil
	}

	info, err := v.UserInfo(oidc.ClientContext(ctx, p.own), oauth2.StaticTokenSource(who.token))
	if err != nil {
		return "", fmt.Errorf("userinfo: %w", err)
	}

	// OpenID Connect Core 5.3.2: an answer about anybody but the token's
	// subject is not used. A provider that answered with somebody else has
	// a defect, and the picture of the wrong person is what it would cost.
	if info.Subject != who.Subject {
		return "", errors.New("userinfo: answered about somebody other than who signed in")
	}

	var claims struct {
		Picture string `json:"picture"`
	}
	if err := info.Claims(&claims); err != nil {
		return "", fmt.Errorf("userinfo: %w", err)
	}

	return claims.Picture, nil
}

// own is whether a URL is on the provider's own API, which is where its
// userinfo is.
func own(u *url.URL, userinfo string) bool {
	w, err := url.Parse(userinfo)

	return err == nil && w.Host != "" && u.Scheme == w.Scheme && strings.EqualFold(u.Host, w.Host)
}

// get is one picture, as a browser showing it would fetch it. A 404 is
// somebody with no picture, which is not an error.
func get(ctx context.Context, c *http.Client, u *url.URL, tok *oauth2.Token) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/jpeg, image/png, image/webp, image/gif")
	if tok != nil {
		tok.SetAuthHeader(req)
	}

	res, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("picture: %w", err)
	}
	defer res.Body.Close()

	switch {
	case res.StatusCode == http.StatusNotFound:
		return nil, nil
	case tok != nil && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden):
		// The provider refusing its own token for its own picture is a
		// connection asking for too little, and worth saying in those words:
		// the status alone reads like a fault at the provider.
		return nil, fmt.Errorf("picture at %s: %s; does the connection ask for the scope its pictures need? Entra's is User.Read", u.Host, res.Status)
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("picture at %s: %s", u.Host, res.Status)
	}

	b, err := io.ReadAll(io.LimitReader(res.Body, maxPicture+1))
	if err != nil {
		return nil, fmt.Errorf("picture at %s: %w", u.Host, err)
	}
	if len(b) > maxPicture {
		return nil, fmt.Errorf("picture at %s: larger than %d bytes", u.Host, maxPicture)
	}

	return b, nil
}

// ownClient fetches from the provider's own API, with its token: wherever the
// deployment's `Connection` says the provider is, which may be inside its own
// network -- a directory on the premises -- because the deployment named it.
// It follows no redirect off that host, since the token goes with every hop.
func ownClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 3 || r.URL.Scheme != via[0].URL.Scheme || !strings.EqualFold(r.URL.Host, via[0].URL.Host) {
				return errors.New("a redirect off the provider's own host")
			}

			return nil
		},
	}
}

// publicClient fetches from wherever a `picture` claim pointed: over https, and
// only from an address `allow` says is on the internet.
//
// The address is checked where the connection is made, so it is the one
// actually dialled -- after every redirect, and after whatever DNS answered,
// including an answer that changed since anybody last looked. And no proxy,
// because a proxy is a host this process can reach and a claim's author cannot,
// which is the thing being refused.
func publicClient(allow func(netip.Addr) bool) *http.Client {
	d := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !allow(ip) {
				return fmt.Errorf("%s is not an address on the internet", host)
			}

			return nil
		},
	}

	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext:         d.DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
			ForceAttemptHTTP2:   true,
		},
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 3 || r.URL.Scheme != "https" {
				return errors.New("a redirect away from https, or one too many")
			}

			return nil
		},
	}
}

// Ranges a public address is not in, beside what [netip.Addr] already knows.
var (
	// The space a carrier puts its subscribers behind (RFC 6598): private in
	// all but name.
	shared = netip.MustParsePrefix("100.64.0.0/10")

	// "This network" (RFC 1122), which some systems connect to themselves.
	here = netip.MustParsePrefix("0.0.0.0/8")
)

// public is whether an address is one on the internet: not this machine, not a
// private network, not a link, not a carrier's shared space.
func public(ip netip.Addr) bool {
	ip = ip.Unmap()

	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !shared.Contains(ip) && !here.Contains(ip)
}
