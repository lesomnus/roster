package core

import (
	"context"
	"strings"
	"unicode"

	"github.com/lesomnus/payday/pderr"

	app "github.com/lesomnus/roster/rstr"
)

// coreGroupExposure is the layer over the generated `GroupExposureService`. The
// client a group is shown to is a name its issuer gave it, and is held to the
// shape one has; which tenant the row is in is the group's, through the wall.
type coreGroupExposure struct {
	Core
	app.GroupExposureServiceServer
}

func (s Core) GroupExposure() app.GroupExposureServiceServer {
	return coreGroupExposure{s, s.Next().GroupExposure()}
}

// maxClient is the longest client a group may be shown to. An issuer's
// identifiers are a UUID or a short name, and anything a good deal longer is
// not one of them.
const maxClient = 255

func (s coreGroupExposure) Add(ctx context.Context, req *app.GroupExposureAddRequest) (*app.GroupExposure, error) {
	if err := isClient(req.GetClient()); err != nil {
		return nil, err
	}

	return s.GroupExposureServiceServer.Add(ctx, req)
}

// isClient refuses what no issuer would call a client. Compared as it is
// written, since a `client_id` is the issuer's string and nothing here may
// decide two of them are the same.
func isClient(v string) error {
	switch {
	case v == "":
		return pderr.Invalidf("client", "the client_id of the relying party whose tokens carry the group")
	case len(v) > maxClient:
		return pderr.Invalidf("client", "at most %d bytes, and a client_id is a good deal shorter", maxClient)
	case strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return pderr.Invalidf("client", "%q: a client_id has no space or control character in it", v)
	}

	return nil
}
