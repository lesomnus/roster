package core

import (
	"context"
	"net/url"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pderr"

	app "github.com/lesomnus/roster/rstr"
)

// What a tenant may write as where its people sign in.
//
// `TenantConfig.front_door` is an origin and nothing more, and this is the
// refusal that keeps it one: the front door's routes are the front door's, and
// a path written here would be a second copy of one of them -- `/login` today,
// and whatever the app renames it to tomorrow, kept in step by nobody.
//
// Held on the way in rather than read leniently on the way out. The value is
// read by two things -- the user console, which sends a browser to it, and
// `Vouch.Accept`, which refuses to mint while it is empty -- and a value that
// parses one way for the page and another for the mint is exactly the drift a
// rule on the write prevents.

// configOf is the settings as they are written: the front door trimmed to its
// origin, or refused, and the profile's Slack reference held to being one.
func configOf(c *app.TenantConfig) (*app.TenantConfig, error) {
	if ref := c.GetProfile().GetSlackSecretRef(); ref != "" &&
		(len(ref) > 256 || strings.TrimSpace(ref) != ref || !strings.Contains(ref, ":")) {
		return nil, pderr.Invalidf("config.profile.slack_secret_ref",
			"a reference to where the deployment keeps the token -- `env:NAME` -- and not the token")
	}

	at, err := frontDoor(c.GetFrontDoor())
	if err != nil {
		return nil, err
	}
	if at == c.GetFrontDoor() {
		return c, nil
	}

	out := proto.Clone(c).(*app.TenantConfig)
	out.SetFrontDoor(at)

	return out, nil
}

// maySlack refuses a new Slack reference from anybody narrower than the
// deployment, which `tenant.ext.proto` argues for: what it names is one of the
// deployment's secrets, and the front door hands it to Slack. Keeping the one
// already written, and taking it away, hand nothing to anybody, so a tenant's
// administrator saving their settings with it unchanged is not refused.
func maySlack(ctx context.Context, was, now *app.TenantConfig) error {
	ref := now.GetProfile().GetSlackSecretRef()
	if ref == "" || ref == was.GetProfile().GetSlackSecretRef() || deployment(ctx) {
		return nil
	}

	return status.Error(codes.PermissionDenied,
		"config.profile.slack_secret_ref: names one of the deployment's secrets, which the deployment writes; a tenant may keep it or take it away")
}

// addWithConfig is [configOf] over the other write that carries settings.
func addWithConfig(req *app.TenantAddRequest) (*app.TenantAddRequest, error) {
	if !req.HasConfig() {
		return req, nil
	}
	cfg, err := configOf(req.GetConfig())
	if err != nil {
		return nil, err
	}
	if cfg == req.GetConfig() {
		return req, nil
	}

	out := proto.Clone(req).(*app.TenantAddRequest)
	out.SetConfig(cfg)

	return out, nil
}

// frontDoor is an origin -- scheme, host, a port if the deployment needs one
// -- and empty for none. Anything after the host is refused, and so is a
// scheme a browser would not be sent to.
func frontDoor(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}

	u, err := url.Parse(v)
	switch {
	case err != nil,
		u.Scheme != "http" && u.Scheme != "https",
		u.Host == "", u.User != nil, u.Opaque != "",
		u.Path != "" && u.Path != "/",
		u.RawQuery != "", u.ForceQuery, u.Fragment != "":
		return "", pderr.Invalidf("config.front_door",
			"an origin -- `https://account.contoso.example` -- and nothing after the host")
	}

	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}
