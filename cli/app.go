package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

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
// is four rows there -- the holder, a role of roster's naming what the app asks
// of roster, the binding, the nomination -- and the app's first grant at
// itself: a role of the app's naming what it serves (`Role.app`), and roster's
// verbs for the app (`Binding.app`), each held by the app and by the people who
// are to administer it there.
//
// # Why it is a roster operator's, and a command rather than an RPC
//
// The last of those is the reason. What somebody holds is held at an app, and
// nobody hands out what they do not hold there -- so nobody inside a tenant
// holds anything at an app that has just arrived, and nobody could give the
// app, or themselves, the first of it. Somebody outside the wall has to start
// it, and that is a roster operator. What the app serves is the app's to know
// rather than roster's, and who administers it there is the tenant's.
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
		Brief: "make an app's holder, roles and nomination in a tenant, and name who administers it there",

		Args: arg.Args{
			&arg.String{Name: "APP", Brief: "the app, as the control-plane holder its deployment key hangs off; made if new"},
		},

		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
			&flg.Strings{Name: "allow", Brief: "what the app may do there, as methods of roster's; repeat it, or comma separate"},
			&flg.Strings{Name: "serves", Brief: "the app's own methods, as patterns: a role of the app's, held by it and by whoever --administer names"},
			&flg.Strings{Name: "administer", Brief: "people of the tenant, by alias, who administer the app: what --serves names, and roster's verbs for the app; repeat it, or comma separate"},
			&flg.Switch{Name: "adopt", Brief: "use a holder of the app's name that already holds roles, keys or a way to sign in, with all of it"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			name, _ := arg.Get[string](cl, "APP")
			tenant, _ := flg.Find[string](cl, "tenant")
			allow, _ := flg.Find[[]string](cl, "allow")
			serves, _ := flg.Find[[]string](cl, "serves")
			administer, _ := flg.Find[[]string](cl, "administer")
			adopt, _ := flg.Find[bool](cl, "adopt")

			if tenant == "" {
				return invocation(errors.New("--tenant: which tenant to install it into"))
			}
			methods, err := cmd.MethodsOf("--allow", allow)
			if err != nil {
				return invocation(err)
			}
			if len(methods) == 0 {
				// An app with nothing bound acts as a holder that may do nothing,
				// which is an installation that silently does not work.
				return invocation(errors.New("--allow: what the app may do in the tenant; a role that allows nothing installs nothing"))
			}
			own, err := cmd.MethodsOf("--serves", serves)
			if err != nil {
				return invocation(err)
			}
			people := aliasesOf(administer)
			if len(people) > 0 && len(own) == 0 {
				// Who administers an app there holds what it serves there,
				// and with nothing served there is nothing to hold.
				return invocation(errors.New("--administer: people hold the app's own methods, which --serves names; say what it serves"))
			}

			s, err := controlled(ctx, c)
			if err != nil {
				return err
			}
			defer s.Close()

			got, err := installApp(ctx, s, name, tenant, methods, own, people, adopt)
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
// The tenant, and each person `--administer` names, are read first, and so is
// whether a holder of the app's name is somebody's: an install that failed half
// way on one of those left a holder, a role and a nomination behind for a
// command that said it failed. What is refused by a write itself is not read
// first: the holder is made before the roles, so a role the server refused --
// one of roster's naming what roster does not serve -- leaves the holder,
// which a second run finds holding nothing and takes.
//
// A `NAME-app` already there that is not roster's is refused before anything
// is bound to it, as a `NAME-everything` that is not the app's is: bound, it
// would hand the app whatever it names at another app.
//
// A holder of the app's name that is already there is somebody's, and is used
// only if it holds nothing but the app's own roles -- which is what a second
// run finds -- or when `--adopt` says to. Taking it silently was answering the
// app's key with whatever that holder held, and handing whoever signs in as it
// the app's methods: `roster app install admin` was a key answered as the
// tenant's administrator.
//
// # The app's first grant at itself
//
// `own` is a role of the app's ([EveryRole]), bound to the app's holder, so
// that it may write, widen and bind the roles it ships, and to each of
// `people`, who may then hand the app's methods on there as anybody hands on
// what they hold. One called so that is already there is left as the tenant
// has it, when it is the app's, and refused before anything is written when it
// is roster's or another app's.
//
// # And roster's verbs, for the app
//
// Beside it, [core.AppAdministrator] is bound **for the app** to the app's
// holder and to each of `people`: roster's verbs over the app's roles, the
// bindings of them, its groups and their memberships, and nothing else of the
// tenant's. So the app reads and writes what is its own as itself, and its
// administrators do at roster, binding the app's roles and making a second
// administrator. The role is the tenant's one of that name, made here the
// first time it is needed and used as it stands when it names fewer; one
// naming anything else is refused before anything is written. The tenant's
// administrators' own role is not written to.
func installApp(ctx context.Context, s *cmd.Server, name, tenant string, methods, own, people []string, adopt bool) ([]string, error) {
	at := rstr.TenantRef_builder{Alias: z.Ptr(tenant)}.Build()
	tn, err := s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{
		Ref:    at,
		Select: rstr.TenantSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return nil, fmt.Errorf("--tenant %s: %w", tenant, err)
	}
	at = rstr.TenantRef_builder{Id: tn.GetId()}.Build()

	// Each of the people first: one who is nobody of the tenant is refused
	// before anything is written.
	admins := make([][]byte, 0, len(people))
	for _, alias := range people {
		v, err := holderFound(ctx, s, at, alias)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, fmt.Errorf("--administer %s: nobody of %s is called that", alias, tenant)
		}
		admins = append(admins, v)
	}

	existing, err := roleNamed(ctx, s, at, AppRole(name))
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// One that is there is left as the tenant has it -- when it is
		// roster's. One of an app's bound to the app would hand it whatever
		// that app's role names there, and not what `--allow` says at roster.
		if err := appRoleIsRosters(ctx, s, existing, tenant, name); err != nil {
			return nil, err
		}
	}
	administers, err := roleNamed(ctx, s, at, core.AppAdministrator)
	if err != nil {
		return nil, err
	}
	if administers != nil && len(own) > 0 {
		// Used as it stands where it names what an app's administrator may
		// hold, and refused before anything is written where it names more.
		if err := administersAnApp(ctx, s, administers, tenant); err != nil {
			return nil, err
		}
	}
	everything, err := roleNamed(ctx, s, at, EveryRole(name))
	if err != nil {
		return nil, err
	}
	if everything != nil && len(own) > 0 {
		// One that is there is left as the tenant has it -- when it is the
		// app's. One of roster's, or of another app's, bound to the app and
		// to the people named would hand them whatever it names, there.
		// Asked before anything is written.
		if err := everyRoleIsTheApps(ctx, s, at, everything, tenant, name); err != nil {
			return nil, err
		}
	}
	said := []string{}

	who, made, err := holderNamed(ctx, s, at, name, nil)
	if err != nil {
		return nil, err
	}
	if !made {
		h, err := holdingOf(ctx, s, who, existing, everything)
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

	role, made, err := roleOnce(ctx, s, at, AppRole(name), methods, nil)
	if err != nil {
		return nil, err
	}
	if made {
		said = append(said, fmt.Sprintf("@%s/%s may call %d method(s) in %s through role %s.", tenant, name, len(methods), tenant, AppRole(name)))
	} else {
		said = append(said, fmt.Sprintf("%s already has role %s; left as the tenant has it.", tenant, AppRole(name)))
	}
	if err := ensureBinding(ctx, s, role, who, nil); err != nil {
		return nil, err
	}

	if len(own) > 0 {
		role, made, err := roleOnce(ctx, s, at, EveryRole(name), own, who)
		if err != nil {
			return nil, err
		}
		if made {
			said = append(said, fmt.Sprintf("@%s/%s holds %d method(s) of its own at itself through role %s.", tenant, name, len(own), EveryRole(name)))
		} else {
			said = append(said, fmt.Sprintf("%s already has role %s; left as the tenant has it.", tenant, EveryRole(name)))
		}
		if err := ensureBinding(ctx, s, role, who, nil); err != nil {
			return nil, err
		}
		for i, v := range admins {
			if err := ensureBinding(ctx, s, role, v, nil); err != nil {
				return nil, err
			}
			said = append(said, fmt.Sprintf("@%s/%s may hand on %s's methods at @%s/%s through role %s.", tenant, people[i], name, tenant, name, EveryRole(name)))
		}

		// And roster's verbs for the app, to the app and to the same people.
		administers, made, err := roleOnce(ctx, s, at, core.AppAdministrator, core.ForAnApp, nil)
		if err != nil {
			return nil, err
		}
		if made {
			said = append(said, fmt.Sprintf("%s has role %s, naming the %d method(s) of roster's that reach an app's rows.", tenant, core.AppAdministrator, len(core.ForAnApp)))
		} else {
			said = append(said, fmt.Sprintf("%s already has role %s; left as the tenant has it.", tenant, core.AppAdministrator))
		}
		if err := ensureBindingFor(ctx, s, administers, who, who); err != nil {
			return nil, err
		}
		said = append(said, fmt.Sprintf("@%s/%s reads and writes what is its own at roster through role %s, for itself.", tenant, name, core.AppAdministrator))
		for i, v := range admins {
			if err := ensureBindingFor(ctx, s, administers, v, who); err != nil {
				return nil, err
			}
			said = append(said, fmt.Sprintf("@%s/%s administers @%s/%s at roster through role %s, for @%s/%s.", tenant, people[i], tenant, name, core.AppAdministrator, tenant, name))
		}
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

		// By its identifier. It was by its alias, which is what somebody
		// wrote about the key and is declared personal now (`apikey.proto`,
		// #280); this command names nobody at the shell and leaves no row of
		// the access record, so it shows no field a row declares so.
		// `roster --as <alias> control key list` shows the names, to
		// somebody the record names.
		id, err := pdid.From(k.GetId())
		if err != nil {
			return nil, err
		}
		said = append(said, fmt.Sprintf("key %s on %s is answered through it (%s).", id, name, as))
	}
	if len(ks.GetItems()) == 0 {
		said = append(said, fmt.Sprintf("%s has no key yet: roster control key add --narrowed %s.", name, name))
	}

	return said, nil
}

// AppRole is the alias of the role `roster app install` binds to an app's
// holder: the app's name and `-app`.
//
// # Not the app's name
//
// It was, and the name is taken. An app may name the role **its people** are
// given after itself -- a staff role called `loft`, holding `/loft.*/*` -- and
// [roleOnce] adopts a role that is already there, as it must for a second run.
// So installing an app into a tenant whose staff role already had its name
// bound that role to the app's own holder: the app's key answered with every
// method of the app it is, and none of the roster reads `--allow` named. Found
// on the first deployment it was pointed at, before it ran.
//
// A suffix rather than a flag, because `docs/apps.md` already drew it so and a
// name an operator has to choose is a name two operators choose differently.
func AppRole(app string) string { return app + "-app" }

// EveryRole is the alias of the role of the app's own that `roster app
// install --serves` writes: the app's name and `-everything`. A suffix for
// [AppRole]'s reason, and a different one, since the two are one role of
// roster's and one of the app's and a role's alias is one within its tenant.
func EveryRole(app string) string { return app + "-everything" }

// everyRoleIsTheApps refuses a role called [EveryRole] that is not a role of
// the app's own: of roster's, or of another app's, or of an app whose holder
// is not the one of the app's name.
func everyRoleIsTheApps(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, role []byte, tenant, name string) error {
	v, err := s.Ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref:    rstr.RoleRef_builder{Id: role}.Build(),
		Select: rstr.RoleSelect_builder{App: rstr.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return err
	}
	who, err := holderFound(ctx, s, at, name)
	if err != nil {
		return err
	}
	if who == nil || !bytes.Equal(v.GetApp().GetId(), who) {
		return fmt.Errorf("%s already has a role %s, and it is not @%s/%s's", tenant, EveryRole(name), tenant, name)
	}

	return nil
}

// appRoleIsRosters refuses a role called [AppRole] that is not a role of
// roster's: one of an app's, which would hand the app's holder what it names
// at that app and nothing `--allow` says at roster.
func appRoleIsRosters(ctx context.Context, s *cmd.Server, role []byte, tenant, name string) error {
	v, err := s.Ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref:    rstr.RoleRef_builder{Id: role}.Build(),
		Select: rstr.RoleSelect_builder{App: rstr.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return err
	}
	if len(v.GetApp().GetId()) > 0 {
		return fmt.Errorf("%s already has a role %s, and it is not roster's", tenant, AppRole(name))
	}

	return nil
}

// administersAnApp refuses a tenant's [core.AppAdministrator] naming anything
// but [core.ForAnApp], which is refused a binding for an app: asked here, before
// anything is written, rather than by the binding after the holder and the
// roles are.
func administersAnApp(ctx context.Context, s *cmd.Server, role []byte, tenant string) error {
	v, err := s.Ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref:    rstr.RoleRef_builder{Id: role}.Build(),
		Select: rstr.RoleSelect_builder{Methods: z.Ptr(true), App: rstr.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return err
	}
	if len(v.GetApp().GetId()) > 0 {
		return fmt.Errorf("%s already has a role %s, and it is not roster's", tenant, core.AppAdministrator)
	}
	for _, m := range v.GetMethods() {
		if !slices.Contains(core.ForAnApp, m) {
			return fmt.Errorf("%s's role %s names %s, and an app's administrators hold it for the app; it may name only what reaches an app's rows (%s)",
				tenant, core.AppAdministrator, m, strings.Join(core.ForAnApp, ", "))
		}
	}

	return nil
}

// ensureBindingFor is [ensureBinding] for an app (`Binding.app`): `role` bound
// to `who` for the app `of`, made if there is none.
func ensureBindingFor(ctx context.Context, s *cmd.Server, role, who, of []byte) error {
	vs, err := s.Ungated.Binding().List(ctx, rstr.BindingListRequest_builder{
		Filters: []*rstr.BindingFilter{rstr.BindingFilter_builder{
			Role:   rstr.RoleRef_builder{Id: role}.Build(),
			Holder: rstr.HolderRef_builder{Id: who}.Build(),
		}.Build()},
	}.Build())
	if err != nil {
		return err
	}
	for _, v := range vs.GetItems() {
		if bytes.Equal(v.GetApp().GetId(), of) {
			return nil
		}
	}

	_, err = s.Ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: role}.Build(),
		Holder: rstr.HolderRef_builder{Id: who}.Build(),
		App:    rstr.HolderRef_builder{Id: of}.Build(),
	}.Build())
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return err
	}

	return nil
}

// aliasesOf is a flag's people, each once: repeated, or comma separated.
func aliasesOf(vs []string) []string {
	out := []string{}
	for _, v := range vs {
		for _, a := range strings.Split(v, ",") {
			if a = strings.TrimSpace(a); a != "" && !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}

	return out
}

// roleOnce is the role called `alias` in this tenant, made with `methods` if
// there is none and **left alone** if there is -- the difference from
// [ensureRoleNamed], which the Login App's provisioning keeps level with what
// this binary calls. It says whether it made one. `of` is the app whose
// methods it names (`Role.app`), and nil is roster's.
func roleOnce(ctx context.Context, s *cmd.Server, at *rstr.TenantRef, alias string, methods []string, of []byte) ([]byte, bool, error) {
	req := rstr.RoleAddRequest_builder{Tenant: at, Alias: alias, Methods: methods}
	if of != nil {
		req.App = rstr.HolderRef_builder{Id: of}.Build()
	}
	v, err := s.Ungated.Role().Add(ctx, req.Build())
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

// newCmdAppUninstall ends an app's nomination in a tenant, which is the whole of
// taking it out: its narrowed key is answered as nobody there from the next
// request. A key of the app's that lists methods of its own is the
// deployment's in every tenant, asks no nomination, and is ended by name.
//
// The holder, its roles and their bindings stay -- the role of roster's, the
// app's own and whoever it was bound to, and roster's verbs for the app. The trail names the holder, so it
// stays as every holder does, and a role the tenant may have shaped is theirs to
// erase. A tenant administrator ends the same nomination themselves with
// `Nomination.Erase`, and needs nothing from a roster operator to do it.
func newCmdAppUninstall(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "uninstall",
		Brief: "end an app's nomination in a tenant; its narrowed key is refused there from then on",

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
				return invocation(errors.New("--tenant: which tenant to take it out of"))
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
			// Said of what a nomination is: who a key is answered as when a
			// request names the tenant. A key of this app's that lists methods
			// of its own never asked one, and is answered as itself in every
			// tenant as before -- the line said *refused* of that key too.
			fmt.Fprintf(os.Stderr, "roster: %s's key is answered as nobody in %s from now on: a request that names %s with it is refused. "+
				"A key of %s's that allows methods of its own was never narrowed, and this does not change it.\n",
				name, tenant, tenant, name)

			return nil
		}),
	}
}
