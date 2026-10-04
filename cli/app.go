package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/cmd"
	"github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
)

// NewCmdApp is a roster-hosted app, put into a tenant and taken out of one.
//
// # What installing is
//
// An app a roster operator runs for many tenants holds one deployment key and
// acts in each tenant as a holder of that tenant's own, nominated for the key
// (`proto/app/nomination.proto`, `docs/apps.md`). So putting it into a tenant
// is four rows there -- the holder, a role, the binding, the nomination -- and,
// once, giving the tenant's administrators the app's methods to hand on.
//
// # Why it is a roster operator's, and a command rather than an RPC
//
// The last of those is the reason. A tenant's first administrator is bound
// `/roster.*/*`, which covers none of an app's own methods, and nobody hands
// out what they do not hold -- so nobody inside a tenant could give the app's
// holder its role, or themselves the right to manage it. Somebody outside the
// wall has to start it, and that is a roster operator. Which methods a tenant
// gets is what was sold to it, which is the app's to know rather than roster's.
//
// So this is the deployment's own work, through the unwalled server, as `roster
// login provision` and `roster tenant add` are. A tenant administrator does not
// need a verb of their own for what follows: every row it writes is an ordinary
// one, and changing the bindings or ending the nomination afterwards is theirs,
// through the same RPCs and under the same rules as anything else they manage.
func NewCmdApp(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "app",
		Brief: "a roster-hosted app: put it into a tenant, or take it out",

		Commands: xli.Commands{
			newCmdAppInstall(c),
			newCmdAppUninstall(c),
		},

		Handler: xli.RequireSubcommand(),
	}
}

// newCmdAppInstall writes what an app needs to act in one tenant, **once**.
//
// # Written once, and then the tenant's
//
// Every row is made if it is missing and left as it is if it is there. That is
// the difference from `roster login provision`, which rewrites the Login App's
// role to the release's list on every start because the Login App is roster's
// own. An app's holder in a tenant is that tenant's: an administrator who
// narrows its role or points the nomination at another holder has decided
// something, and running this again -- for another tenant, or by a runbook that
// runs it every time -- does not undo it. What it does put back is a row that
// is gone, a nomination or a binding: that is how an operator puts an app back
// after a tenant ended it, which the tenant usually cannot do itself.
func newCmdAppInstall(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "install",
		Brief: "make an app's holder, role and nomination in a tenant, and let its administrators manage it",

		Args: arg.Args{
			&arg.String{Name: "APP", Brief: "the app, as the control-plane holder its deployment key hangs off; made if new"},
		},

		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
			&flg.Strings{Name: "role", Brief: "what the app may do there, as methods; repeat it, or comma separate"},
			&flg.Strings{Name: "administer", Brief: "the methods the tenant's administrators may hand on; added to their role, once"},
			&flg.Switch{Name: "adopt", Brief: "use a holder of the app's name that already holds roles, keys or a way to sign in, with all of it"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			name, _ := arg.Get[string](cl, "APP")
			tenant, _ := flg.Find[string](cl, "tenant")
			role, _ := flg.Find[[]string](cl, "role")
			administer, _ := flg.Find[[]string](cl, "administer")
			adopt, _ := flg.Find[bool](cl, "adopt")

			if tenant == "" {
				return errors.New("--tenant: which tenant to install it into")
			}
			methods := cmd.SplitMethods(role)
			if len(methods) == 0 {
				// An app with nothing bound acts as a holder that may do nothing,
				// which is an installation that silently does not work.
				return errors.New("--role: what the app may do in the tenant; a role that allows nothing installs nothing")
			}

			s, err := controlled(ctx, c)
			if err != nil {
				return err
			}
			defer s.Close()

			got, err := installApp(ctx, s, name, tenant, methods, cmd.SplitMethods(administer), adopt)
			if err != nil {
				return err
			}

			for _, line := range got {
				fmt.Fprintln(os.Stderr, "roster: "+line)
			}

			return nil
		}),
	}
}

// installApp is [newCmdAppInstall] without the process, and answers with what
// it wrote, one sentence each, for the log.
//
// # What it looks at before it writes
//
// The tenant, and the role its administrators hold when `--administer` names
// one, are read first: an install that failed half way left a holder, a role
// and a nomination behind for a command that said it failed.
//
// A holder of the app's name that is already there is somebody's, and is used
// only if it holds nothing but the app's own role -- which is what a second run
// finds -- or when `--adopt` says to. Taking it silently was answering the
// app's key with whatever that holder held, and handing whoever signs in as it
// the app's methods: `roster app install admin` was a key answered as the
// tenant's administrator.
func installApp(ctx context.Context, s *cmd.Server, name, tenant string, methods, administer []string, adopt bool) ([]string, error) {
	at := rstr.TenantRef_builder{Alias: z.Ptr(tenant)}.Build()
	tn, err := s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
		Ref:    at,
		Select: rstr.TenantSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return nil, fmt.Errorf("--tenant %s: %w", tenant, err)
	}
	at = rstr.TenantRef_builder{Id: tn.GetId()}.Build()

	if len(administer) > 0 {
		r, err := roleNamed(ctx, s, at, core.Everyverb)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, fmt.Errorf("--administer: %s has no role %q to add the app's methods to", tenant, core.Everyverb)
		}
	}

	existing, err := roleNamed(ctx, s, at, AppRole(name))
	if err != nil {
		return nil, err
	}
	said := []string{}

	who, made, err := holderNamed(ctx, s, at, name)
	if err != nil {
		return nil, err
	}
	if !made {
		h, err := holdingOf(ctx, s, who, existing)
		if err != nil {
			return nil, err
		}
		switch {
		case h.empty():
		case !adopt:
			return nil, fmt.Errorf("@%s/%s is already there and holds %s: the app's key would be answered with all of it, "+
				"and whoever signs in as it would hold the app's methods. Name the app something else, or say --adopt", tenant, name, h)
		default:
			said = append(said, fmt.Sprintf("adopted @%s/%s, which already held %s; the app is answered with all of it.", tenant, name, h))
		}
	}

	borrower, err := cmd.HolderNamed(ctx, s.Control, name)
	if err != nil {
		return nil, err
	}

	role, made, err := roleOnce(ctx, s, at, AppRole(name), methods)
	if err != nil {
		return nil, err
	}
	if made {
		said = append(said, fmt.Sprintf("@%s/%s may call %d method(s) in %s through role %s.", tenant, name, len(methods), tenant, AppRole(name)))
	} else {
		said = append(said, fmt.Sprintf("%s already has role %s; left as the tenant has it.", tenant, AppRole(name)))
	}
	if err := ensureBinding(ctx, s, role, who); err != nil {
		return nil, err
	}

	switch n, err := nominationOf(ctx, s, at, borrower); {
	case status.Code(err) == codes.NotFound:
		if _, err := s.Ungated.Nomination().Add(ctx, rstr.NominationAddRequest_builder{
			Tenant:     at,
			BorrowerId: borrower.Bytes(),
			ActsAs:     rstr.HolderRef_builder{Id: who}.Build(),
			Name:       name,
		}.Build()); err != nil {
			return nil, err
		}
		said = append(said, fmt.Sprintf("%s's key is answered as @%s/%s in %s.", name, tenant, name, tenant))
	case err != nil:
		return nil, err
	case !bytes.Equal(n.GetActsAs().GetId(), who):
		said = append(said, fmt.Sprintf("%s's key is nominated as somebody else in %s; left as the tenant has it.", name, tenant))
	}

	// Every live key on the app's control-plane holder borrows through the
	// nomination, not only the one somebody just minted -- so they are named.
	ks, err := s.Control.Ungated.ApiKey().List(ctx, rstr.ApiKeyListRequest_builder{
		Filters: []*rstr.ApiKeyFilter{rstr.ApiKeyFilter_builder{Holder: rstr.HolderRef_builder{Id: borrower.Bytes()}.Build()}.Build()},
	}.Build())
	if err != nil {
		return nil, err
	}
	for _, k := range ks.GetItems() {
		as := "narrowed"
		if n := len(k.GetMethods()); n > 0 {
			as = fmt.Sprintf("and %d method(s) across every tenant without roster-at", n)
		}
		said = append(said, fmt.Sprintf("key %q on %s is answered through it (%s).", k.GetAlias(), name, as))
	}
	if len(ks.GetItems()) == 0 {
		said = append(said, fmt.Sprintf("%s has no key yet: roster control key add --narrowed %s.", name, name))
	}

	if len(administer) > 0 {
		added, err := administered(ctx, s, at, administer)
		if err != nil {
			return nil, err
		}
		if added > 0 {
			said = append(said, fmt.Sprintf("%s's administrators may hand on %d more method(s).", tenant, added))
		}
	}

	return said, nil
}

// AppRole is the alias of the role `roster app install` binds to an app's
// holder: the app's name and `-app`.
//
// # Not the app's name
//
// It was, and the name is taken. An app names the role **its people** are
// given after itself -- kamino's staff role is `kamino`, `/hday.oasys.*/*`,
// and khala's is `khala` -- and [roleOnce] adopts a role that is already there,
// as it must for a second run. So installing kamino into a tenant that already
// used it bound the staff role to the app's own holder: the app's key answered
// with every method of the app it is, and none of the roster reads `--role`
// named. Found on the first deployment it was pointed at, before it ran.
//
// A suffix rather than a flag, because `docs/apps.md` already drew it so and a
// name an operator has to choose is a name two operators choose differently.
func AppRole(app string) string { return app + "-app" }

// roleOnce is the role called `alias` in this tenant, made with `methods` if
// there is none and **left alone** if there is -- the difference from
// [ensureRoleNamed], which the Login App's provisioning keeps level with each
// release. It says whether it made one.
func roleOnce(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, alias string, methods []string) ([]byte, bool, error) {
	v, err := s.Ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant: at, Alias: alias, Methods: methods,
	}.Build())
	if err == nil {
		return v.GetId(), true, nil
	}
	if status.Code(err) != codes.AlreadyExists {
		return nil, false, err
	}

	got, err := s.Ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref: rstr.RoleRef_builder{
			Slug: rstr.RoleRefBySlug_builder{Alias: z.Ptr(alias), Tenant: at}.Build(),
		}.Build(),
		Select: rstr.RoleSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return nil, false, err
	}

	return got.GetId(), false, nil
}

// nominationOf is this tenant's nomination for the key's holder, or `NotFound`.
func nominationOf(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, borrower pdid.Id) (*rstr.Nomination, error) {
	return s.Ungated.Nomination().Get(ctx, rstr.NominationGetRequest_builder{
		Ref: rstr.NominationRef_builder{
			Borrower: rstr.NominationRefByBorrower_builder{Tenant: at, BorrowerId: borrower.Bytes()}.Build(),
		}.Build(),
		Select: rstr.NominationSelect_builder{ActsAs: rstr.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
}

// administered adds `methods` to the role a tenant was made with, so whoever
// administers the tenant may hand them on, and answers how many were new.
//
// That role (`server/core`'s [core.Everyverb]) is `/roster.*/*` from the day the
// tenant is made, which covers no app's methods; this is what a tenant
// administrator needs before the app's holder's role, or anybody else's, is
// theirs to change. Added to and never rewritten: what is already there stays,
// and a method already there, spelled the same, is not added twice. Spelled
// the same and not covered: `/hday.oasys.*/*` beside `/hday.oasys.Robot/*` is
// two rows, which is untidy and grants nothing more.
func administered(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, methods []string) (int, error) {
	got, err := s.Ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref: rstr.RoleRef_builder{
			Slug: rstr.RoleRefBySlug_builder{Alias: z.Ptr(core.Everyverb), Tenant: at}.Build(),
		}.Build(),
		Select: rstr.RoleSelect_builder{Methods: z.Ptr(true), DateUpdated: z.Ptr(true)}.Build(),
	}.Build())
	if err != nil {
		return 0, fmt.Errorf("the tenant's administrators' role (%q): %w", core.Everyverb, err)
	}

	have := got.GetMethods()
	next := slices.Clone(have)
	for _, m := range methods {
		if slices.Contains(next, m) {
			continue
		}
		next = append(next, m)
	}
	added := len(next) - len(have)
	if added == 0 {
		return 0, nil
	}

	if _, err := s.Ungated.Role().Patch(ctx, rstr.RolePatchRequest_builder{
		Ref:         rstr.RoleRef_builder{Id: got.GetId()}.Build(),
		Methods:     next,
		DateUpdated: got.GetDateUpdated(),
	}.Build()); err != nil {
		return 0, err
	}

	return added, nil
}

// newCmdAppUninstall ends an app's nomination in a tenant, which is the whole of
// taking it out: its key is refused there from the next request.
//
// The holder, its role and its binding stay. The trail names the holder, so it
// stays as every holder does, and a role the tenant may have shaped is theirs to
// erase. A tenant administrator ends the same nomination themselves with
// `Nomination.Erase`, and needs nothing from a roster operator to do it.
func newCmdAppUninstall(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "uninstall",
		Brief: "end an app's nomination in a tenant; its key is refused there from then on",

		Args: arg.Args{
			&arg.String{Name: "APP", Brief: "the app, as the control-plane holder its deployment key hangs off"},
		},

		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			name, _ := arg.Get[string](cl, "APP")
			tenant, _ := flg.Find[string](cl, "tenant")
			if tenant == "" {
				return errors.New("--tenant: which tenant to take it out of")
			}

			s, err := controlled(ctx, c)
			if err != nil {
				return err
			}
			defer s.Close()

			// Both looked up and neither made: a mistyped tenant was "not
			// nominated; nothing to do", and a mistyped app was a new
			// control-plane holder written by a command taking one away.
			tn, err := s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
				Ref:    rstr.TenantRef_builder{Alias: z.Ptr(tenant)}.Build(),
				Select: rstr.TenantSelect_builder{}.Build(),
			}.Build())
			if err != nil {
				return fmt.Errorf("--tenant %s: %w", tenant, err)
			}
			borrower, err := cmd.ControlHolder(ctx, s.Control, name)
			if err != nil {
				return err
			}
			n, err := nominationOf(ctx, s, rstr.TenantRef_builder{Id: tn.GetId()}.Build(), borrower)
			if status.Code(err) == codes.NotFound {
				fmt.Fprintf(os.Stderr, "roster: %s is not nominated in %s; nothing to do.\n", name, tenant)

				return nil
			}
			if err != nil {
				return err
			}

			if _, err := s.Ungated.Nomination().Erase(ctx, rstr.NominationRef_builder{Id: n.GetId()}.Build()); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "roster: %s's key is refused in %s from now on.\n", name, tenant)

			return nil
		}),
	}
}
