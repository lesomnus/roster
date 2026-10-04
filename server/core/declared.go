package core

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/roster/rstr"
)

// A row a file declared is a row the file owns.
//
// # Why refusing is kinder than allowing
//
// `cmd/resources.go` applies what a deployment declared, every time it starts.
// So an operator who edits a declared `Connection` in the admin console has made a
// change that survives until the next restart and then vanishes -- and the
// restart is a config change, an image bump, a node draining, none of which
// look related. They would be left with a setting that was right on Tuesday and
// wrong on Wednesday, and nothing anywhere saying why.
//
// Refused, they find out immediately and the message says where the row is
// declared. That is the whole of it: the edit was already futile, and this is
// the difference between being told and finding out.
//
// # It costs something, and the cost is real
//
// An operator cannot fix a declared row by hand during an outage. A wrong
// issuer is a git round trip -- a commit, a sync, a restart -- when what they
// want is thirty seconds and a text field. That is the price of the row having
// one source of truth, and a deployment that would rather have the text field
// declares fewer things.
//
// # Erasure too
//
// It used not to guard it: an erased declared row came back on the next start,
// and that was read as the provisioner winning, which is the answer anyway.
// It is the same futile edit -- one that holds until a restart nobody connects
// to it -- and roster's own front doors made it sharp. A tenant administrator
// could end the Login App's nomination in their tenant from the user console:
// everybody's sign-in there stopped, and came back at the next deploy. So a
// declared row is **read-only** to everybody who reaches it through a port.
//
// Turning one off is two steps, both outside any console: take it out of
// where it is declared -- `resources.yaml`, or the deployment's configuration
// and manifests for a front door -- and then erase what is left from a shell
// on the box, as the deployment (`docs/operating.md`, "Declared rows").
//
// # What is declared
//
// The rows `cmd/resources.go` applies, and the rows roster's own front doors
// write for themselves at start from the configuration that turns them on
// (`cli/login.go`, `nominateAs`): their holder in each tenant, its role, the
// binding and the nomination.

// declaredBy is the label a provisioner writes, and its presence is the whole
// test. `cmd.Declared` is the name; it is spelled out here rather than imported
// because `server` may not import `cmd` -- the sandbox's dependency graph is
// the reason, and `cmd/consumers.go` has it.
const declaredBy = "roster.declared"

// mayWriteDeclared refuses a write to a row a file owns, unless the caller is
// the one that owns it.
//
// The provisioner is told apart by its **scope**: it frames itself over every
// tenant, which is what nothing reaching a listener can have. A caller that got
// here from a port was resolved to a tenant and narrowed to it; the provisioner
// runs in-process, before anything is served, and so does a person holding the
// database with `roster connection update` -- which is right, because they have
// the file too.
func (s Core) mayWriteDeclared(ctx context.Context, field string, labels map[string]string) error {
	if _, ok := labels[declaredBy]; !ok {
		return nil
	}
	// No frame is the deployment's own work in this process -- the CLI on the
	// database, which is what the paragraph above says passes and what this
	// refused until turning a declared row off depended on it. Nothing at a
	// port arrives without one: the gate refuses every method that is not
	// public before it gets here.
	if f, ok := frame.From(ctx); !ok || f.Scope.All() {
		return nil
	}

	return status.Error(codes.FailedPrecondition, fmt.Sprintf(
		"%s: this row is declared in %s and is written from there, not here", field, labels[declaredBy]))
}

// The guard asked of each declarable kind before a write, by reading the row's
// labels. A row that is not there is answered as the write would answer it.

func (s coreHolder) declaredHolder(ctx context.Context, ref *app.HolderRef) error {
	got, err := s.HolderServiceServer.Get(ctx, app.HolderGetRequest_builder{
		Ref: ref, Select: app.HolderSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return err
	}

	return s.mayWriteDeclared(ctx, "ref", got.GetLabels())
}

func (s coreRole) declaredRole(ctx context.Context, ref *app.RoleRef) error {
	got, err := s.RoleServiceServer.Get(ctx, app.RoleGetRequest_builder{
		Ref: ref, Select: app.RoleSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return err
	}

	return s.mayWriteDeclared(ctx, "ref", got.GetLabels())
}

func (s coreBinding) declaredBinding(ctx context.Context, ref *app.BindingRef) error {
	got, err := s.BindingServiceServer.Get(ctx, app.BindingGetRequest_builder{
		Ref: ref, Select: app.BindingSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return err
	}

	return s.mayWriteDeclared(ctx, "ref", got.GetLabels())
}

func (s coreNomination) declaredNomination(ctx context.Context, ref *app.NominationRef) error {
	got, err := s.Next().Nomination().Get(ctx, app.NominationGetRequest_builder{
		Ref: ref, Select: app.NominationSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return err
	}

	return s.mayWriteDeclared(ctx, "ref", got.GetLabels())
}

// Erase, for every kind a deployment declares: refused on a declared row, as
// every other write to one is. See the top of this file.

func (s coreTenant) Erase(ctx context.Context, req *app.TenantRef) (*app.TenantEraseResponse, error) {
	got, err := s.TenantServiceServer.Get(ctx, app.TenantGetRequest_builder{
		Ref: req, Select: app.TenantSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}

	return s.TenantServiceServer.Erase(ctx, req)
}

func (s coreConnection) Erase(ctx context.Context, req *app.ConnectionRef) (*app.ConnectionEraseResponse, error) {
	got, err := s.ConnectionServiceServer.Get(ctx, app.ConnectionGetRequest_builder{
		Ref: req, Select: app.ConnectionSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}

	return s.ConnectionServiceServer.Erase(ctx, req)
}

func (s coreHost) Erase(ctx context.Context, req *app.HostRef) (*app.HostEraseResponse, error) {
	got, err := s.HostServiceServer.Get(ctx, app.HostGetRequest_builder{
		Ref: req, Select: app.HostSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}

	return s.HostServiceServer.Erase(ctx, req)
}

func (s coreMailDomain) Erase(ctx context.Context, req *app.MailDomainRef) (*app.MailDomainEraseResponse, error) {
	got, err := s.MailDomainServiceServer.Get(ctx, app.MailDomainGetRequest_builder{
		Ref: req, Select: app.MailDomainSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}

	return s.MailDomainServiceServer.Erase(ctx, req)
}

func (s coreHolder) Erase(ctx context.Context, req *app.HolderRef) (*app.HolderEraseResponse, error) {
	if err := s.declaredHolder(ctx, req); err != nil {
		return nil, err
	}

	return s.HolderServiceServer.Erase(ctx, req)
}

func (s coreRole) Erase(ctx context.Context, req *app.RoleRef) (*app.RoleEraseResponse, error) {
	if err := s.declaredRole(ctx, req); err != nil {
		return nil, err
	}

	return s.RoleServiceServer.Erase(ctx, req)
}

func (s coreBinding) Patch(ctx context.Context, req *app.BindingPatchRequest) (*app.Binding, error) {
	if err := s.declaredBinding(ctx, req.GetRef()); err != nil {
		return nil, err
	}

	return s.BindingServiceServer.Patch(ctx, req)
}

func (s coreBinding) Erase(ctx context.Context, req *app.BindingRef) (*app.BindingEraseResponse, error) {
	if err := s.declaredBinding(ctx, req); err != nil {
		return nil, err
	}

	return s.BindingServiceServer.Erase(ctx, req)
}
