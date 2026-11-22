package core

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
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
//
// Its presence being the whole test is safe only while nobody else writes it,
// and a label was anybody's to write: so a request that carries this one is
// refused where requests arrive (`cmd.Undeclared`), on every listener.
const declaredBy = "roster.declared"

// mayWriteDeclared refuses a write to a row a file owns, unless the caller is
// the one that owns it.
//
// The provisioner is told apart by being **in this process**: it frames itself
// over every tenant as somebody of the control plane, before anything is
// served, and a person holding the database with `roster connection update`
// has no frame at all -- which is right, because they have the file too. A
// caller that got here from a port was resolved to a tenant and narrowed to
// it, or is a key ([inProcess]).
func (s Core) mayWriteDeclared(ctx context.Context, field string, labels map[string]string) error {
	if _, ok := labels[declaredBy]; !ok {
		return nil
	}
	if inProcess(ctx) {
		return nil
	}

	return status.Error(codes.FailedPrecondition, fmt.Sprintf(
		"%s: this row is declared in %s and is written from there, not here", field, labels[declaredBy]))
}

// deployment is whether the caller is the deployment itself, for the writes
// only it may make: the ones that name its secrets. One of its operators, at
// the port that has only operators ([Operated]); or the test
// [Core.mayWriteDeclared] states, a frame over every tenant, which nothing
// reaching a listener as a tenant's somebody has.
func (s Core) deployment(ctx context.Context) bool {
	return s.operators || deployment(ctx)
}

// deployment is the frame's half of [Core.deployment].
//
// No frame passes too. It is the deployment's own work in this process -- the
// CLI on the database, which turning a declared row off depended on -- and
// nothing at a port arrives without one: the gate refuses every method that is
// not public before it gets here.
func deployment(ctx context.Context) bool {
	f, ok := frame.From(ctx)

	return !ok || f.Scope.All()
}

// inProcess is whether a call is the deployment at work in its own process: no
// frame, which is a command on the database, or what applies its files, which
// frames itself over every tenant as a holder of the control plane.
//
// # And not a key
//
// [deployment] takes a frame over every tenant for the deployment, which a
// deployment key's is: that is right for what it is asked there -- a key
// calls what its list names in every tenant, and names the deployment's own
// secrets, being the operator's to hand out. It is wrong for the two rules
// that say **whose work** they are not asked of. A tenant is left with
// somebody administering it unless it is a command at the box or the
// operators' port that says otherwise, and a row a file declares is written by
// what applies the file: neither says a key, and both tested for a frame
// *nothing reaching a listener can have*, which a key reaching a listener has.
// So a key made to suspend a customer's people suspended the deployment's only
// operator, and one that could write a kind of row wrote the ones a file owns
// (#274).
func inProcess(ctx context.Context) bool {
	f, ok := frame.From(ctx)

	return !ok || (f.Scope.All() && f.Actor.Domain() != pd.ApiKeyDomain)
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
// every other write to one is. See the top of this file. A tenant's is not
// here: no tenant is erased, declared or not ([coreTenant.Erase]).
//
// A row that is not there to the caller -- nobody by that reference, another
// tenant's, one erased already, one out of their reach -- is erased as the
// generated `Erase` erases it: nothing, and `OK`. It answered `NotFound` here
// and `OK` from the services with no rule about their erase, and an erase is
// asked again after a timeout by every client: the second asking was a
// failure. The same of every erase in this layer that reads its row first.

func (s coreConnection) Erase(ctx context.Context, req *app.ConnectionRef) (*app.ConnectionEraseResponse, error) {
	if err := s.acrossTheTenant(ctx, app.ConnectionService_Erase_FullMethodName); err != nil {
		return nil, err
	}
	got, err := s.ConnectionServiceServer.Get(ctx, app.ConnectionGetRequest_builder{
		Ref: req, Select: app.ConnectionSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return s.ConnectionServiceServer.Erase(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}

	return s.ConnectionServiceServer.Erase(ctx, req)
}

func (s coreHost) Erase(ctx context.Context, req *app.HostRef) (*app.HostEraseResponse, error) {
	if err := s.acrossTheTenant(ctx, app.HostService_Erase_FullMethodName); err != nil {
		return nil, err
	}
	got, err := s.HostServiceServer.Get(ctx, app.HostGetRequest_builder{
		Ref: req, Select: app.HostSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return s.HostServiceServer.Erase(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}

	return s.HostServiceServer.Erase(ctx, req)
}

func (s coreMailDomain) Erase(ctx context.Context, req *app.MailDomainRef) (*app.MailDomainEraseResponse, error) {
	if err := s.acrossTheTenant(ctx, app.MailDomainService_Erase_FullMethodName); err != nil {
		return nil, err
	}
	got, err := s.MailDomainServiceServer.Get(ctx, app.MailDomainGetRequest_builder{
		Ref: req, Select: app.MailDomainSelect_builder{Labels: z.Ptr(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return s.MailDomainServiceServer.Erase(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}

	return s.MailDomainServiceServer.Erase(ctx, req)
}

func (s coreBinding) Patch(ctx context.Context, req *app.BindingPatchRequest) (*app.Binding, error) {
	if err := s.declaredBinding(ctx, req.GetRef()); err != nil {
		return nil, err
	}

	return s.BindingServiceServer.Patch(ctx, req)
}
