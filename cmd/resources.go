package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// Declaring a deployment's rows in a file, and applying them at start.
//
// # What this is for
//
// A `Connection` is a customer's directory: an issuer, a client id, the scopes,
// and the reference to a secret roster stores and never reads. It is
// configuration by every test one can put to it -- it is written once, it is
// the same on every replica, and somebody who loses it rebuilds it from what
// they wrote down. What it was until now is a row somebody typed into a
// console, which is a deployment that cannot be stood up twice the same way.
//
// So the rows that are configuration are declared beside the configuration:
// `resources:` in `roster.yaml` names files, this reads them, and `serve`
// applies them before it serves. A file in a ConfigMap makes that GitOps
// without roster knowing what Kubernetes is -- editing it changes the hash,
// which replaces the pod, which applies it.
//
// # Three rules, and each of them is a decision
//
// **It adds and it updates. It never erases.** A resource dropped from a file
// leaves its row where it is. That is not laziness: `Connection.Update` exists
// because erase-and-add on a provider is a gap in service and, under a
// mistyped name, every identity through it orphaned silently. A reconciler that
// deleted what it no longer saw would do that on a bad merge. Removing a row is
// a person's act, with the console or the terminal.
//
// **It writes as somebody.** Every row carries `Audit.actor_id`, and a
// provisioner that wrote as nobody would leave a trail that says a row appeared.
// So there is a `provisioner` holder in the control plane -- made here, on
// first run -- and every write below is framed as them. It has no password and
// no key: it is not something that signs in, it is something the trail can
// name.
//
// **What it writes, it owns.** A row this applied carries [Declared], and
// `server/core` refuses a write to it from anybody else. The alternative is a
// console edit that survives until the next restart and then vanishes, which is
// worse than being told no -- see `server/core/declared.go`.
//
// # What is not here, and why
//
// `Holder`, `Credential`, `Identity` and `Email` are people and the ways into
// their accounts. A file that made those is a file that grants access to
// whoever can write it, and `server/core/escalate.go` is a whole document about
// not doing that by accident.
//
// `Role` and `Binding` are the same argument one step further out -- *a grant
// is any write that changes what the gate will answer for somebody* -- and they
// are a better idea than they look, because RBAC in git is reviewed RBAC. They
// are left out **for now** rather than refused: it is a decision to take on
// purpose and not one to arrive at because a provisioner happened to support
// them.

// Declared is the label a provisioned row carries, and its value is the file
// that declared it.
//
// A label rather than a field, because it is true of every entity this touches
// and adding a column to four of them for it would be four migrations for one
// fact. The cost is `Holder.Profile`'s -- labels are where an app puts what the
// schema does not name, and this spends one of those names.
const Declared = "roster.declared"

// Provisioner is the control-plane holder every write below is framed as.
const Provisioner = "provisioner"

// Resource is one declared row: what kind, and its fields.
//
// Shaped like a Kubernetes object because that is what somebody writing one
// expects, and not because roster knows anything about Kubernetes. `kind` picks
// the entity and the rest is that entity's own vocabulary -- the same names its
// proto uses, so a person reading `docs/entity.md` can write one.
type Resource struct {
	Kind string `yaml:"kind"`

	// Tenant is the customer this hangs off, by alias. Every kind below but
	// `Tenant` needs one.
	Tenant string `yaml:"tenant"`

	// Alias is a `Tenant`'s; Name is what every other kind is called.
	Alias string `yaml:"alias"`
	Name  string `yaml:"name"`
	Desc  string `yaml:"desc"`

	// Connection.
	Issuer    string   `yaml:"issuer"`
	ClientId  string   `yaml:"client_id"`
	Scopes    []string `yaml:"scopes"`
	SecretRef string   `yaml:"secret_ref"`

	// MailDomain: which of the tenant's connections an address at this domain
	// is routed to. Empty routes nowhere, as on `Add`.
	Routes string `yaml:"routes"`
}

// Resources is a file of them.
type Resources struct {
	Resources []Resource `yaml:"resources"`
}

// Applied is what a run did, for a caller that wants to say so.
type Applied struct {
	Added   []string
	Changed []string
	Same    []string
}

// ReadResources reads the files named, in order.
func ReadResources(paths []string) ([]Resource, error) {
	out := []Resource{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("resources: %s: %w", p, err)
		}

		var v Resources
		if err := yaml.Unmarshal(b, &v); err != nil {
			return nil, fmt.Errorf("resources: %s: %w", p, err)
		}
		for i, r := range v.Resources {
			if r.Kind == "" {
				return nil, fmt.Errorf("resources: %s: [%d]: kind", p, i)
			}
			out = append(out, r)
		}
	}

	return out, nil
}

// ApplyResources writes what the files declared, as the provisioner.
//
// Idempotent by construction rather than by checking: each kind is `Add`, and
// on `AlreadyExists` a read for the version and a `Patch` over it. So a first
// run creates, a second with the same file changes nothing anybody can see, and
// one with a field edited writes that field.
//
// `dry` answers what it would do and writes nothing, which is what a person
// wants before a deployment does it for them.
func ApplyResources(ctx context.Context, s *Server, rs []Resource, dry bool) (Applied, error) {
	out := Applied{}
	if len(rs) == 0 {
		return out, nil
	}

	as, err := asProvisioner(ctx, s)
	if err != nil {
		return out, err
	}

	for i, r := range rs {
		var (
			what string
			was  string
			err  error
		)
		switch r.Kind {
		case "Tenant":
			what, was, err = applyTenant(as, s, r, dry)
		case "Connection":
			what, was, err = applyConnection(as, s, r, dry)
		case "Host":
			what, was, err = applyHost(as, s, r, dry)
		case "MailDomain":
			what, was, err = applyMailDomain(as, s, r, dry)
		default:
			// Named rather than ignored. A `kind` this does not know is a
			// typo or a thing somebody expected to work, and either way a
			// silent skip is the worst answer.
			return out, fmt.Errorf("resources: [%d]: %q is not a kind this applies", i, r.Kind)
		}
		if err != nil {
			return out, fmt.Errorf("resources: [%d] %s: %w", i, r.Kind, err)
		}

		switch was {
		case "added":
			out.Added = append(out.Added, what)
		case "changed":
			out.Changed = append(out.Changed, what)
		default:
			out.Same = append(out.Same, what)
		}
	}

	return out, nil
}

// asProvisioner is the context every write goes out on: the control plane's
// `provisioner` holder, framed as the actor.
//
// Made here on first run rather than by `init`, because a deployment that has
// already been initialised -- which is every one this is useful to -- would
// otherwise need a second visit to get one.
//
// `frame.Everything` because this writes across every tenant and the wall is
// what it would otherwise be narrowed by. It is `Ungated` either way; what the
// frame buys is the **trail**, which now says which rows a file wrote.
func asProvisioner(ctx context.Context, s *Server) (context.Context, error) {
	if s.Control == nil {
		return nil, errors.New("resources: no control plane, and the provisioner is a holder in it")
	}

	// The same call `roster key add` makes for a service, which is what this
	// is: a row in the control plane's one tenant, made if it is not there.
	who, err := ServiceOf(ctx, s.Control, Provisioner)
	if err != nil {
		return nil, err
	}

	// `frame.Everything` because this writes across every tenant, and the wall
	// is what would otherwise narrow it. The server is `Ungated` either way;
	// what the frame buys is the **trail**, which now names which rows a file
	// wrote rather than saying they appeared.
	return frame.Into(ctx, frame.New(who, pdid.Nil, frame.Whole())), nil
}

// labels is what a declared row carries, merged over what the file said.
func labelsOf(r Resource) map[string]string {
	return map[string]string{Declared: r.Kind}
}

// The four kinds. Each is the same shape and the shape is the point: `Add`,
// and on `AlreadyExists` a read for the version and a `Patch` over it. So the
// first run creates, the second changes nothing, and one with a field edited
// writes that field.
//
// What none of them writes twice is the **name**. A `Connection.name` is what
// `Identity.provider` points at, a `Host.name` is what a tenant is resolved
// through and a `MailDomain.name` is what an address is routed by -- so a
// renamed one is a **different resource**, and the old row stays because this
// erases nothing. `Update` on each of these refuses the name for the same
// reason (`proto/ext/app/host_svc.ext.proto` and its neighbours); this is that
// rule arrived at from the other side.

func applyTenant(ctx context.Context, s *Server, r Resource, dry bool) (string, string, error) {
	what := "@" + r.Alias
	if r.Alias == "" {
		return what, "", errors.New("alias")
	}

	got, err := s.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    app.TenantRef_builder{Alias: proto.String(r.Alias)}.Build(),
		Select: app.TenantSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		if dry {
			return what, "added", nil
		}
		_, err = s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{
			Alias: r.Alias, Name: r.Name, Desc: r.Desc, Labels: labelsOf(r),
		}.Build())

		return what, "added", err
	}
	if err != nil {
		return what, "", err
	}
	if got.GetName() == r.Name && got.GetDesc() == r.Desc && declared(got.GetLabels()) {
		return what, "same", nil
	}
	if dry {
		return what, "changed", nil
	}

	_, err = s.Ungated.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
		Ref:         app.TenantRef_builder{Id: got.GetId()}.Build(),
		Name:        proto.String(r.Name),
		Desc:        proto.String(r.Desc),
		Labels:      labelsOf(r),
		DateUpdated: got.GetDateUpdated(),
	}.Build())

	return what, "changed", err
}

func applyConnection(ctx context.Context, s *Server, r Resource, dry bool) (string, string, error) {
	what := "@" + r.Tenant + "/" + r.Name
	at, err := tenantRef(ctx, s, r)
	if err != nil {
		return what, "", err
	}

	got, err := s.Ungated.Connection().Get(ctx, app.ConnectionGetRequest_builder{
		Ref: app.ConnectionRef_builder{
			At: app.ConnectionRefByAt_builder{Tenant: at, Name: proto.String(r.Name)}.Build(),
		}.Build(),
		Select: app.ConnectionSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		if dry {
			return what, "added", nil
		}
		_, err = s.Ungated.Connection().Add(ctx, app.ConnectionAddRequest_builder{
			Tenant: at, Name: r.Name, Desc: r.Desc,
			Issuer: r.Issuer, ClientId: r.ClientId, Scopes: r.Scopes, SecretRef: r.SecretRef,
			Labels: labelsOf(r),
		}.Build())

		return what, "added", err
	}
	if err != nil {
		return what, "", err
	}
	if got.GetIssuer() == r.Issuer && got.GetClientId() == r.ClientId &&
		got.GetSecretRef() == r.SecretRef && got.GetDesc() == r.Desc &&
		same(got.GetScopes(), r.Scopes) && declared(got.GetLabels()) {
		return what, "same", nil
	}
	if dry {
		return what, "changed", nil
	}

	_, err = s.Ungated.Connection().Patch(ctx, app.ConnectionPatchRequest_builder{
		Ref:         app.ConnectionRef_builder{Id: got.GetId()}.Build(),
		Desc:        proto.String(r.Desc),
		Issuer:      proto.String(r.Issuer),
		ClientId:    proto.String(r.ClientId),
		Scopes:      r.Scopes,
		SecretRef:   proto.String(r.SecretRef),
		Labels:      labelsOf(r),
		DateUpdated: got.GetDateUpdated(),
	}.Build())

	return what, "changed", err
}

func applyHost(ctx context.Context, s *Server, r Resource, dry bool) (string, string, error) {
	what := r.Name
	at, err := tenantRef(ctx, s, r)
	if err != nil {
		return what, "", err
	}

	got, err := s.Ungated.Host().Get(ctx, app.HostGetRequest_builder{
		Ref:    app.HostRef_builder{Name: proto.String(r.Name)}.Build(),
		Select: app.HostSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		if dry {
			return what, "added", nil
		}
		_, err = s.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: at, Name: r.Name, Desc: r.Desc, Labels: labelsOf(r),
		}.Build())

		return what, "added", err
	}
	if err != nil {
		return what, "", err
	}
	if got.GetDesc() == r.Desc && declared(got.GetLabels()) {
		return what, "same", nil
	}
	if dry {
		return what, "changed", nil
	}

	_, err = s.Ungated.Host().Patch(ctx, app.HostPatchRequest_builder{
		Ref:         app.HostRef_builder{Id: got.GetId()}.Build(),
		Desc:        proto.String(r.Desc),
		Labels:      labelsOf(r),
		DateUpdated: got.GetDateUpdated(),
	}.Build())

	return what, "changed", err
}

func applyMailDomain(ctx context.Context, s *Server, r Resource, dry bool) (string, string, error) {
	what := r.Name
	at, err := tenantRef(ctx, s, r)
	if err != nil {
		return what, "", err
	}

	// Where an address at this domain is routed: one of the tenant's own
	// connections, **by name**, and empty routes nowhere -- which is what `Add`
	// already means by an empty one.
	ref := app.MailDomainRef_builder{
		At: app.MailDomainRefByAt_builder{Tenant: at, Name: proto.String(r.Name)}.Build(),
	}.Build()

	got, err := s.Ungated.MailDomain().Get(ctx, app.MailDomainGetRequest_builder{
		Ref:    ref,
		Select: app.MailDomainSelect_builder{All: proto.Bool(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		if dry {
			return what, "added", nil
		}
		_, err = s.Ungated.MailDomain().Add(ctx, app.MailDomainAddRequest_builder{
			Tenant: at, Name: r.Name, Desc: r.Desc, Provider: r.Routes, Labels: labelsOf(r),
		}.Build())

		return what, "added", err
	}
	if err != nil {
		return what, "", err
	}
	if got.GetProvider() == r.Routes && got.GetDesc() == r.Desc && declared(got.GetLabels()) {
		return what, "same", nil
	}
	if dry {
		return what, "changed", nil
	}

	_, err = s.Ungated.MailDomain().Patch(ctx, app.MailDomainPatchRequest_builder{
		Ref:         app.MailDomainRef_builder{Id: got.GetId()}.Build(),
		Desc:        proto.String(r.Desc),
		Provider:    proto.String(r.Routes),
		Labels:      labelsOf(r),
		DateUpdated: got.GetDateUpdated(),
	}.Build())

	return what, "changed", err
}

// tenantRef is the customer a resource hangs off, and a refusal when it names
// none: everything but a `Tenant` is inside one, and a resource that forgot to
// say which would otherwise land wherever the first query looked.
func tenantRef(ctx context.Context, s *Server, r Resource) (*app.TenantRef, error) {
	if r.Tenant == "" {
		return nil, errors.New("tenant")
	}
	if r.Name == "" {
		return nil, errors.New("name")
	}

	return app.TenantRef_builder{Alias: proto.String(r.Tenant)}.Build(), nil
}

// declared is whether a row already carries the label, so that a row somebody
// made by hand and then declared is **changed** rather than reported the same.
func declared(labels map[string]string) bool {
	_, ok := labels[Declared]

	return ok
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
