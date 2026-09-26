package core

import (
	"context"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/front"
	"github.com/lesomnus/roster/server/prove"
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
// Nothing, for a long time. The row is written, the admin console lists it, and the
// only thing that never happens is a match: `FrontService.WhoseHost` normalises
// what a browser arrived at, so a row that is not normalised is a row no
// request ever reaches. The symptom is a sign-in page saying nobody is there,
// on a tenant that is plainly configured, which is a long way from the cause.

type coreHost struct {
	Core
	app.HostServiceServer
}

func (s Core) Host() app.HostServiceServer { return coreHost{s, s.Next().Host()} }

// Add writes a name, and there are two roads to one.
//
// A tenant writing its **own** name is held to a proof: they claimed it
// ([coreHostProof.Add]), published what roster asked for, and this looks it up.
// Anybody else is somebody administering a tenant from outside it -- a roster
// operator through `admin.addr` or through a shell -- and writes the row.
//
// # Why that is the discriminator and not the port
//
// Because the port is not a thing this layer can see, and the frame is. What it
// asks is *is the tenant you are writing into your own*, which answers the
// question that matters rather than a proxy for it: **proving a name is
// something you do about your own organisation.** Writing one into somebody
// else's is administration, and the standing for it is the port, where
// `docs/operating.md` § "What the admin port waives" already says what that
// means.
//
// A request with no frame at all is the deployment's own work through an
// unwalled server -- `init`, `resources apply`, the CLI -- which is where every
// other rule in `escalate.go` waives itself and for the same stated reason.
//
// And a tenant cannot reach the other road by naming somebody else's tenant: the
// gate refuses an `Add` whose edges point into a tenant the caller cannot see,
// which is what `cmd/foreignedge_test.go` is about.
func (s coreHost) Add(ctx context.Context, req *app.HostAddRequest) (*app.Host, error) {
	if err := normalised("name", req.GetName(), front.Hostname); err != nil {
		return nil, err
	}
	if err := s.actsAsIsTheirs(ctx, req.GetTenant(), req.GetActsAs()); err != nil {
		return nil, err
	}

	own, err := s.ownName(ctx, req.GetTenant())
	if err != nil {
		return nil, err
	}
	if !own {
		// `date_proved` stays unset, which is the row saying nobody proved it.
		// The trail says who wrote it.
		return s.HostServiceServer.Add(ctx, req)
	}

	return s.proved(ctx, req)
}

// ownName reports whether the tenant being written into is the caller's.
//
// No frame is **false**: the deployment's own work is not a tenant claiming
// anything. A ref that names nothing is false for the same reason -- payday
// fills the tenant from the frame where it can, and a request that got here with
// neither is one the generated `Add` refuses below for its own reasons.
func (s coreHost) ownName(ctx context.Context, at *app.TenantRef) (bool, error) {
	f, ok := frame.From(ctx)
	if !ok || f.Tenant == pdid.Nil {
		return false, nil
	}
	if at == nil {
		// Nothing said, so it is whatever the frame is -- which is the caller's
		// own by construction.
		return true, nil
	}

	v, err := s.Next().Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: at}.Build())
	if err != nil {
		return false, err
	}

	k, err := pdid.From(v.GetId())
	if err != nil {
		return false, err
	}

	return k == f.Tenant, nil
}

// proved is the tenant's road: the claim, the lookup, and the row.
//
// Three writes in one transaction, and the order is the whole of it. The name is
// unique among the rows that are not erased, so the incumbent has to let go
// **before** the insert or the insert is what fails -- and if anything after
// that goes wrong the release has to go with it, or a tenant loses a name to a
// call that wrote nothing.
//
//	release   whoever holds this name lets go of it, across the deployment
//	add       the row, then the stamp `date_proved` needs a `Patch` for
//	spend     the claim is erased, the way a `Continuation` is spent
//
// `Add` then `Patch` rather than one write, because `date_proved` is `stamped`
// and a stamped field is refused to a request: the one road to it is a server
// writing it below the gate. `coreEmail.Confirm` is the same two writes for the
// same reason.
func (s coreHost) proved(ctx context.Context, req *app.HostAddRequest) (*app.Host, error) {
	if s.proving == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"this deployment cannot check whether a name is yours, so a tenant cannot claim one here; "+
				"a roster operator writes the row (host.resolver, and docs/operating.md)")
	}
	if s.rules.Releasing == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"this server cannot let go of a name another tenant holds, so it will not take one")
	}
	if s.drv == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"this server cannot open a transaction, so it will not take a name in three writes")
	}

	name := req.GetName()

	// Through the wall, so a claim is only ever the caller's own -- there is no
	// reference here a caller could point at somebody else's.
	c, err := s.Next().HostProof().Get(ctx, app.HostProofGetRequest_builder{
		Ref: app.HostProofRef_builder{
			At: app.HostProofRefByAt_builder{Tenant: req.GetTenant(), Name: z.Ptr(name)}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, status.Errorf(codes.FailedPrecondition,
				"%s: claim it first, and roster will say what to publish", name)
		}

		return nil, err
	}
	if e := c.GetDateExpires(); e != nil && e.AsTime().Before(time.Now()) {
		return nil, status.Errorf(codes.FailedPrecondition,
			"%s: that claim expired; make another and publish what it answers with", name)
	}

	// Refused apart from a claim that was not proved, because they are different
	// mornings: a lookup that never left is not a record that is wrong, and
	// telling somebody their zone is wrong when it is not is how an afternoon
	// goes on a correct zone file.
	held, err := prove.Held(ctx, s.proving, name, c.GetToken())
	if err != nil {
		return nil, status.Errorf(codes.Unavailable,
			"%s: could not ask DNS whether the record is there: %v", name, err)
	}
	if !held {
		return nil, status.Errorf(codes.FailedPrecondition,
			"%s: %s.%s does not say %q yet", name, prove.Record, name, c.GetToken())
	}

	drv, tx, err := dialect.BeginTx(ctx, s.drv)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Whoever held the name lets go of it, on the driver this transaction is,
	// and across every tenant -- which is the one thing here the wall cannot do
	// and the reason it is a `Rules` function rather than a server. It erases at
	// most one row and cannot be pointed at anything else; `cmd.Releasing` is
	// the whole of it.
	if err := s.rules.Releasing(ctx, drv, name); err != nil {
		return nil, err
	}

	// This layer again over a rebound one below it, as `coreTenant.Add` does:
	// the row goes to the server **below** this one, or `Add` arrives back here
	// and asks for a claim that has just been spent.
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	v, err := next.Host().Add(ctx, req)
	if err != nil {
		return nil, err
	}

	v, err = next.Host().Patch(ctx, app.HostPatchRequest_builder{
		Ref:         app.HostRef_builder{Id: v.GetId()}.Build(),
		DateProved:  timestamppb.Now(),
		DateUpdated: v.GetDateUpdated(),
	}.Build())
	if err != nil {
		return nil, err
	}

	// Spent by an erase, the way a `Continuation` is: *used* is *not there*, so
	// there is no second column recording the same fact.
	if _, err := next.HostProof().Erase(ctx,
		app.HostProofRef_builder{Id: c.GetId()}.Build()); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return v, nil
}

// actsAsIsTheirs refuses a `Host` nominating somebody else's holder.
//
// The nomination is what a deployment key borrows here (`Host.acts_as`,
// `server/keys/at.go`), so a row naming a holder of another tenant would hand a
// caller a frame in a tenant that never agreed to it -- one row reaching two,
// which is what the agreements in `agree.go` exist for and the same shape.
//
// Unset is nobody to borrow and is the ordinary state; only a nomination is
// checked.
func (s coreHost) actsAsIsTheirs(ctx context.Context, at *app.TenantRef, who *app.HolderRef) error {
	if who == nil {
		return nil
	}

	whose, err := s.tenantOfHolder(ctx, who)
	if err != nil {
		return err
	}

	where := pdid.Nil
	if at != nil {
		v, err := s.Next().Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: at}.Build())
		if err != nil {
			return err
		}

		if where, err = pdid.From(v.GetId()); err != nil {
			return err
		}
	}

	return tenantsAgree("acts_as", whose, where)
}

func (s coreHost) Patch(ctx context.Context, req *app.HostPatchRequest) (*app.Host, error) {
	if req.HasName() {
		if err := normalised("name", req.GetName(), front.Hostname); err != nil {
			return nil, err
		}
	}

	return s.HostServiceServer.Patch(ctx, req)
}

// Update is `Patch` with the name held back; `host_svc.ext.proto` says why.
func (s coreHost) Update(ctx context.Context, req *app.HostUpdateRequest) (*app.Host, error) {
	patch := app.HostPatchRequest_builder{
		Ref:         req.GetRef(),
		DateUpdated: req.GetDateUpdated(),
	}
	if req.HasDesc() {
		patch.Desc = z.Ptr(req.GetDesc())
	}

	return s.HostServiceServer.Patch(ctx, patch.Build())
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

// Update is `Patch` with the name held back; `host_svc.ext.proto` says why.
func (s coreMailDomain) Update(ctx context.Context, req *app.MailDomainUpdateRequest) (*app.MailDomain, error) {
	patch := app.MailDomainPatchRequest_builder{
		Ref:         req.GetRef(),
		DateUpdated: req.GetDateUpdated(),
	}
	if req.HasProvider() {
		patch.Provider = z.Ptr(req.GetProvider())
	}
	if req.HasDesc() {
		patch.Desc = z.Ptr(req.GetDesc())
	}

	return s.MailDomainServiceServer.Patch(ctx, patch.Build())
}
