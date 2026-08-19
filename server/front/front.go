// Package front answers the two questions a front door has before it knows
// anything.
//
// Which tenant serves the name a browser arrived at, and where the people at an
// address authenticate. Both are asked at the moment nobody has been resolved
// to anybody, which is what decides everything about the shape below.
//
// # It reads through no wall
//
// `vouch.Verify` does the same and says why: *working out who somebody is
// cannot require knowing who they are.* There is no frame to narrow by, because
// producing one is what the caller is trying to do.
//
// What keeps that honest is that neither RPC answers with a **row**. One
// identifier, or one provider name. There is no `Select` to get wrong, nothing
// that could be pointed at anything but a name somebody typed into DNS, and no
// shape for a field to be added to later without somebody noticing.
//
// # Neither half is public
//
// The person arriving has no credential -- that is what they are asking for --
// but they are not the caller. The caller is a front door holding a key, and
// roster is reached only by machines.
package front

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// Server answers what a front door asks first.
type Server struct {
	app.UnimplementedFrontServiceServer

	open app.Server
}

// New makes the service from the stack the wall was never installed on.
//
// One argument rather than two, unlike `vouch.New`: nothing here writes, so
// there is no second stack to get right.
func New(open app.Server) *Server { return &Server{open: open} }

// Hostname is a host as this app stores and compares one: lowercased, with any
// port removed.
//
// Exported because both sides need it and neither may disagree. A row written
// as `Acme.Example.com:8443` is a row that silently never matches, and the
// place that finds out is a sign-in page saying nobody is there.
func Hostname(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))

	// A bracketed IPv6 literal keeps its colons. Cutting at the last colon
	// outside the brackets is what `net.SplitHostPort` does, and doing it by
	// hand avoids an error return for a string that may legitimately have no
	// port at all.
	if i := strings.LastIndex(v, "]"); i >= 0 {
		if j := strings.Index(v[i:], ":"); j >= 0 {
			v = v[:i+j]
		}

		return v
	}
	if i := strings.LastIndex(v, ":"); i >= 0 && !strings.Contains(v[i+1:], ":") {
		v = v[:i]
	}

	return v
}

// Domain is the part of an address that is looked up, from either an address or
// a bare domain.
func Domain(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if i := strings.LastIndex(v, "@"); i >= 0 {
		v = v[i+1:]
	}

	return v
}

// WhoseHost answers which tenant serves a name.
func (s *Server) WhoseHost(ctx context.Context, req *app.FrontWhoseHostRequest) (*app.FrontWhoseHostResponse, error) {
	name := Hostname(req.GetHost())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "host: which name did they arrive at")
	}

	v, err := s.open.Host().Get(ctx, app.HostGetRequest_builder{
		Ref: app.HostRef_builder{Name: &name}.Build(),
		Select: app.HostSelect_builder{
			Tenant: app.TenantSelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		// `NotFound` unchanged, which is the answer a front door needs: one
		// that carried on with no tenant would look somebody up in whichever
		// one it happened to reach.
		return nil, err
	}

	k, err := pdid.From(v.GetTenant().GetId())
	if err != nil {
		return nil, err
	}

	return app.FrontWhoseHostResponse_builder{Tenant: k.Bytes()}.Build(), nil
}

// WhereFrom answers where the people at an address authenticate.
//
// A domain nobody has said anything about answers with an empty provider rather
// than `NotFound`, and that is the opposite of the choice above on purpose:
// there is nothing here a caller could carry on wrongly with, and a front door
// that learns nothing offers whatever it offers everybody. It is also what
// stops this from answering *does this domain exist here*.
func (s *Server) WhereFrom(ctx context.Context, req *app.FrontWhereFromRequest) (*app.FrontWhereFromResponse, error) {
	name := Domain(req.GetAddress())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "address: an address, or the domain from one")
	}

	tenant, err := pdid.From(req.GetTenant())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "tenant: %s", err)
	}

	v, err := s.open.MailDomain().Get(ctx, app.MailDomainGetRequest_builder{
		Ref: app.MailDomainRef_builder{
			At: app.MailDomainRefByAt_builder{
				Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
				Name:   &name,
			}.Build(),
		}.Build(),
		Select: app.MailDomainSelect_builder{Provider: ptr(true)}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return app.FrontWhereFromResponse_builder{}.Build(), nil
		}

		return nil, err
	}

	return app.FrontWhereFromResponse_builder{Provider: v.GetProvider()}.Build(), nil
}

func ptr[T any](v T) *T { return &v }
