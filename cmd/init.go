package cmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
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

		operator, secret, err := seedOperator(ctx, s.Control, operator, password)
		if err != nil {
			return Seeded{}, fmt.Errorf("operator %q: %w", in.Operator, err)
		}

		return Seeded{Operator: operator, Password: secret}, nil
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

	h, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: t.GetId()}.Build(),
		Alias:  holder,
	}.Build())
	if err != nil {
		return Seeded{}, fmt.Errorf("holder %q: %w", holder, err)
	}

	j, err := pdid.From(h.GetId())
	if err != nil {
		return Seeded{}, err
	}

	if err := allow(ctx, s, k, j); err != nil {
		return Seeded{}, fmt.Errorf("the first binding: %w", err)
	}

	v := Seeded{Tenant: k, Holder: j}
	if s.Control == nil {
		return v, nil
	}

	v.Operator, v.Password, err = seedOperator(ctx, s.Control, operator, password)
	if err != nil {
		return Seeded{}, fmt.Errorf("operator %q: %w", operator, err)
	}

	return v, nil
}

// everything is what the first role is called, on both planes.
//
// A name somebody will read in a list of roles and understand without opening
// it, which matters more here than anywhere else: it is the role that explains
// why somebody could do something.
const Everyverb = "everything"

// everyRosterMethod is what that role holds: this app's own package, whatever
// is in it now and whatever a later release puts there.
const EveryRosterMethod = "/" + string(protoPackage) + ".*/*"

// allow writes the role that says everything and binds it.
//
// Through `Ungated`, where there is no frame, so `mayGrantEverything` waives
// itself -- which is the only place it ever does. Every later grant of this
// descends from somebody who already held it.
func allow(ctx context.Context, s *Server, in pdid.Id, to pdid.Id) error {
	r, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:  Everyverb,
		Desc:   "Every Rpc roster serves, including ones added by a later release.",

		// A pattern rather than an enumeration, for the reason in
		// `Role.methods`: a list written here is what existed the day it was
		// written, and the first administrator is the one person who must not
		// have to notice that.
		//
		// `/roster.*/*` and not `/*.*/*`, which would take in payday's own --
		// `BatchService` is a way of calling the methods this already covers,
		// and `TokenService` is asked by a product app holding a key rather
		// than by anybody a role is written for. A deployment that wants those
		// grants them, and does it on purpose.
		Methods: []string{EveryRosterMethod},
	}.Build())
	if err != nil {
		return err
	}

	_, err = s.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: r.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: to.Bytes()}.Build(),
	}.Build())

	return err
}

// seedOperator is the person who signs in to the console, and the password they
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
// browser, and what a browser carries is a session cookie the console sets
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
func seedOperator(ctx context.Context, s *Server, alias, given string) (pdid.Id, string, error) {
	// The same owner tenant `roster key add` uses, made here if this is a fresh
	// control plane. There is nothing to choose: a control plane has one owner.
	who, err := ServiceOf(ctx, s, alias)
	if err != nil {
		return pdid.Nil, "", err
	}

	t, err := s.Ent.Tenant.Query().First(ctx)
	if err != nil {
		return pdid.Nil, "", err
	}
	if err := allow(ctx, s, pdid.Id(t.Id), who); err != nil {
		return pdid.Nil, "", err
	}

	secret := given
	if secret == "" {
		secret, err = passphrase()
		if err != nil {
			return pdid.Nil, "", err
		}
	}

	// Hashed by the service that will later check it, so the argon2 parameters
	// are in one place. A hash computed here would be a second set of them, and
	// the weaker of the two is the one that matters.
	if _, err := s.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
		Ref:    app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Secret: []byte(secret),
	}.Build()); err != nil {
		return pdid.Nil, "", err
	}

	return who, secret, nil
}

// passphrase is 32 bytes from `crypto/rand`, printable.
//
// Long enough that it is not guessed and not a word anybody will recognise,
// because the one thing it must not be is something somebody keeps. It is for
// the first sign-in, and the console's job is to make them change it.
func passphrase() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}
