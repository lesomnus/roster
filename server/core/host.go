package core

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
)

// A name is stored as it will be compared, and this is what says so.
//
// # Why it is a refusal and not a fixup
//
// Normalising on the way in would be kinder and is the wrong direction. A
// caller that wrote `Contoso.Example.com:8443` and got back a row saying
// `contoso.example.com` has had its value changed without being told, and the next
// thing it does is compare the two and disagree with itself. Worse, the
// caller most likely to write one is a console reading it back to a person, who
// then cannot find the name they just typed.
//
// So it is refused, with the value it should have been. What it costs is one
// line in whatever writes one -- `front.Hostname` is exported precisely so that
// there is nothing to reimplement.
//
// # What goes wrong without it
//
// Nothing, for a long time. The row is written, the console lists it, and the
// only thing that never happens is a match: `FrontService.WhoseHost` normalises
// what a browser arrived at, so a row that is not normalised is a row no
// request ever reaches. The symptom is a sign-in page saying nobody is there,
// on a tenant that is plainly configured, which is a long way from the cause.

type coreHost struct {
	Core
	app.HostServiceServer
}

func (s Core) Host() app.HostServiceServer { return coreHost{s, s.Next().Host()} }

func (s coreHost) Add(ctx context.Context, req *app.HostAddRequest) (*app.Host, error) {
	if err := normalised("name", req.GetName(), front.Hostname); err != nil {
		return nil, err
	}

	return s.HostServiceServer.Add(ctx, req)
}

func (s coreHost) Patch(ctx context.Context, req *app.HostPatchRequest) (*app.Host, error) {
	if req.HasName() {
		if err := normalised("name", req.GetName(), front.Hostname); err != nil {
			return nil, err
		}
	}

	return s.HostServiceServer.Patch(ctx, req)
}

type coreMailDomain struct {
	Core
	app.MailDomainServiceServer
}

func (s Core) MailDomain() app.MailDomainServiceServer {
	return coreMailDomain{s, s.Next().MailDomain()}
}

func (s coreMailDomain) Add(ctx context.Context, req *app.MailDomainAddRequest) (*app.MailDomain, error) {
	if err := normalised("name", req.GetName(), front.Domain); err != nil {
		return nil, err
	}

	return s.MailDomainServiceServer.Add(ctx, req)
}

func (s coreMailDomain) Patch(ctx context.Context, req *app.MailDomainPatchRequest) (*app.MailDomain, error) {
	if req.HasName() {
		if err := normalised("name", req.GetName(), front.Domain); err != nil {
			return nil, err
		}
	}

	return s.MailDomainServiceServer.Patch(ctx, req)
}

// normalised refuses a value that is not already what it will be compared as.
func normalised(field, v string, by func(string) string) error {
	if v == "" {
		return status.Errorf(codes.InvalidArgument, "%s: must not be empty", field)
	}

	w := by(v)
	if w == v {
		return nil
	}
	if w == "" {
		return status.Errorf(codes.InvalidArgument, "%s: %q is not a name", field, v)
	}

	return status.Errorf(codes.InvalidArgument,
		"%s: stored as it is compared, so %q rather than %q", field, w, v)
}
