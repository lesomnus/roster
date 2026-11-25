package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/z"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
)

// handsOverSaid has `role add`, `role widen` and `binding add` say, after the
// row each prints, what the grant it made hands over that it does not read as:
// `docs/usage/wide-grants.md`, asked of `RoleService/HandsOver` (#310).
//
// # Why these three, and after the call
//
// Most grants that hand over more than they read as are made where a role is
// written or bound, and these are the commands the tree builds for it; they
// printed the row and nothing else, where `roster key add` said a note of its
// own. What is said is asked of the same connection the write was made on, as
// the same caller -- the pipe at the box, or the wire -- so it is the server's
// list and its words, and the plane's: on `roster control`'s tree a role is
// exercised in every tenant, and is told so.
//
// # Where it would be held
//
// A role added or widened is asked of where it can be held widest: across the
// tenant, in its site where it is a site's, nowhere where it is the tenant's
// `self-service` role. A binding is asked of where it was made, across the
// tenant or in its site, for the methods the role names as it is now. Nothing
// else of a binding is in `RoleHeld`: one to a group is asked as one to a
// person, and one held for an app (`Binding.app`) as one held across the
// tenant, which is the wider of the two.
//
// # What is not asked
//
// A role that names an app (`Role.app`) names that app's methods, which are
// not roster's and are not on the page, so it and a binding of it say nothing.
//
// # It never changes what the command did
//
// The write was made, so the exit status is the write's whatever is or is not
// said here, and all of it is on standard error: `-o json` and a pipe read the
// row alone. A caller who may not ask, or an answer that fails, is one line
// saying so with its code, and the page.
//
// A function because both trees have these commands, as [overlays] is; and
// after it, since `role widen` is one of its.
func handsOverSaid(t *pdcmd.Tree) {
	for path, f := range map[string]func(context.Context, pdcmd.Call){
		"role/add":    roleWrittenSaid,
		"role/widen":  roleWrittenSaid,
		"binding/add": bindingSaid,
	} {
		if err := t.After(path, f); err != nil {
			// A path the schema no longer has, which is a mistake in this file
			// rather than something a deployment can cause.
			panic(err)
		}
	}
}

// roleWrittenSaid is said after `role add`, of the methods the role now names,
// and after `role widen`, of the methods it was asked to add.
func roleWrittenSaid(ctx context.Context, c pdcmd.Call) {
	r, ok := c.Answered.(*app.Role)
	if !ok || namesAnApp(r) {
		return
	}

	methods := r.GetMethods()
	if w, ok := c.Sent.(*app.RoleWidenRequest); ok {
		methods = w.GetMethods()
	}

	held := app.RoleHeld_ROLE_HELD_ACROSS_THE_TENANT
	switch {
	case r.GetAlias() == core.Owns:
		held = app.RoleHeld_ROLE_HELD_NOWHERE
	case len(r.GetSite().GetId()) > 0:
		held = app.RoleHeld_ROLE_HELD_IN_A_SITE
	}

	sayHandedOver(ctx, c.Conn, held, methods)
}

// bindingSaid is said after `binding add`, of the role that was bound, read
// once on the same connection.
func bindingSaid(ctx context.Context, c pdcmd.Call) {
	b, ok := c.Answered.(*app.Binding)
	if !ok {
		return
	}

	ref := app.RoleRef_builder{Id: b.GetRole().GetId()}.Build()
	if len(b.GetRole().GetId()) == 0 {
		v, ok := c.Sent.(*app.BindingAddRequest)
		if !ok {
			return
		}
		ref = v.GetRole()
	}

	r, err := app.NewRoleServiceClient(c.Conn).Get(ctx, app.RoleGetRequest_builder{
		Ref:    ref,
		Select: app.RoleSelect_builder{All: z.Ptr(true), App: app.HolderSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		notAsked(err)
		return
	}
	if namesAnApp(r) {
		return
	}

	held := app.RoleHeld_ROLE_HELD_ACROSS_THE_TENANT
	if len(b.GetSite().GetId()) > 0 {
		held = app.RoleHeld_ROLE_HELD_IN_A_SITE
	}

	sayHandedOver(ctx, c.Conn, held, r.GetMethods())
}

// namesAnApp is a role of an app's, whose methods are not roster's.
func namesAnApp(r *app.Role) bool {
	return len(r.GetApp().GetId()) > 0
}

// sayHandedOver asks what `methods` hand over where they are `held`, and says
// each grant answered: what it reads as, what it is, and where on the page.
func sayHandedOver(ctx context.Context, conn grpc.ClientConnInterface, held app.RoleHeld, methods []string) {
	if len(methods) == 0 {
		return
	}

	v, err := app.NewRoleServiceClient(conn).HandsOver(ctx, app.RoleHandsOverRequest_builder{
		Methods: methods,
		Held:    held,
	}.Build())
	if err != nil {
		notAsked(err)
		return
	}
	if len(v.GetGrants()) == 0 {
		return
	}

	w := &strings.Builder{}
	for _, g := range v.GetGrants() {
		fmt.Fprintf(w, "\nNOTE: `%s` reads as %q, and is %s.\n  %s#%s\n",
			strings.Join(g.GetMethods(), "`, `"), g.GetReadsAs(), g.GetHandsOver(), wideGrantsPage, g.GetGrant())
	}
	fmt.Fprint(os.Stderr, w.String())
}

// wideGrantsPage is where a grant answered is written at length.
const wideGrantsPage = "docs/usage/wide-grants.md"

// notAsked is the one line said where what a write hands over could not be
// asked.
func notAsked(err error) {
	fmt.Fprintf(os.Stderr, "\nroster: what this hands over could not be asked (%s); %s says what each grant hands over\n",
		status.Code(err), wideGrantsPage)
}
