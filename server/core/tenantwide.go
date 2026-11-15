package core

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// What is the tenant's as a whole, and who may change it.
//
// # Why the gate's answer is not enough here
//
// The gate asks whether a caller may ever call a method, and counts every place
// a role reaches them: a binding across the tenant, a binding in a site, a role
// held in a team (`cmd/policy.go`, `policy.of`). That is the right question for
// a verb on rows the wall narrows by site, because the rows they reach are then
// the site's own. It is the wrong one for a row that is no site's. A Seoul
// administrator whose role named `TenantService/*` closed the whole tenant, and
// one whose role named a service of the rows below changed them for everybody
// in it: a grant made in Seoul reaching everything, which is the asymmetry
// `mayGrant` refuses one service along.
//
// The rows that are the tenant's as a whole, and every write of each:
//
//	Tenant      closing it, restoring it, how everybody in it signs in   Close, Restore, Update
//	Connection  the directories its people arrive through                Add, Update, Erase, Patch, Apply
//	Host        the names it answers at                                  Add, Update, Erase, Patch, Apply
//	HostProof   a claim to one of those names                            Add, Erase, Patch, Apply
//	MailDomain  where an address at its domains is sent to sign in       Add, Update, Erase, Patch, Apply
//	Nomination  who an app's key is answered as here                     Add, Erase, Patch, Apply
//	Site        the parts it is divided into                             Add, Erase, Patch, Apply
//	Holder      a person made, and one erased who is not the caller      Add, Erase
//	SiteMembership  who is of which site, and so in whose reach          Add, Erase
//	Team, Group one of no site, made                                     Add
//	GroupExposure   which relying party's tokens carry a group           Add, Erase
//
// And the trail, read, which no layer here is over: `cmd/policy.go`,
// `ofTheWholeTenant`.
//
// Each asks what the caller holds **across the tenant**: a binding with no
// site, to them or to a group they are in, which is [Rules.Everywhere] and what
// `HolderService.Reaches` answers as `everywhere`. Rows that carry a site --
// roles, bindings, teams, groups, their memberships -- are not here: the site
// on the row is the scope, and the rules about granting are what hold a write
// of one to it (`escalate.go`). Nor is a person's row otherwise: it is reached
// by a grant made in a site the person is in (`cmd/people.go`).
//
// The deployment's own work is not asked, as everywhere in this package: a
// file it declares, a command on its database, its operators.

// acrossTheTenant refuses a write of what is the tenant's as a whole to a caller
// who holds `method` only somewhere smaller than the tenant.
func (s Core) acrossTheTenant(ctx context.Context, method string) error {
	if s.deployment(ctx) {
		return nil
	}
	f, _ := frame.From(ctx)
	if s.rules.Everywhere == nil {
		return status.Error(codes.PermissionDenied,
			"this server cannot say what you hold across the tenant, so it will not let you change all of it")
	}

	held, err := s.rules.Everywhere(ctx, f.Actor)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(held, func(m string) bool { return frame.Covers(m, method) }) {
		return nil
	}

	return status.Errorf(codes.PermissionDenied,
		"%s is about the whole tenant, and you hold it only in part of it -- a site, or a team", method)
}

// The writes the layers below do not otherwise have, held to the same rule.

func (s coreConnection) Apply(ctx context.Context, req *app.ConnectionApplyRequest) (*app.Connection, error) {
	if err := s.acrossTheTenant(ctx, app.ConnectionService_Apply_FullMethodName); err != nil {
		return nil, err
	}

	return s.ConnectionServiceServer.Apply(ctx, req)
}

func (s coreHost) Apply(ctx context.Context, req *app.HostApplyRequest) (*app.Host, error) {
	if err := s.acrossTheTenant(ctx, app.HostService_Apply_FullMethodName); err != nil {
		return nil, err
	}

	return s.HostServiceServer.Apply(ctx, req)
}

func (s coreMailDomain) Apply(ctx context.Context, req *app.MailDomainApplyRequest) (*app.MailDomain, error) {
	if err := s.acrossTheTenant(ctx, app.MailDomainService_Apply_FullMethodName); err != nil {
		return nil, err
	}

	return s.MailDomainServiceServer.Apply(ctx, req)
}

func (s coreHostProof) Erase(ctx context.Context, req *app.HostProofRef) (*app.HostProofEraseResponse, error) {
	if err := s.acrossTheTenant(ctx, app.HostProofService_Erase_FullMethodName); err != nil {
		return nil, err
	}

	return s.HostProofServiceServer.Erase(ctx, req)
}

func (s coreHostProof) Apply(ctx context.Context, req *app.HostProofApplyRequest) (*app.HostProof, error) {
	if err := s.acrossTheTenant(ctx, app.HostProofService_Apply_FullMethodName); err != nil {
		return nil, err
	}

	return s.HostProofServiceServer.Apply(ctx, req)
}

func (s coreNomination) Apply(ctx context.Context, req *app.NominationApplyRequest) (*app.Nomination, error) {
	if err := s.acrossTheTenant(ctx, app.NominationService_Apply_FullMethodName); err != nil {
		return nil, err
	}

	return s.NominationServiceServer.Apply(ctx, req)
}

// coreSite is the layer over the generated `SiteService`, for this rule alone:
// a site is a part of the tenant, and making or removing one changes what the
// tenant is divided into for everybody in it.
type coreSite struct {
	Core
	app.SiteServiceServer
}

func (s Core) Site() app.SiteServiceServer { return coreSite{s, s.Next().Site()} }

func (s coreSite) Add(ctx context.Context, req *app.SiteAddRequest) (*app.Site, error) {
	if err := s.acrossTheTenant(ctx, app.SiteService_Add_FullMethodName); err != nil {
		return nil, err
	}

	return s.SiteServiceServer.Add(ctx, req)
}

// Erase takes a site that has nothing in it.
//
// # What is a site's, and why it goes first
//
// A role, a binding, a team and a group may each name a site, and each means
// what it means **there**: the role is bound there alone, the binding reaches
// there alone. An erase asked who was erasing and nothing of the site, so what
// was in it stayed, naming a place that was gone, and went on counting --
// somebody bound there kept every method the binding carried, in a site nobody
// could see or administer any more (#274). A team erased takes its roles with
// it (`cmd/policy.go`, `liveTeam`); a site erased took nothing.
//
// Refused, rather than taken with it or counted as gone. Erasing everything
// in a site because the site went is a great deal to undo for one call made
// by mistake. And a binding that stopped counting because its site was erased
// is a row that says something nothing reads, there for whoever next asks why
// somebody holds what they hold. So each is taken out by whoever decides it,
// and the refusal says what is left.
//
// It is about the site and not about who asks, so a call with no frame is
// held to it too. Who is recorded as being in a site ([SiteMembership]) is
// not what is asked: it grants nothing, and is a record of somewhere that
// was.
func (s coreSite) Erase(ctx context.Context, req *app.SiteRef) (*app.SiteEraseResponse, error) {
	if err := s.acrossTheTenant(ctx, app.SiteService_Erase_FullMethodName); err != nil {
		return nil, err
	}
	if s.rules.Sited != nil {
		k, err := s.siteOf(ctx, req)
		if err != nil {
			return nil, err
		}
		in, err := s.rules.Sited(ctx, k)
		if err != nil {
			return nil, err
		}
		if left := in.String(); left != "" {
			return nil, status.Errorf(codes.FailedPrecondition,
				"ref: this site still has %s in it, each of which means what it means there; take them out first", left)
		}
	}

	return s.SiteServiceServer.Erase(ctx, req)
}

// Sited is what is still a site's: the live rows that name it, by kind.
//
// Asked of the rows and not through a server, for [Administered]'s reason: it
// answers the same whoever is asking, and somebody erasing a site need not be
// somebody who may list its bindings.
type Sited func(ctx context.Context, site pdid.Id) (InSite, error)

// InSite is how many of each kind a site still has.
type InSite struct {
	Bindings, Roles, Teams, Groups int
}

// String is what is left, as a refusal says it, and empty for nothing.
func (v InSite) String() string {
	var out []string
	for _, k := range []struct {
		n    int
		kind string
	}{
		{v.Bindings, "binding"}, {v.Roles, "role"}, {v.Teams, "team"}, {v.Groups, "group"},
	} {
		switch {
		case k.n == 1:
			out = append(out, "a "+k.kind)
		case k.n > 1:
			out = append(out, fmt.Sprintf("%d %ss", k.n, k.kind))
		}
	}

	return strings.Join(out, ", ")
}

func (s coreSite) Patch(ctx context.Context, req *app.SitePatchRequest) (*app.Site, error) {
	if err := s.acrossTheTenant(ctx, app.SiteService_Patch_FullMethodName); err != nil {
		return nil, err
	}

	return s.SiteServiceServer.Patch(ctx, req)
}

func (s coreSite) Apply(ctx context.Context, req *app.SiteApplyRequest) (*app.Site, error) {
	if err := s.acrossTheTenant(ctx, app.SiteService_Apply_FullMethodName); err != nil {
		return nil, err
	}

	return s.SiteServiceServer.Apply(ctx, req)
}
