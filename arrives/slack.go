package arrives

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rstr "github.com/lesomnus/roster/rstr"
)

// A profile filled from Slack rather than from the directory somebody signs in
// through.
//
// # Why Slack, and why only when somebody signs in
//
// Because it is where a company's people keep a profile up to date: the
// directory has whatever the person who made the account typed, and Slack has
// the photograph its owner chose. So a tenant may name its workspace
// (`TenantProfile.slack_secret_ref`), and its blanks are filled from there
// instead.
//
// The moment is a sign-in for the reason `fill.go` gives -- it is when
// anything here is about somebody -- and the rule is `Fill`'s: blanks only,
// once. Nothing watches Slack afterwards. A person who wants Slack's again
// empties the field, and their next sign-in fills it.
//
// # How somebody is found there
//
// By the address their directory signed them in with, which Slack is asked
// for exactly (`users.lookupByEmail`). That is the directory's word and
// Slack's, about one company's people in one company's workspace -- the
// workspace a tenant names is its own -- and what it decides is a name and a
// picture, not a way in.

// slackAPI is where Slack's Web API is when [Providers.SlackAPI] says nothing.
const slackAPI = "https://slack.com/api"

// slackPerson is what Slack has about somebody that a profile wants.
type slackPerson struct {
	// name is `real_name`: the name they go by, not a handle.
	name string

	// image is a picture they chose, and empty for Slack's own placeholder,
	// which is nobody's picture.
	image string
}

// fillFromSlack is [Providers.Fill] for a tenant whose people keep their
// profile in Slack: what the profile lacks, from the Slack profile under the
// address the person signed in with.
//
// One call to roster first, which fills nothing and answers what is blank, so
// a person with a name and a picture costs Slack nothing -- most sign-ins.
func (p *Providers) fillFromSlack(ctx context.Context, ref *rstr.HolderRef, token string, who Caller) error {
	res, err := p.roster.Holder().Fill(ctx, rstr.HolderFillRequest_builder{Ref: ref}.Build())
	if err != nil || (!res.GetNameless() && !res.GetPictureless()) || who.Email == "" {
		return err
	}

	in, err := p.slackUser(ctx, token, who.Email)
	if err != nil || in == nil {
		return err
	}

	req := rstr.HolderFillRequest_builder{Ref: ref}
	if res.GetNameless() {
		req.DisplayName = in.name
	}
	var missed error
	if res.GetPictureless() && in.image != "" {
		// A picture that could not be had is no reason to leave the name
		// out: it is said, and the next sign-in tries again.
		req.Image, req.Picture, missed = p.slackPicture(ctx, in.image)
	}
	if req.DisplayName == "" && len(req.Image) == 0 && req.Picture == "" {
		return missed
	}

	_, err = p.roster.Holder().Fill(ctx, req.Build())
	if status.Code(err) == codes.InvalidArgument && len(req.Image) > 0 {
		// An image roster will not keep: the rest of what Slack said still
		// is theirs.
		req.Image = nil
		_, err = p.roster.Holder().Fill(ctx, req.Build())
	}

	return errors.Join(err, missed)
}

// slackUser is who Slack has under an address, or nil for nobody: not in the
// workspace, deactivated, or a bot.
func (p *Providers) slackUser(ctx context.Context, token, email string) (*slackPerson, error) {
	at := strings.TrimSuffix(p.slackAPI(), "/") + "/users.lookupByEmail?email=" + url.QueryEscape(email)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, at, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	// The workspace's API is somewhere the deployment named, as a provider's
	// is, so it is asked the way a provider's own API is.
	res, err := p.own.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("slack: users.lookupByEmail: %s", res.Status)
	}

	var body struct {
		Ok     bool   `json:"ok"`
		Error  string `json:"error"`
		Needed string `json:"needed"`
		User   struct {
			Deleted bool `json:"deleted"`
			IsBot   bool `json:"is_bot"`
			Profile struct {
				RealName      string `json:"real_name"`
				Image512      string `json:"image_512"`
				IsCustomImage bool   `json:"is_custom_image"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("slack: users.lookupByEmail: %w", err)
	}

	switch {
	case !body.Ok && body.Error == "users_not_found":
		return nil, nil
	case !body.Ok && body.Needed != "":
		// Said in the words of the fix: the token is the workspace's and so is
		// the scope it lacks.
		return nil, fmt.Errorf("slack: users.lookupByEmail: %s; the token needs %s", body.Error, body.Needed)
	case !body.Ok:
		return nil, fmt.Errorf("slack: users.lookupByEmail: %s", body.Error)
	case body.User.Deleted || body.User.IsBot:
		return nil, nil
	}

	v := &slackPerson{name: strings.TrimSpace(body.User.Profile.RealName)}
	if body.User.Profile.IsCustomImage {
		v.image = body.User.Profile.Image512
	}

	return v, nil
}

// slackPicture is the picture at a URL Slack answered with, and the URL when a
// browser can fetch it there too.
//
// The rule [Providers.picture] holds a `picture` claim to, with the workspace's
// API in the provider's place: on that host it is fetched as the API is, and
// is not a URL roster keeps; anywhere else -- which is where Slack keeps
// pictures -- only over https from an address on the internet, and without the
// token, which is the workspace's and is for its API.
func (p *Providers) slackPicture(ctx context.Context, at string) ([]byte, string, error) {
	u, err := url.Parse(at)
	if err != nil || u.Host == "" {
		return nil, "", fmt.Errorf("slack: picture %q: not a URL", at)
	}
	if own(u, p.slackAPI()) {
		img, err := get(ctx, p.own, u, nil)

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

func (p *Providers) slackAPI() string {
	if p.SlackAPI != "" {
		return p.SlackAPI
	}

	return slackAPI
}
