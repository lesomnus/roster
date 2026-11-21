package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/z"

	"github.com/lesomnus/roster/internal/ent"
	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/core"
	"github.com/lesomnus/roster/server/vouch"
)

// Seeded is what a fresh deployment is: the operator who runs it, and the
// customer if one was asked for.
//
// `Tenant` and `Holder` are [pdid.Nil] where [Seeding.Tenant] was empty, which
// is what `roster init` asks for -- a deployment starts with no customers, and
// making one is an operator's act. A caller that wants both is a test or the
// Wasm sandbox, which have no console to make one from.
//
// `Operator` is [pdid.Nil] and `Password` is empty where there was no control
// plane, which `init` refuses and a Go call may still do.
type Seeded struct {
	Tenant   pdid.Id
	Holder   pdid.Id
	Operator pdid.Id

	// Found is whether the operator's holder was on the control plane before
	// this: a service `control key add` named, or the provisioner, which
	// `init` binds the role and a password to all the same, and says so.
	Found bool

	// Shown once and not stored. What is stored is an argon2id hash.
	Password string
}

// Seeding is what [Seed] is asked for.
//
// A struct rather than the positional strings this had grown to, because the
// last two additions -- a password, and now an identifier -- are both things a
// reader of a call site could not tell apart from the aliases beside them.
type Seeding struct {
	// Tenant and Holder are a **customer**: a tenant and the first person in
	// it, who is bound the role that administers it.
	//
	// Empty is none, which is what `roster init` asks for and is the ordinary
	// state of a fresh deployment. A caller that fills them in is one with no
	// console to make a customer from -- a test, or the Wasm sandbox, where a
	// reload is a fresh deployment and a page with nobody in it shows nothing.
	//
	// A `Holder` with no `Tenant` is refused rather than ignored: there is
	// nowhere to put them, and the caller meant one of the two.
	Tenant string
	Holder string

	// Operator is the one who administers this deployment from the control
	// plane, and is the only row `roster init` writes.
	Operator string

	// Password is the **operator's**, and empty means generate one.
	//
	// A caller that supplies it is one that has somewhere to have got it from
	// and somewhere to put it -- a container told by its environment. Nothing
	// else should: a password chosen by anything but the person is one somebody
	// else knows, and what makes the generated one safe is that it is shown
	// once and then changed.
	Password string

	// TenantId is the identifier the first tenant is given, and the nil one
	// mints a fresh one.
	//
	// It is here for the deployment that is not the only one who knows this
	// organisation. An app served by this roster anchors its own rows on the
	// identifier a credential carries, so the two have to agree about which
	// tenant somebody is in -- and when that app also has the tenant written
	// down as a constant, the agreement has to start here.
	//
	// **What happens without it is not an error**, which is why it is worth a
	// field rather than a note. Both sides come up, somebody signs in, and the
	// app makes a *second* tenant for them because the identifier it was handed
	// is not one it has: two rows for one organisation, and the rows that
	// belong together split across them, with nothing failing.
	//
	// It has to be a tenant-domain identifier, and `Tenant().Add` refuses
	// anything else -- so the check is payday's rather than one written here.
	TenantId pdid.Id
}

// Seed writes every row `init` writes, without the printing.
//
// Separate from the command because the command **is** the printing: what it
// adds is telling somebody what was made, and the rows are the same wherever
// they are wanted. The sandbox wants them and has no terminal to print a
// generated password on; anything else that wanted a deployment somebody can
// use would otherwise write these calls again and drift from them.
func Seed(ctx context.Context, s *Server, in Seeding) (Seeded, error) {
	tenant, holder, operator, password := in.Tenant, in.Holder, in.Operator, in.Password

	// No customer, which is what `roster init` asks for. See [Seeding.Tenant].
	if tenant == "" {
		if holder != "" || in.TenantId != pdid.Nil {
			return Seeded{}, errors.New("a holder and an identifier, and no tenant to put them in")
		}
		if s.Control == nil {
			return Seeded{}, nil
		}

		operator, found, secret, err := seedOperator(ctx, s.Control, operator, password)
		if err != nil {
			return Seeded{}, fmt.Errorf("operator %q: %w", in.Operator, err)
		}

		return Seeded{Operator: operator, Found: found, Password: secret}, nil
	}

	req := app.TenantAddRequest_builder{Alias: tenant}
	if in.TenantId != pdid.Nil {
		req.Id = in.TenantId.Bytes()
	}

	t, err := s.Ungated.Tenant().Add(ctx, req.Build())
	if err != nil {
		return Seeded{}, fmt.Errorf("tenant %q: %w", tenant, err)
	}

	k, err := pdid.From(t.GetId())
	if err != nil {
		return Seeded{}, err
	}

	// The tenant arrived with one, which is what `Tenant.Add` means on this
	// plane now (`server/core/tenant.go`): a customer is never made without
	// somebody who can administer it. So the alias this was asked for is
	// either that person -- the ordinary case, and there is nothing to write
	// -- or a second one, who is bound to the role the first already holds.
	j, err := holderOf(ctx, s.Ungated, k, holder)
	if err != nil {
		return Seeded{}, fmt.Errorf("holder %q: %w", holder, err)
	}
	if holder != core.Administers {
		r, err := s.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
			Ref: app.RoleRef_builder{
				Slug: app.RoleRefBySlug_builder{
					Alias:  z.Ptr(core.Everyverb),
					Tenant: app.TenantRef_builder{Id: t.GetId()}.Build(),
				}.Build(),
			}.Build(),
		}.Build())
		if err != nil {
			return Seeded{}, fmt.Errorf("the first role: %w", err)
		}

		if _, err := s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
			Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: j.Bytes()}.Build(),
		}.Build()); err != nil {
			return Seeded{}, fmt.Errorf("the first binding: %w", err)
		}
	}

	v := Seeded{Tenant: k, Holder: j}
	if s.Control == nil {
		return v, nil
	}

	v.Operator, v.Found, v.Password, err = seedOperator(ctx, s.Control, operator, password)
	if err != nil {
		return Seeded{}, fmt.Errorf("operator %q: %w", operator, err)
	}

	return v, nil
}

// Everyverb is what the first role is called, on both planes, and
// EveryRosterMethod is what it holds: this app's own package, whatever is in it
// now and whatever a later release puts there.
//
// `server/core`'s, because `Tenant.Establish` writes the same role for a
// customer and a constant spelled twice is one that drifts once.
const (
	Everyverb         = core.Everyverb
	EveryRosterMethod = core.EveryMethod
)

// allow has somebody hold the role that says everything: the role written where
// this tenant has none, and bound to them where it is not yet.
//
// # Found before it is written
//
// Two things want this role on a control plane and neither is first by rule:
// `roster init`, for the operator, and `serve`, for the provisioner of a
// deployment that declares resources ([asProvisioner]). `deploy/` starts
// `serve` and runs no `init`, so there the role is `serve`'s, and an `init`
// that wrote it again stopped at `AlreadyExists` on a deployment nobody could
// sign in to (#268). Whoever comes second finds it, and is bound to the one
// there is.
//
// The binding is looked for too, because nothing refuses a second: `Binding`
// has no unique index to answer `AlreadyExists` with, so a start that only
// added one added one more every time -- seven on one deployment's control
// plane before anybody counted. `cli`'s `ensureBinding` learned the same thing
// about the Login App's.
//
// # Why writing it is allowed at all
//
// Through `Ungated`, where there is no frame, so `mayGrantEverything` waives
// itself -- which is the only place it ever does. Every later grant of this
// descends from somebody who already held it.
func allow(ctx context.Context, at app.Server, in pdid.Id, to pdid.Id) error {
	r, err := at.Role().Get(ctx, app.RoleGetRequest_builder{
		Ref: app.RoleRef_builder{
			Slug: app.RoleRefBySlug_builder{
				Alias:  z.Ptr(Everyverb),
				Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		r, err = at.Role().Add(ctx, app.RoleAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
			Alias:  Everyverb,
			Desc:   "Every Rpc roster serves, including ones added by a later release.",

			// A pattern rather than an enumeration, for the reason in
			// `Role.methods`: a list written here is what existed the day it
			// was written, and the first administrator is the one person who
			// must not have to notice that.
			//
			// `/roster.*/*` and not `/*.*/*`, which would take in payday's own
			// -- `TokenService` is asked by a product app holding a key rather
			// than by anybody a role is written for. A deployment that wants
			// it grants it, and does it on purpose.
			Methods: []string{EveryRosterMethod},
		}.Build())
	}
	if err != nil {
		return fmt.Errorf("the role %q: %w", Everyverb, err)
	}

	vs, err := at.Binding().List(ctx, app.BindingListRequest_builder{
		Filters: []*app.BindingFilter{app.BindingFilter_builder{
			Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
			Holder: app.HolderRef_builder{Id: to.Bytes()}.Build(),
		}.Build()},
	}.Build())
	if err != nil {
		return fmt.Errorf("who holds the role %q: %w", Everyverb, err)
	}
	for _, v := range vs.GetItems() {
		// Everywhere, and not at one site: a binding narrowed to a site is
		// not this one.
		if len(v.GetSite().GetId()) == 0 {
			return nil
		}
	}

	_, err = at.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: to.Bytes()}.Build(),
	}.Build())
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("binding the role %q: %w", Everyverb, err)
	}

	return nil
}

// ErrOperated is `roster init` on a deployment that has an operator already.
//
// An error and not a no-op: an `init` that quietly did nothing is one somebody
// runs against the wrong deployment and believes.
var ErrOperated = errors.New("this deployment has an operator already")

// signsIn is somebody who signs in on this plane with a secret kept here, said
// as the refusal of a second `init` says them, and empty where nobody does.
//
// Any `Credential` at all -- a password, or what is enrolled beside one --
// since each is a person's and none is a caller's: a key is an `ApiKey`, and a
// holder made for one has no row here.
//
// One who was erased is not found by a server, and was said as thirty-two
// hexadecimal digits in no form any command prints, of somebody who signs in
// nowhere now (#284): so they are read from `db`, erased or not, named by
// alias and identifier as a command names somebody, and said to be erased.
func signsIn(ctx context.Context, at app.Server, db *ent.Client) (string, error) {
	vs, err := at.Credential().List(ctx, app.CredentialListRequest_builder{Size: 1}.Build())
	if err != nil {
		return "", fmt.Errorf("who signs in here: %w", err)
	}
	if len(vs.GetItems()) == 0 {
		return "", nil
	}

	id := vs.GetItems()[0].GetHolder().GetId()
	v, err := at.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: id}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		// Whoever it was, there is one; the name is for the reader.
		who, err := pdid.From(id)
		if err != nil {
			return "", fmt.Errorf("who signs in here: %w", err)
		}
		e, err := db.Holder.Get(ctx, who.Uuid())
		if err != nil {
			return fmt.Sprintf("%s signed in to its admin console, and was erased", who), nil
		}
		erased := "was erased"
		if e.DateErased != nil {
			erased = "was erased " + e.DateErased.UTC().Format(time.RFC3339)
		}

		return fmt.Sprintf("%q (%s) signed in to its admin console, and %s; "+
			"somebody of the control plane who is erased is not brought back", e.Alias, who, erased), nil
	}
	if err != nil {
		return "", fmt.Errorf("who signs in here: %w", err)
	}

	return fmt.Sprintf("%q signs in to its admin console", v.GetAlias()), nil
}

// seedOperator is the person who signs in to the admin console, and the password they
// do it with.
//
// # Why the control plane
//
// Because what a console does first is put up a tenant, and that is not
// something anybody inside a tenant does. The control plane is the deployment's
// own roster -- its holders are the callers rather than the customers -- and an
// operator is a caller. See docs/position.md, 'Two planes, one schema'.
//
// # Why a password and not a key
//
// A key is for a machine and travels on every call. This is a person at a
// browser, and what a browser carries is a session cookie the admin console sets
// after checking a secret; see `payday/auth/authsession` and
// `docs/guide/signing-in.md`. `VouchService` is what checks it, on the control
// plane's own instance.
//
// # Why it is generated and not typed
//
// There is no `--password` flag on purpose. A secret on a command line is in
// the shell history and in the process list, and `roster key add` already
// refuses to take a key for the same reason. What this prints is shown once and
// stored as an argon2id hash, so the deployment cannot tell anybody what it was
// any more than it can tell them their key.
//
// # One transaction
//
// The holder, the role and its binding, and the password are three writes, and
// the last is the one that can be refused -- too short, too long, in the
// leaked corpus. Written one at a time, a refusal left the first two behind
// and every later `init` stopped at the role with `AlreadyExists`: a control
// plane with an operator nobody could sign in as and no command to finish it
// (#20). So all three are one transaction on the control plane, the stack
// rebound onto it (`enttx.Rebind`), and a refused password leaves nothing.
//
// # The second run
//
// `init` is refused where the deployment has an operator already: somebody on
// this plane who signs in with a secret kept here ([signsIn]). Asked before
// anything is written, so a second run changes nothing -- least of all the
// password the first one printed.
//
// It used to be the role that refused it. `everything` was written with `Add`,
// and a second `init` stopped there with `AlreadyExists`. But the role is not
// `init`'s alone: `serve` writes it for the provisioner of a deployment that
// declares resources, which is every one made from `deploy/`, and there the
// first `init` was refused as a second, on a deployment nobody could sign in
// to (#268). What `init` alone leaves is the person, so that is what is asked,
// and the role is found or written like anybody's ([allow]).
//
// A holder with no way in is not one. The provisioner is a holder of this
// plane, and so is a caller `roster control key add` named; nobody signs in as
// either, and a deployment that has only those has not been `init`ed.
func seedOperator(ctx context.Context, s *Server, alias, given string) (pdid.Id, bool, string, error) {
	secret := given
	if secret == "" {
		var err error
		secret, err = vouch.Passphrase()
		if err != nil {
			return pdid.Nil, false, "", err
		}
	}

	drv, tx, err := dialect.BeginTx(ctx, s.Drv)
	if err != nil {
		return pdid.Nil, false, "", err
	}
	defer tx.Rollback()

	at, err := enttx.Rebind(s.Ungated, drv)
	if err != nil {
		return pdid.Nil, false, "", err
	}

	if has, err := signsIn(ctx, at, s.Ent); err != nil {
		return pdid.Nil, false, "", err
	} else if has != "" {
		return pdid.Nil, false, "", fmt.Errorf("%w: %s. "+
			"init makes the first operator and no other; if that is not who you expected to find, "+
			"this is not the deployment you meant", ErrOperated, has)
	}

	// Whether somebody of this name is here already: a service `control key
	// add` named, or the provisioner. They are bound and given a password all
	// the same, since nobody signs in as them yet; and the caller says so, so
	// that a name that was somebody's is not read as one made now (#284).
	found, err := heldAlready(ctx, at, alias)
	if err != nil {
		return pdid.Nil, false, "", err
	}

	// The same owner tenant `roster key add` uses, made here if this is a fresh
	// control plane. There is nothing to choose: a control plane has one owner.
	who, tenant, err := holderNamedIn(ctx, at, alias)
	if err != nil {
		return pdid.Nil, false, "", err
	}
	if err := allow(ctx, at, tenant, who); err != nil {
		return pdid.Nil, false, "", err
	}

	// Hashed by the service that will later check it, so the argon2 parameters
	// are in one place. A hash computed here would be a second set of them, and
	// the weaker of the two is the one that matters.
	if _, err := at.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Secret: []byte(secret),
	}.Build()); err != nil {
		return pdid.Nil, false, "", err
	}

	if err := tx.Commit(); err != nil {
		return pdid.Nil, false, "", err
	}

	return who, found, secret, nil
}

// heldAlready is whether this plane's one tenant has a holder called `alias`.
func heldAlready(ctx context.Context, at app.Server, alias string) (bool, error) {
	ts, err := at.Tenant().List(ctx, app.TenantListRequest_builder{Size: 1}.Build())
	if err != nil {
		return false, err
	}
	if len(ts.GetItems()) == 0 {
		return false, nil
	}

	_, err = at.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Alias:  z.Ptr(alias),
				Tenant: app.TenantRef_builder{Id: ts.GetItems()[0].GetId()}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	switch status.Code(err) {
	case codes.OK:
		return true, nil
	case codes.NotFound:
		return false, nil
	default:
		return false, err
	}
}
