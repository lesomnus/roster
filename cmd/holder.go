// Package cmd, this file: who a key is for, and what it may call.
//
// Here rather than beside `roster key add` in `cli` because `Seed` names a
// holder the same way -- a deployment raised by a Go call is the same
// deployment -- and a helper written twice is a helper that drifts.
package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	entapikey "github.com/lesomnus/roster/internal/ent/apikey"
	app "github.com/lesomnus/roster/rstr"
)

// HolderNamed is a holder of this plane by alias, made if there is none.
//
// Made rather than refused, because a caller of this deployment's own is not a
// row somebody creates on purpose beforehand: `control key add custody` is the
// moment custody becomes one, and asking for two commands to express one
// intent is how a runbook grows a step nobody remembers.
//
// The tenant it goes in is the control plane's only one, made here if the
// database is new. There is nothing to choose: a control plane has one owner.
func HolderNamed(ctx context.Context, s *Server, alias string) (pdid.Id, error) {
	who, _, err := holderNamedIn(ctx, s.Ungated, alias)
	return who, err
}

// ControlHolder is a holder of this plane by alias, looked up and never made:
// [HolderNamed] for a caller that is about to take something away and must not
// add a row because somebody mistyped the name it is taking it from.
func ControlHolder(ctx context.Context, s *Server, alias string) (pdid.Id, error) {
	ts, err := s.Ungated.Tenant().List(ctx, app.TenantListRequest_builder{Size: 1}.Build())
	if err != nil {
		return pdid.Nil, err
	}
	if len(ts.GetItems()) == 0 {
		return pdid.Nil, status.Errorf(codes.NotFound, "%s: no such holder; this control plane has no owner yet", alias)
	}

	v, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Alias:  z.Ptr(alias),
				Tenant: app.TenantRef_builder{Id: ts.GetItems()[0].GetId()}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, fmt.Errorf("%s: %w", alias, err)
	}

	return pdid.From(v.GetId())
}

// holderNamedIn is [HolderNamed] on any stack, and the owner tenant it landed in.
//
// A stack rather than a [Server] so that `seedOperator` can hand it one rebound
// onto a transaction: every read and write here goes through it, and none
// around it, or the transaction would hold half of them.
func holderNamedIn(ctx context.Context, at app.Server, alias string) (pdid.Id, pdid.Id, error) {
	ts, err := at.Tenant().List(ctx, app.TenantListRequest_builder{Size: 1}.Build())
	if err != nil {
		return pdid.Nil, pdid.Nil, err
	}

	var tenant pdid.Id
	if vs := ts.GetItems(); len(vs) > 0 {
		tenant = MustFrom(vs[0].GetId())
	} else {
		v, err := at.Tenant().Add(ctx, app.TenantAddRequest_builder{
			Alias: "owner",
		}.Build())
		if err != nil {
			return pdid.Nil, pdid.Nil, err
		}

		tenant = MustFrom(v.GetId())
	}

	who, err := holderOf(ctx, at, tenant, alias)
	if err != nil {
		return pdid.Nil, pdid.Nil, err
	}

	return who, tenant, nil
}

// customerOf is one of a customer's people, by the tenant they are in and their
// alias, and is a refusal where there is no such person.
//
// Looked up and never created, which is the difference between this and
// [HolderNamed] beside it. The control plane has one tenant and a row there is
// one somebody names when they need it; the data plane has many, a customer's
// people are the customer's, and a command that made one by mentioning them
// would write rows into somebody else's tenant by typo.
//
// The tenant is an alias or an identifier, because both are things somebody has
// to hand: `roster tenant ls` prints the first and an app that anchors on this
// organisation was given the second.
func CustomerOf(ctx context.Context, s *Server, tenant string, alias string) (pdid.Id, error) {
	v, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Alias:  z.Ptr(alias),
				Tenant: TenantRef(tenant),
			}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, fmt.Errorf("@%s/%s: %w", tenant, alias, err)
	}

	return pdid.From(v.GetId())
}

// tenantRef is what somebody typed, as the reference an Rpc takes.
//
// An identifier if it parses as one and an alias otherwise. There is no
// ambiguity to resolve: an alias is `[a-z0-9-]` shaped and a UUID is not
// something anybody chooses as one.
func TenantRef(v string) *app.TenantRef {
	if k, err := pdid.Parse(v); err == nil {
		return app.TenantRef_builder{Id: k.Bytes()}.Build()
	}

	return app.TenantRef_builder{Alias: z.Ptr(v)}.Build()
}

func holderOf(ctx context.Context, at app.Server, in pdid.Id, alias string) (pdid.Id, error) {
	v, err := at.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Alias:  z.Ptr(alias),
				Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	if err == nil {
		return pdid.From(v.GetId())
	}

	w, err := at.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	if status.Code(err) == codes.AlreadyExists {
		// Another process made it between the read and the write -- an init
		// container and a shell running the same command at once. Theirs is
		// the row, and it is read rather than refused.
		v, err := at.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{
				Slug: app.HolderRefBySlug_builder{
					Alias:  z.Ptr(alias),
					Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
				}.Build(),
			}.Build(),
		}.Build())
		if err != nil {
			return pdid.Nil, err
		}

		return pdid.From(v.GetId())
	}
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(w.GetId())
}

func MustFrom(b []byte) pdid.Id {
	k, _ := pdid.From(b)

	return k
}

// splitMethods is the `--allow` list, however it was written.
//
// Every occurrence of the flag, and every comma inside one, so these are the
// same key:
//
//	--allow a,b --allow c
//	--allow a --allow b --allow c
//	--allow 'a, b, c'
//
// The flag is `flg.Strings` and not `flg.String`, which is the whole of the
// fix and was the whole of the bug. A scalar flag takes the **last**
// occurrence -- which is right for `--config` and every other *choose one*
// flag, and silently wrong for a list. `roster key add --service kamino
// --allow /roster.VouchService/Verify --allow /roster.HolderService/Get` -- as
// `roster control key add` was spelled then -- minted
// a key allowing the second and nothing else, and the only sign was the line
// this prints saying `allowing 1 method(s)`.
//
// It was documented that way in another app, which is how it was found: an
// operator following a runbook gets a key that fails on its first call.
//
// xli already had the type. `flg.Strings` is `Multi[string, StringParser]` and
// appends per occurrence -- so nothing was missing anywhere but here.
//
// The spacing is tolerated because a list somebody wrapped for a runbook has
// some.
func SplitMethods(vs []string) []string {
	var out []string
	for _, v := range vs {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}

	return out
}

// Widest says so when a key's methods reach the one read that is wider than
// every other one put together.
//
// # Why this and nothing else
//
// A key is the deployment's, and the deployment is every tenant in it -- so
// `cmd.Policy.Where` answers `frame.Everything` and the wall narrows nothing.
// That is the design and it is right: a key allowed `/roster.HolderService/List`
// reads every customer's people, and a service that manages customers has to.
//
// `AuditService` is the same property with a different magnitude. `Audit.value`
// is the row as each write left it, so one method answers **every table's
// contents, across every tenant, across all time** -- including rows long since
// deleted, since nothing erases a trail row. It is the single widest read this
// app has, and `cmd/trailkey_test.go` is what says so.
//
// Said rather than refused. A compliance exporter is a real service and this is
// the method it needs; what is wrong is granting it by reaching for `*` and not
// noticing. So this is the sentence that makes somebody notice, once, at the
// moment they could still choose otherwise.
func Widest(methods []string) string {
	wide := []struct {
		method string
		why    string
	}{
		{app.AuditService_List_FullMethodName, ReadsTheTrail},
		{app.AuditService_Get_FullMethodName, ReadsTheTrail},
		{app.VouchService_Accept_FullMethodName, MintsForAnybody},
	}

	for _, held := range methods {
		for _, v := range wide {
			if !frame.Covers(held, v.method) {
				continue
			}

			return "NOTE: `" + held + "` " + v.why
		}
	}

	return ""
}

const ReadsTheTrail = "reaches the audit trail, which holds the contents of every\n" +
	"write in this deployment, in every tenant, for as long as the retention policy\n" +
	"keeps them. A key is not walled by tenant. Grant it only to something that has\n" +
	"to read other customers' history."

// accepts is the other one, and it is the sharper of the two.
//
// `Vouch.Accept` mints a delegation for **anybody**, with no secret and no
// proof: it exists so that a front door which ran an OIDC flow can say who it
// checked, and the grant is the whole of the control. Reading a trail is
// reading; this is acting as somebody.
const MintsForAnybody = "mints a credential for anybody, on the caller's word.\n" +
	"`Vouch.Accept` is for a front door that did its own checking -- an OIDC flow --\n" +
	"and it verifies nothing itself. An app that checks passwords through `Verify`\n" +
	"does not need it, and should not have it."

// There was a third, `BecomesAnOperator`, and it is gone because the hole it
// warned about closed.
//
// It was found by pointing `roster issue` at the control port:
// `IssueService.IssuePassword` wrote through the **generated** `Credential`
// verbs, so the reach rule never ran and a key whose methods named it handed
// out any operator's password. A printed NOTE was the whole mitigation.
//
// That verb is `Credential.Issue` now and goes through `server/core`'s own
// write, so `mayReach` runs: a caller may write the credential of somebody who
// holds nothing -- a new operator, which is what the act is for -- and of
// somebody whose grants their own bindings cover, and of nobody else. A key
// holds no bindings, so it reaches exactly the first case, where there is
// nothing to become. The wiring says it now instead of the warning.

// listKeys writes one plane's keys, in the shape `roster key list` prints them.
//
// The tenant is printed as well as the alias, which the control plane's version
// did not need and this one does: an alias is unique within a tenant, so
// `@alice/laptop` is ambiguous across customers and `@newco/alice/laptop` is
// not.
func ListKeys(ctx context.Context, w io.Writer, db *ent.Client, _ string) error {
	// Erased rows left out, which reading ent directly does not do for you:
	// erasure is applied by the servers and this is under them. A `list` that
	// showed a key somebody had just revoked would be worse than one that is a
	// moment behind.
	vs, err := db.ApiKey.Query().
		Where(entapikey.DateErasedIsNil()).
		WithHolder(func(q *ent.HolderQuery) { q.WithTenant() }).
		All(ctx)
	if err != nil {
		return err
	}

	for _, v := range vs {
		who := "?"
		if h := v.Edges.Holder; h != nil {
			who = h.Alias
			if t := h.Edges.Tenant; t != nil {
				who = t.Alias + "/" + who
			}
		}

		used := "never"
		if v.DateUsed != nil {
			used = v.DateUsed.Format(time.RFC3339)
		}

		fmt.Fprintf(w, "%s\t@%s/%s\tused=%s\t%s\n",
			pdid.Id(v.Id), who, v.Alias, used, strings.Join(v.Methods, ","))
	}

	return nil
}

// keyPlane is the server holding the key with this identifier.
//
// Both planes are asked because both mint them and nothing about an identifier
// says which one it came from -- they are `ApiKey` rows in the same domain from
// the same generator. The control plane first, since that is where a key
// somebody is revoking in a hurry usually is.
//
// A key on neither is an error. It was a silent success, which is the direction
// that matters for this particular act: somebody stopping a credential they
// believe is leaked must not be told it stopped when it did not.
func KeyPlane(ctx context.Context, s *Server, k pdid.Id) (*Server, error) {
	for _, at := range []*Server{s.Control, s} {
		n, err := at.Ent.ApiKey.Query().
			Where(entapikey.IdEQ(uuid.UUID(k)), entapikey.DateErasedIsNil()).
			Count(ctx)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			return at, nil
		}
	}

	return nil, fmt.Errorf("--id: no key %s on either plane", k)
}

// keyOwners answers which control-plane holder a deployment key hangs off, for
// `server/core`'s [core.Borrower]: the identity a `Nomination` is found by, on
// the other database.
//
// A pointer filled in after the fact because the data plane's stack is built
// before the control plane that answers it (`build`). Unfilled is a deployment
// with no control plane, where there are no deployment keys to ask about.
type keyOwners struct{ db *ent.Client }

func (o *keyOwners) of(ctx context.Context, k pdid.Id) (pdid.Id, error) {
	if o.db == nil {
		return pdid.Nil, status.Error(codes.Unimplemented, "this deployment has no control plane, so no deployment keys")
	}

	who, err := o.db.ApiKey.Query().
		Where(entapikey.IdEQ(uuid.UUID(k)), entapikey.DateErasedIsNil()).
		QueryHolder().
		OnlyId(ctx)
	if ent.IsNotFound(err) {
		return pdid.Nil, status.Error(codes.NotFound, "no such key")
	}
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(who[:])
}
