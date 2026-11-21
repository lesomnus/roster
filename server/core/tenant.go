package core

import (
	"context"
	"strings"
	"time"

	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/bare"
	"github.com/lesomnus/roster/server/keys"
)

// A row that names two tenants, refused.
//
// # What was wrong
//
// The wall reaches a tenant by **one** path per entity -- `holder.tenant` for a
// membership, `tenant` for a team. A row that names two things reaches two, and
// nothing compared them. So this was written and accepted:
//
//	SiteMembership{holder: somebody in contoso, site: a site of fabrikam's}
//
// and whichever path the wall happened to take decided who could see it. One
// tenant read a row naming the other's, which is the one thing the wall exists
// to make impossible. It was found by writing it; nothing refused, nothing
// logged.
//
// # Why the schema cannot say it
//
// Each edge is valid on its own -- that holder exists, that site exists -- and
// there is no constraint over the pair, because the thing they must agree about
// is not on either row. It is a judgement about a combination, which is what
// this package is for.
//
// # Why it is not the wall's job either
//
// The wall is a predicate on reads. This is a **write** naming something the
// writer may not have been able to read, and by the time a read is narrowed the
// row already exists. Refusing it at the write is the only place the answer
// helps.
//
// A caller who cannot see one of the rows gets `NotFound` from the lookup and
// the write is refused, which is also right: you may not point at what you
// cannot see.

// tenantsAgree refuses when the tenants it was given are not all the same.
//
// `pdid.Nil` is "nothing was named" and is skipped, since an optional edge that
// was left out says nothing about which tenant this belongs to.
func tenantsAgree(field string, vs ...pdid.Id) error {
	var was pdid.Id
	for _, v := range vs {
		if v == pdid.Nil {
			continue
		}
		if was == pdid.Nil {
			was = v

			continue
		}
		if was != v {
			return pderr.Invalidf(field,
				"this names rows of two tenants, and a row belongs to one; "+
					"the wall reaches a tenant by one path and the other would be invisible to it")
		}
	}

	return nil
}

// The tenant each kind of reference belongs to, read through this stack so that
// the wall applies: a caller who cannot see the row cannot point at it either.
//
// A nil reference is `pdid.Nil` rather than an error. Whether an edge is
// required is the schema's to say, and saying it twice is how the two come to
// disagree.

func (s Core) tenantOfHolder(ctx context.Context, ref *app.HolderRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    ref,
		Select: app.HolderSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetTenant().GetId())
}

func (s Core) tenantOfSite(ctx context.Context, ref *app.SiteRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Site().Get(ctx, app.SiteGetRequest_builder{
		Ref:    ref,
		Select: app.SiteSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetTenant().GetId())
}

func (s Core) tenantOfTeam(ctx context.Context, ref *app.TeamRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Team().Get(ctx, app.TeamGetRequest_builder{
		Ref:    ref,
		Select: app.TeamSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetTenant().GetId())
}

func (s Core) tenantOfRole(ctx context.Context, ref *app.RoleRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Role().Get(ctx, app.RoleGetRequest_builder{
		Ref:    ref,
		Select: app.RoleSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetTenant().GetId())
}

func (s Core) tenantOfGroup(ctx context.Context, ref *app.GroupRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}

	v, err := s.Next().Group().Get(ctx, app.GroupGetRequest_builder{
		Ref:    ref,
		Select: app.GroupSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetTenant().GetId())
}

// tenantOfRef is a `TenantRef` as an identifier, which is the one case that
// needs no read: a tenant names itself.
func (s Core) tenantOfRef(ctx context.Context, ref *app.TenantRef) (pdid.Id, error) {
	if ref == nil {
		return pdid.Nil, nil
	}
	if b := ref.GetId(); len(b) > 0 {
		return pdid.From(b)
	}

	v, err := s.Next().Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    ref,
		Select: app.TenantSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(v.GetId())
}

// coreTenant is the layer over the generated `TenantService`, for its one
// overlay.
type coreTenant struct {
	Core
	app.TenantServiceServer
}

func (s Core) Tenant() app.TenantServiceServer { return coreTenant{s, s.Next().Tenant()} }

// Update is the narrow write over a tenant: name, note, labels and the
// settings, and never the alias or the identifier. See `tenant_svc.ext.proto`.
//
// Refused outright on a row a file declared, which `declared.go` argues for --
// and argues hardest here, since the settings are the security-relevant half:
// a password switch flipped in a console that the next start flips back is the
// "right on Tuesday, wrong on Wednesday" that file is about.
func (s coreTenant) Update(ctx context.Context, req *app.TenantUpdateRequest) (*app.Tenant, error) {
	// The settings are how everybody in the tenant signs in, so they are
	// written by somebody who holds this across it ([Core.acrossTheTenant]).
	if err := s.acrossTheTenant(ctx, app.TenantService_Update_FullMethodName); err != nil {
		return nil, err
	}

	got, err := s.TenantServiceServer.Get(ctx, app.TenantGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.TenantSelect_builder{Labels: z.Ptr(true), Config: z.Ptr(true), DateDestroyed: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	// What a tenant says about itself is what destroying it took off the row,
	// and is not written back ([gone]).
	if at := got.GetDateDestroyed(); at != nil {
		return nil, gone("ref", at.AsTime())
	}
	if err := s.mayWriteDeclared(ctx, "ref", got.GetLabels()); err != nil {
		return nil, err
	}

	patch := app.TenantPatchRequest_builder{
		Ref:         req.GetRef(),
		DateUpdated: req.GetDateUpdated(),
	}
	if req.HasName() {
		patch.Name = z.Ptr(req.GetName())
	}
	if req.HasDesc() {
		patch.Desc = z.Ptr(req.GetDesc())
	}
	// A map has no presence: given, it replaces; empty, it is left as it is.
	if len(req.GetLabels()) > 0 {
		patch.Labels = req.GetLabels()
	}
	// A message does have presence, so an absent one is *leave it* and an empty
	// one is *nothing is set* -- which for `password` means back to the default,
	// and is a thing a caller may want to say.
	if req.HasConfig() {
		cfg, err := configOf(req.GetConfig())
		if err != nil {
			return nil, err
		}
		if err := s.maySlack(ctx, got.GetConfig(), cfg); err != nil {
			return nil, err
		}
		patch.Config = cfg
	}

	return s.TenantServiceServer.Patch(ctx, patch.Build())
}

// Patch is the general write, which no port serves and a command at the box
// reaches (`roster tenant patch`), with the two things about a tenant's row
// that hold whoever is writing.
//
// # A destroyed row says nothing about itself again
//
// Destroying a tenant blanks its name, its note, its labels and its settings
// (`server/forget`), and they are not written back: [gone]. What is still
// written on such a row is what a destruction is made of, by the deployment
// itself -- the alias given back once its hold is over, and an operator's
// hold, which keeps what the trail has of the tenant and stops a destruction
// that is under way. Those are the row's other columns, and are let through.
//
// # A declared tenant is not renamed
//
// A file finds its tenant by alias (`cmd/resources.go`). Every other field of
// a declared row is the box's to write, because whoever is at a shell has the
// file too ([Core.mayWriteDeclared]); for the alias that is not enough, since
// the file would have to be changed in the same moment and nothing makes it
// so. Renamed here and not there, the next start found nobody of the old
// alias, made a second tenant, and stopped at the first name both declared
// (#279). So it is refused while the row is declared, whoever asks. Not of a
// destroyed row: nothing declares one any more, and its alias is the
// deployment's to give back.
func (s coreTenant) Patch(ctx context.Context, req *app.TenantPatchRequest) (*app.Tenant, error) {
	got, err := s.TenantServiceServer.Get(ctx, app.TenantGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.TenantSelect_builder{Alias: z.Ptr(true), Labels: z.Ptr(true), DateDestroyed: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	if at := got.GetDateDestroyed(); at != nil {
		for _, v := range []struct {
			field   string
			written bool
		}{
			{"name", req.HasName()},
			{"desc", req.HasDesc()},
			{"labels", len(req.GetLabels()) > 0},
			{"config", req.HasConfig()},
		} {
			if v.written {
				return nil, gone(v.field, at.AsTime())
			}
		}

		return s.TenantServiceServer.Patch(ctx, req)
	}

	// Folded as the write below folds it, so that the alias it has, spelled
	// in capitals, is no change.
	if from, ok := got.GetLabels()[declaredBy]; ok && req.HasAlias() &&
		strings.ToLower(strings.TrimSpace(req.GetAlias())) != got.GetAlias() {
		return nil, status.Errorf(codes.FailedPrecondition,
			"alias: @%s is declared in %s, and a file finds its tenant by this alias: renamed here, the next start "+
				"makes a second tenant called %s. Take it out of the file and take the label %q off this row first; "+
				"then rename it, and declare it again by its new alias if a file is to own it",
			got.GetAlias(), from, got.GetAlias(), declaredBy)
	}

	return s.TenantServiceServer.Patch(ctx, req)
}

// gone is the refusal of a write into a tenant that is destroyed.
//
// `FailedPrecondition`, as a restore of one is: nothing is wrong with the
// request, and nothing about who is asking would change the answer.
func gone(field string, at time.Time) error {
	return status.Errorf(codes.FailedPrecondition,
		"%s: this tenant was destroyed on %s, and nothing is written into a tenant that is gone: what it held is not "+
			"there to add to. A customer that comes back is a new tenant (`roster tenant add`), under its old alias "+
			"once that is free (`roster forget --reuse`)", field, at.Format(time.RFC3339))
}

// living is the server this layer is built over, with one question in front of
// it: whether the tenant a row is being added under is still there.
//
// # What was wrong
//
// A destroyed tenant is a row that keeps an identifier for the trail and, for
// a while, an alias so that nobody else is that name (`server/forget`).
// Nothing asked whether a tenant was one before writing into it. Nobody of a
// closed tenant gets in, so the callers left were the deployment's own -- an
// operator at `admin.addr`, a command at the box, a file -- and for them a
// person, a host, a site and a role were each added to a destroyed tenant, OK.
// A person added there was forgotten by the next sweep. A host was not: no
// sweep visits a destroyed tenant again, and the name went on answering for
// one that is gone (#279).
//
// # Why it is here, and not on each verb
//
// Because there was no one place. Each kind's `Add` is its own, in its own
// file, with its own rules, and several of them make a row of another kind on
// the way -- a tenant's first person, a group's role, a directory's person --
// so a rule written on the verbs is written a dozen times, and the verb that
// forgets is the hole. What every such write has in common is where it ends:
// the `Add` of the server this layer is over. So the question is asked there,
// once, of every kind whose row names its tenant ([Core] is built over this
// in [New], and again on a transaction in [Core.over]); and
// `TestNothingIsWrittenIntoATenantThatIsGone` reads the kinds off the schema,
// so that the next one is asked too.
//
// The rows that name a person, a role or a group and not a tenant are not
// asked. In a tenant whose destruction is finished there is nobody and
// nothing left for one to name. In one whose destruction is under way there
// may be, for as long as the person is, and such a row goes with them.
//
// # What it leaves alone
//
// A closed tenant. Closing shuts a tenant's people out and destroys nothing,
// and whoever runs the deployment still writes in one, on purpose: that is
// how a customer is put right before it is restored. And erasing: taking a
// row out of a tenant that is going is what destroying it is.
type living struct{ app.Overlay }

// alive is `next` with [living] in front of it, once.
func alive(next app.Server) app.Server {
	if _, ok := next.(living); ok {
		return next
	}

	return living{app.NewOverlay(next)}
}

var (
	_ app.Server               = living{}
	_ enttx.Binder[app.Server] = living{}
)

// WithDriver is what nothing inherits; see `CLAUDE.md`, "Writing a layer".
func (s living) WithDriver(drv dialect.Driver) (app.Server, error) {
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	return living{app.NewOverlay(next)}, nil
}

// into refuses a row under a tenant that is destroyed.
//
// A tenant that is not there, or that this caller does not see, is not this
// rule's to say: the write answers that in its own words. Nor is a request
// that names none, which is filled from the frame below -- the caller's own
// tenant, and nobody of a destroyed one arrives.
func (s living) into(ctx context.Context, at *app.TenantRef) error {
	if at == nil {
		return nil
	}

	v, err := s.Next().Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref:    at,
		Select: app.TenantSelect_builder{DateDestroyed: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return nil
	}
	if when := v.GetDateDestroyed(); when != nil {
		return gone("tenant", when.AsTime())
	}

	return nil
}

// under is [living.into] as the rule of an `Add`, for every request that
// names the tenant it writes into. It answers no request of its own, which is
// the one it was handed.
func under[R interface{ GetTenant() *app.TenantRef }](s living) func(context.Context, R) (R, error) {
	return func(ctx context.Context, req R) (R, error) {
		var same R

		return same, s.into(ctx, req.GetTenant())
	}
}

// Every kind whose row names its tenant: the ones `docs/entity.md` lists as
// walled by a `tenant` edge.

func (s living) Holder() app.HolderServiceServer {
	return bare.HolderWithAddRule(s.Next().Holder(), under[*app.HolderAddRequest](s))
}

func (s living) Site() app.SiteServiceServer {
	return bare.SiteWithAddRule(s.Next().Site(), under[*app.SiteAddRequest](s))
}

func (s living) Team() app.TeamServiceServer {
	return bare.TeamWithAddRule(s.Next().Team(), under[*app.TeamAddRequest](s))
}

func (s living) Role() app.RoleServiceServer {
	return bare.RoleWithAddRule(s.Next().Role(), under[*app.RoleAddRequest](s))
}

func (s living) Group() app.GroupServiceServer {
	return bare.GroupWithAddRule(s.Next().Group(), under[*app.GroupAddRequest](s))
}

func (s living) Host() app.HostServiceServer {
	return bare.HostWithAddRule(s.Next().Host(), under[*app.HostAddRequest](s))
}

func (s living) MailDomain() app.MailDomainServiceServer {
	return bare.MailDomainWithAddRule(s.Next().MailDomain(), under[*app.MailDomainAddRequest](s))
}

func (s living) HostProof() app.HostProofServiceServer {
	return bare.HostProofWithAddRule(s.Next().HostProof(), under[*app.HostProofAddRequest](s))
}

func (s living) Nomination() app.NominationServiceServer {
	return bare.NominationWithAddRule(s.Next().Nomination(), under[*app.NominationAddRequest](s))
}

func (s living) Connection() app.ConnectionServiceServer {
	return bare.ConnectionWithAddRule(s.Next().Connection(), under[*app.ConnectionAddRequest](s))
}

// NotErased is what is said to anybody who asks for a tenant to be erased:
// that none is, and what ends one instead.
const NotErased = "a tenant is not erased. It is closed, which shuts its people out and is undone while tenant.restore_within lasts " +
	"(TenantService/Close; `roster tenant close @tenant`), and a closed one is destroyed by whoever runs this deployment, " +
	"from a shell on the box (`roster forget @tenant`)"

// Erase is not served: a tenant is not erased, by anybody, through any stack.
//
// # What it was
//
// `Tenant` declares a hard erase, and the generated one is a `DELETE` of the
// row. Every row of a tenant is keyed to it, and `Add` makes a tenant with a
// person and two roles; a destroyed one keeps the blank rows of its people.
// So the delete met a foreign key at every point of a tenant's life, and what
// a caller was answered was the database's own words about that: `Unknown`,
// *FOREIGN KEY constraint failed*, `500` over HTTP. The gate refuses the
// method in a sentence, so that was the answer wherever there is no gate --
// `admin.addr`, and `roster tenant erase` at the box (#279).
//
// # Why it refuses rather than doing what was meant
//
// What was meant is one of two things, and they are different acts with
// different words. Closing is one row and can be undone
// ([coreTenant.Close]). Destroying forgets every person and removes every
// other row, through ent and past every server, from a shell or a sweep
// (`server/forget`): it is not reachable over the wire on purpose. A verb
// called *erase* that did either would be the other one's surprise.
//
// `Unimplemented`, the gate's own code for it: this is not about who is
// asking, and no credential changes it.
func (s coreTenant) Erase(context.Context, *app.TenantRef) (*app.TenantEraseResponse, error) {
	return nil, status.Error(codes.Unimplemented, NotErased)
}

// Everyverb is what the first role of a tenant is called, and EveryMethod is
// what it holds.
//
// A name somebody will read in a list of roles and understand without opening
// it, and a **pattern** rather than an enumeration: a list written the day a
// tenant is made is what existed that day, and its administrator is the one
// person who must not have to notice a release adding a method.
//
// `/roster.*/*` and not `/*.*/*`, which would take in payday's own -- the same
// line `roster init` draws for the operator's own role, and `cmd` takes these
// rather than spelling them a second time.
const (
	Everyverb   = "everything"
	EveryMethod = "/roster.*/*"

	// Administers is what the first holder of a tenant is called. A name
	// somebody can guess from outside, because a caller that has just made a
	// tenant has to be able to name its administrator without being told.
	Administers = "admin"
)

// Add is a tenant **and somebody who can administer it**.
//
// # Why the generated verb does more than the row
//
// Because the row on its own is not a thing anybody wanted. A customer was four
// writes -- the tenant, the first holder, a role that administers it, the
// binding -- and what the four left between the first and the last was a tenant
// **nobody could do anything in**. The only way to finish one was a roster
// operator reaching inside through `admin.addr`, the port where standing comes
// from the port rather than from a role, which is the shape #26 is about.
//
// So the four are one act on the verb that already means *make a tenant*,
// rather than a second verb meaning *make a tenant properly*. `docs/usage/customers.md`
// argued the opposite once -- no fifth RPC, because a composite would be a
// fifth thing to hold to the rules -- and that is about a **caller** composing
// them. Every write below goes back through `s.Core`, this layer, so a role
// naming methods the caller does not hold meets `mayGrant` here exactly as it
// would have arriving on its own.
//
// # And what it does not do
//
// Hand out a way in. The first holder gets no password here, because a verb
// that answers with a row cannot answer with a secret as well -- and the verb
// that does is `Credential.Issue`, which needs nothing from this one: the
// holder is `@<tenant>/admin`, by slug, and a caller that has just made the
// tenant knows both halves.
//
//	roster tenant add @newco
//	roster vouch reset @newco/admin
//
// # The control plane is not a customer
//
// There, the one tenant is the deployment itself and its first holder is
// `roster init`'s business -- named by `--operator`, bound by `allow`, and
// given a password in the same act. `WithPrefix` is what tells the two apart,
// the same fact `ApiKey.Issue` and `Credential.Issue` read.
//
// # One transaction
//
// Five writes, and a refusal at any of them leaves nothing: an alias already
// taken, a role wider than the caller holds. Written one at a time a refusal
// would leave a tenant nobody can get into whose alias cannot be used again --
// `roster init`'s failure before #20, at a larger size. A caller who has
// already arranged a transaction -- an RPC that writes several rows as one --
// arrives with no driver and runs inside theirs.
func (s coreTenant) Add(ctx context.Context, req *app.TenantAddRequest) (*app.Tenant, error) {
	if err := unlived(req); err != nil {
		return nil, err
	}
	req, bad := addWithConfig(req)
	if bad != nil {
		return nil, bad
	}
	if err := s.maySlack(ctx, nil, req.GetConfig()); err != nil {
		return nil, err
	}
	if s.prefix != keys.PrefixTenant {
		return s.TenantServiceServer.Add(ctx, req)
	}

	var out *app.Tenant

	// `below` and not `at.Tenant()`: the row is the write this layer is
	// **in** the middle of, so sending it back through the layer would be
	// this method calling itself. The three after it do go through, because
	// each is a write the layer has rules about.
	run := func(at app.Server, below app.TenantServiceServer) error {
		t, err := below.Add(ctx, req)
		if err != nil {
			return err
		}

		h, err := at.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: t.GetId()}.Build(),
			Alias:  Administers,
		}.Build())
		if err != nil {
			return err
		}

		r, err := at.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: t.GetId()}.Build(),
			Alias:   Everyverb,
			Desc:    "Everything roster serves about this tenant, including what a later release adds.",
			Methods: []string{EveryMethod},
		}.Build())
		if err != nil {
			return err
		}

		if _, err := at.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
		}.Build()); err != nil {
			return err
		}

		// And what everybody of it does to their own account, which nobody is
		// bound: see `own.go`.
		if _, err := at.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant:  app.TenantRef_builder{Id: t.GetId()}.Build(),
			Alias:   Owns,
			Desc:    "What somebody of this tenant does to their own account. Everybody holds it, over what is theirs; take a method out to turn it off for everybody.",
			Methods: Owning,
		}.Build()); err != nil {
			return err
		}

		out = t

		return nil
	}

	if s.drv == nil {
		if err := run(s.Core, s.TenantServiceServer); err != nil {
			return nil, err
		}

		return out, nil
	}

	drv, tx, err := dialect.BeginTx(ctx, s.drv)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// This layer again over a rebound one below it, which is what
	// [Core.WithDriver] does and what `only` does beside it: the rules carry
	// over and the driver does not, so nothing inside opens a second
	// transaction in this one.
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}
	if err := run(s.over(next, drv), next.Tenant()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return out, nil
}
