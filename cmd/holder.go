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
	"strconv"
	"strings"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"uuid"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	entapikey "github.com/lesomnus/roster/internal/ent/apikey"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/keys"
)

// HolderNamed is a holder of this plane by alias, made if there is none.
//
// Made rather than refused, because a caller of this deployment's own is not a
// row somebody creates on purpose beforehand: `control key add loft` is the
// moment Loft becomes one, and asking for two commands to express one
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

// tenantRef is what somebody typed, as the reference an RPC takes.
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
// flag, and silently wrong for a list. `roster key add --service loft
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

// MethodsOf is a list of methods read from the command line, by
// [SplitMethods], each held to the form the gate matches a caller's methods
// by (`frame.Covers`): `/<package>.<Service>/<Method>`, any of the three a
// whole `*`, or a service with no package written as it is. `flag` is how the
// list was typed, for the refusal to name.
//
// A word in no such form matches nothing, and was taken: `roster key add …
// --allow HolderService/Get` printed a key *allowing 1 method(s)* that calls
// nothing (#284). The gate compares a word it cannot read as a method with
// `==`, so a part that holds a `*` beside other letters, a missing slash, a
// part left empty or a space inside are all words no call is ever named, and
// a key that silently does not work is worse to debug than being told now.
func MethodsOf(flag string, vs []string) ([]string, error) {
	out := SplitMethods(vs)
	for _, v := range out {
		if !methodForm(v) {
			return nil, fmt.Errorf("%s %s: not a method, which is written in full -- /<package>.<Service>/<Method>, as /roster.HolderService/Get -- with * for a whole part, as /roster.HolderService/*", flag, v)
		}
	}

	return out, nil
}

// methodForm is whether `v` is a method or a pattern some method could be:
// what `frame.Covers` reads, and the literal it falls back on.
func methodForm(v string) bool {
	if strings.ContainsAny(v, " \t\r\n") {
		return false
	}

	rest, ok := strings.CutPrefix(v, "/")
	if !ok {
		return false
	}
	full, method, ok := strings.Cut(rest, "/")
	if !ok || !wholePart(method) || strings.Contains(method, "/") {
		return false
	}

	i := strings.LastIndex(full, ".")
	if i < 0 {
		// A service with no package: no pattern names one, and it matches
		// as the literal it is, so it holds no `*`.
		return full != "" && !strings.Contains(full, "*")
	}

	return wholePart(full[:i]) && wholePart(full[i+1:])
}

// wholePart is a part of a method that is a name or `*`, and not empty or a
// name with a `*` in it, which `frame.Covers` takes as letters.
func wholePart(v string) bool {
	return v == "*" || (v != "" && !strings.Contains(v, "*"))
}

// Widest says so when what was just minted reaches further than its methods
// read: the trail, a credential for anybody, a link to anybody's account, a
// delegation of anything its person may call -- or, of a key that lists all
// of roster, that it is all of roster.
//
// # By the kind of key it is
//
// `kind` is how the key begins -- [keys.PrefixDeployment] or
// [keys.PrefixTenant], or the token itself, which begins with one -- because
// what a method reaches is the kind's and not the method's:
//
//   - A **deployment's** key is every tenant in it: `cmd.Policy.Where` answers
//     `frame.Everything` and the wall narrows nothing. That is the design and
//     it is right -- a key allowed `/roster.HolderService/List` reads every
//     customer's people, and a service that manages customers has to.
//   - A **customer's** -- a key for one of a tenant's people, or a delegation
//     -- is answered as that person, behind their tenant's wall, and calls what
//     they hold.
//
// It was one sentence for both, the deployment's, and `roster key add` mints
// the other kind: an administrator minting a key for their own tenant's
// exporter was told it read every customer's history (#274). A sentence that
// is wrong half the times it is printed is not read the other half.
//
// # Why the trail
//
// `AuditService` is the same property as any other read with a different
// magnitude. `Audit.value` is the row as each write left it, so one method
// answers **every table's contents, across all time** -- including rows long
// since deleted, since nothing erases a trail row -- and on a deployment's key
// across every tenant. It is the single widest read this app has, and
// `cmd/trailkey_test.go` is what says so.
//
// Said rather than refused. A compliance exporter is a real service and this is
// the method it needs; what is wrong is granting it by reaching for `*` and not
// noticing. So this is the sentence that makes somebody notice, once, at the
// moment they could still choose otherwise.
func Widest(kind string, methods []string) string {
	wide := []struct {
		method string

		// What is said of a customer's, and of a deployment's.
		theirs, ours string
	}{
		{app.AuditService_List_FullMethodName, ReadsItsTenantsTrail, ReadsTheTrail},
		{app.AuditService_Get_FullMethodName, ReadsItsTenantsTrail, ReadsTheTrail},
		{app.VouchService_Accept_FullMethodName, MintsForAnybody, MintsForAnybody},
		{app.VouchService_Link_FullMethodName, LinksAnybody, LinksAnybodyAnywhere},
		{app.DelegationService_Issue_FullMethodName, MintsDelegations, MintsDelegationsAnywhere},
	}

	deployments := strings.HasPrefix(kind, keys.PrefixDeployment)

	// All of roster is said as what it is, as `RoleService/HandsOver` answers
	// it: of `/roster.*/*` this said the trail's note, of the pattern, as if
	// it were `AuditService/List`.
	for _, held := range methods {
		if !core.CoversThePage(held) {
			continue
		}

		why := ListsEverything
		if deployments {
			why = ListsEverythingAnywhere
		}

		return "NOTE: `" + held + "` " + fmt.Sprintf(why, core.GrantsOfThePage(kind, app.RoleHeld_ROLE_HELD_ACROSS_THE_TENANT))
	}

	for _, held := range methods {
		for _, v := range wide {
			if !frame.Covers(held, v.method) {
				continue
			}

			why := v.theirs
			if deployments {
				why = v.ours
			}

			// The method the note is about, and the pattern that named it:
			// `/roster.AuditService/*` does not read the trail by its name.
			named := "`" + v.method + "`"
			if held != v.method {
				named += " (named by `" + held + "`)"
			}

			return "NOTE: " + named + " " + why
		}
	}

	return ""
}

// ListsEverything is said of a customer's key that lists every method roster
// serves, with how many grants of `docs/usage/wide-grants.md` that is.
const ListsEverything = "is every method roster serves, and every one a later release adds, the day it is\n" +
	"added: whatever whoever it is for holds, and with it the %d grants\n" +
	"docs/usage/wide-grants.md lists. Grant it only to something that is to act as them\n" +
	"in everything."

// ListsEverythingAnywhere is that of a deployment's key, which is every tenant.
const ListsEverythingAnywhere = "is every method roster serves, and every one a later release adds, the day it is\n" +
	"added, in every tenant: the %d grants docs/usage/wide-grants.md lists, of every\n" +
	"customer. Grant it only to something that is to run this deployment."

// ReadsTheTrail is said of a deployment's key.
const ReadsTheTrail = "reaches the audit trail, which holds the contents of every\n" +
	"write in this deployment, in every tenant, for as long as the retention policy\n" +
	"keeps them. A deployment's key is not walled by tenant. Grant it only to\n" +
	"something that has to read other customers' history."

// ReadsItsTenantsTrail is said of a customer's, which reads its own tenant's
// and nobody else's -- and that only where whoever it is for is allowed the
// read across the tenant (`ofTheWholeTenant`).
const ReadsItsTenantsTrail = "reaches the audit trail of its tenant, which holds the\n" +
	"contents of every write there, of rows long since erased too, for as long as\n" +
	"the retention policy keeps them. It reads it where whoever it is for is allowed\n" +
	"to across the tenant. Grant it only to something that has to read that history."

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

// LinksAnybody is the third, and the one that does not look like it.
//
// `Vouch.Link` mints a way into somebody's account and answers with it -- to
// its **caller**, since roster sends nothing and whoever asked is who mails
// it. `Vouch.Redeem` spends one. So whoever may call both needs no mailbox:
// they ask for an administrator's link and spend it themselves, and are the
// administrator (#274). Neither is held to the reach rule every other write
// of a way in is, and cannot be: a front door holds nothing, and has to be
// able to mail a link to anybody who signs in at it.
//
// So the grant is the whole of the control, as it is for `Accept`, and this is
// the sentence that says what is being granted. It was said nowhere: "may
// send recovery links" reads like a helpdesk's permission, and is the tenant.
const LinksAnybody = "hands its caller a way into anybody's account.\n" +
	"`Vouch.Link` answers the link to whoever asked for it, and `Vouch.Redeem` spends\n" +
	"one: together they are every account of the tenant, an administrator's among\n" +
	"them. They are for a front door -- the app a tenant's people sign in at, which\n" +
	"mails the link -- and for nothing and nobody else."

// LinksAnybodyAnywhere is that of a deployment's key, which has no tenant to
// be *the* tenant: a front door every customer signs in at.
const LinksAnybodyAnywhere = "hands its caller a way into anybody's account.\n" +
	"`Vouch.Link` answers the link to whoever asked for it, and `Vouch.Redeem` spends\n" +
	"one: together they are every account of every tenant, an administrator's among\n" +
	"them. They are for a front door -- the app people sign in at, which mails the\n" +
	"link -- and for nothing and nobody else."

// MintsDelegations is the fourth: a key that mints keys, by
// `DelegationService/Issue` (#567). What a delegation carries is asked of what
// the person it is of may call, and not of the list of the credential that
// asks (`server/core/delegation.go`, `Issue`) -- so a key listing that one
// method hands out everything its person may call, to any holder of the
// tenant, which `docs/usage/wide-grants.md` § *A key that mints keys* says.
const MintsDelegations = "hands whoever holds it a delegation of anything its person may call.\n" +
	"`DelegationService/Issue` is asked what the person may call, and not what this key\n" +
	"lists, and the delegation goes to any holder of the tenant, an app's or anybody's.\n" +
	"Grant it only to something that is to hand that out."

// MintsDelegationsAnywhere is that of a deployment's key, which names somebody
// of any tenant it may write a way into.
const MintsDelegationsAnywhere = "hands whoever holds it a delegation of anything its person may call,\n" +
	"in every tenant: `DelegationService/Issue` is asked what the person it names may\n" +
	"call, and not what this key lists, and the delegation goes to any holder of that\n" +
	"person's tenant. Grant it only to something that is to hand that out."

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

// ListKeys writes this plane's keys, in the shape `roster key list` prints
// them, as `by` read them at the shell (#221).
//
// The tenant is printed as well as the alias, which the control plane's version
// did not need and this one does: an alias is unique within a tenant, so
// `@alice/laptop` is ambiguous across customers and `@newco/alice/laptop` is
// not.
//
// # On the access record, before anything is printed
//
// A holder's alias is declared `personal`, so the list is a read of every key
// holder's data -- a column of names no declaration can see, since it is read
// past the servers and printed as text. So it says what it showed
// (`access.Saw`) in a call of `ApiKeyService/List` made at the shell, which is
// the RPC it stands for, and prints only once that row is written: a list
// whose row could not be written is not printed at all.
func ListKeys(ctx context.Context, w io.Writer, at *Server, by Operator) error {
	var out strings.Builder
	err := at.AtTheShell(ctx, by, app.ApiKeyService_List_FullMethodName, func(ctx context.Context) error {
		// Erased rows left out, which reading ent directly does not do for
		// you: erasure is applied by the servers and this is under them. A
		// `list` that showed a key somebody had just revoked would be worse
		// than one that is a moment behind.
		vs, err := at.Ent.ApiKey.Query().
			Where(entapikey.DateErasedIsNil()).
			WithHolder(func(q *ent.HolderQuery) { q.WithTenant() }).
			WithApp(func(q *ent.HolderQuery) { q.WithTenant() }).
			All(ctx)
		if err != nil {
			return err
		}

		for _, v := range vs {
			who := "?"
			if h := v.Edges.Holder; h != nil {
				who = h.Alias
				var t pdid.Id
				if e := h.Edges.Tenant; e != nil {
					who = e.Alias + "/" + who
					t = pdid.Id(e.Id)
				}
				access.Saw(ctx, access.Subject{Id: pdid.Id(h.Id), Tenant: t})
			}

			used := "never"
			if v.DateUsed != nil {
				used = v.DateUsed.Format(time.RFC3339)
			}

			// And the app it is spent at (`ApiKey.app`), where it is for one:
			// nothing is said of a key of roster's, as nothing was.
			of := ""
			if a := v.Edges.App; a != nil {
				of = "\tapp=@" + a.Alias
				if e := a.Edges.Tenant; e != nil {
					of = "\tapp=@" + e.Alias + "/" + a.Alias
				}
			}
			// And the app's word about it (`ApiKey.scope`), quoted, since it
			// is the app's and may hold anything; nothing for none.
			if v.Scope != "" {
				of += "\tscope=" + strconv.Quote(v.Scope)
			}

			fmt.Fprintf(&out, "%s\t@%s/%s\tused=%s%s\t%s\n",
				pdid.Id(v.Id), who, v.Alias, used, of, strings.Join(v.Methods, ","))
		}

		return nil
	})
	if err != nil {
		return err
	}

	_, err = io.WriteString(w, out.String())

	return err
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
// with no control plane, where there are no deployment keys to ask about -- or
// the control plane's own stack, which has none above it: a nomination is a
// row of the plane a deployment key calls into, and is read there.
type keyOwners struct {
	db *ent.Client

	// control is whether this is the control plane's own stack.
	control bool
}

func (o *keyOwners) of(ctx context.Context, k pdid.Id) (pdid.Id, error) {
	if o.db == nil && o.control {
		// Said at `control.addr`, where the sentence below was not so: this
		// deployment has a control plane, and it is the one being asked.
		return pdid.Nil, status.Error(codes.Unimplemented,
			"a nomination names who a deployment key is answered as in a tenant, so it is read on the data plane, where the key calls")
	}
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
