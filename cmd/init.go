package cmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// NewCmdInit is `roster init`: the person who runs this deployment, and the
// first thing they may do.
//
// It exists because there is nowhere else it could happen. A tenant is not put
// up from inside one, so the first row of a deployment cannot arrive over the
// API. What puts it there is [Server.Ungated], which is not a privilege anybody
// holds: it is a server instance this process was handed, reachable from this
// command and from nowhere a request can get to.
//
// # It is the control plane's, and it used to be both
//
// This also wrote a **customer**: a tenant, somebody in it, and the role that
// administers it, from `--tenant` and `--holder` whose defaults were `contoso`
// and `admin`. So every deployment began life with a customer nobody had asked
// for, named after an example company, in a production database -- and once a
// control plane became required, that person could not be signed in as anyway:
// a data plane holder gets no password and no key from here, and both writes
// that would give them one are served on `admin.addr`, by an operator.
//
// Making a customer is an operator's act now, and it is the same act the
// hundredth time. `mayGrant` compares methods and site rather than tenants, so
// the operator's binding -- tenant-wide, in the **control** plane -- reaches a
// tenant that did not exist a moment ago; the admin port registers all four
// writes plus the two that write a way in. `ts/src/customers.tsx` is the
// screen, `cmd/newcustomer_test.go` is the whole sequence, and
// docs/operating.md, § 'The same thing from a console', is why.
//
// `Seed` still writes one when it is asked for a tenant, because a test and the
// Wasm sandbox want a deployment with somebody in it and have no console to
// make one from.
//
// # It grants, and it used to only create
//
// This wrote a tenant and a holder, printed "sign in as @contoso/admin", and
// stopped. Permissions are deny-by-default, so what it printed was somebody who
// could call exactly one method -- `MeService.Get`, which would tell them they
// held nothing.
//
// There was no way out either. Writing the first role needs `RoleService/Add`,
// which no binding allowed, and there is no other door: `mayGrant` is waived
// only where there is no frame at all, which is `Ungated`, which is here. So a
// fresh deployment answered nothing and nothing could change that. It went
// unnoticed because this command had no test.
//
// So it binds a role, and the role is a pattern rather than a list. A list
// written here is a snapshot: the next release adds an RPC the first operator
// cannot call, and cannot grant themselves either, because granting is refused
// for anything the granter does not already hold. That is now about the
// operator alone, and it is why `allow` is still here.
//
// # It needs a control plane
//
// `control.db` is required here and nowhere else. The plane is the deployment's
// own roster -- who may call it, and who runs it -- and a deployment that
// leaves it for later serves `auth.Plain` in the meantime, where every caller
// is whoever they type. What that costs is not only the obvious: an `rt_`
// minted while nobody is checking is a row that outlives the arrangement, and
// the day a control plane is named every one of them starts working. See the
// refusal below.
//
// `Seed` is not asked this. A deployment raised by a Go call -- a test, the
// Wasm sandbox -- is exactly where `Plain` belongs, and this is the only
// command anybody types.
//
// # Running it twice
//
// An error, because an alias is unique and the database says so. That is the
// right answer -- an `init` that quietly did nothing is one somebody runs
// against the wrong deployment and believes.
func NewCmdInit(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "init",
		Brief: "put up the operator who runs this deployment, and what they may do",

		Flags: flg.Flags{
			&flg.String{Name: "operator", Brief: "the alias of the control plane holder who runs the console"},
			&flg.Switch{Name: "password-stdin", Brief: "read the operator's password from stdin instead of generating one"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			operator, ok := flg.Find[string](cmd, "operator")
			if !ok || operator == "" {
				operator = "ops"
			}

			// A control plane, always, and the reason is not tidiness.
			//
			// A deployment that adds one **later** serves `auth.Plain` until it
			// does: every caller is whoever they type. `ApiKey.Issue`
			// works perfectly well in that state -- a name is written, a frame
			// is built, an `ApiKey` row lands on the data plane -- and nothing
			// reads it, because `auth.Bearer` is not in the chain. An expiry is
			// optional, so those rows do not go away.
			//
			// The day a control plane is named, `auth.Seq` gains
			// `keys.Store(control.Ungated, s.Ungated)` and every one of them
			// becomes a working credential at once. Nobody issued them on
			// purpose, because under `Plain` there was nobody to be. That is
			// not a migration; it is a deployment quietly acquiring
			// credentials.
			//
			// So the door is shut here rather than warned about. `Seed` is not
			// asked this -- a test and the Wasm sandbox raise a deployment by a
			// Go call, which is exactly where `Plain` belongs -- and this is
			// the only command anybody types to create one.
			if !c.Control.Serves() {
				return errors.New(
					"control.db.driver: init needs a control plane and this names no database for one. " +
						"a deployment that adds one later serves auth.Plain until it does -- every caller is " +
						"whoever they type, and an rt_ key minted by any of them sits inert in the data plane " +
						"until the day a control plane reads it, when all of them work at once. " +
						"name control.db, then run this again")
			}

			s, err := Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// A password from **stdin**, for a container that was told one.
			//
			// There is no `--password` flag and there will not be: an argument
			// is in the shell history and in the process list, which is the
			// same reason `roster key add` will not take a key. A pipe is
			// neither, and it is the shape `docker login --password-stdin` and
			// `gh auth login --with-token` already have.
			//
			// What reads the environment is the container's entrypoint, not
			// this. `ROSTER_ADMIN_PASSWORD` is a convention of the image the
			// way `POSTGRES_PASSWORD` is of that one, and a CLI that grew a
			// flag for it would be answering a question only a container asks.
			var given string
			if ok, _ := flg.Find[bool](cmd, "password-stdin"); ok {
				b, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<10))
				if err != nil {
					return fmt.Errorf("the password on stdin: %w", err)
				}

				given = strings.TrimRight(string(b), "\r\n")
				if given == "" {
					return errors.New("--password-stdin was given and stdin was empty")
				}
			}

			v, err := Seed(ctx, s, Seeding{Operator: operator, Password: given})
			if err != nil {
				return err
			}

			cmd.Printf("control plane\n")
			cmd.Printf("  holder %s is %s\n", operator, v.Operator)
			cmd.Printf("  bound to role %q = %s -- every Rpc roster serves, now and after an upgrade\n", everything, everyRosterMethod)
			cmd.Printf("  password  %s\n", v.Password)
			cmd.Printf("\nsign in to the console as %s. that password is shown once and is not stored -- write it down now.\n", operator)

			// And the next thing to do, because there is one and it is no
			// longer this command's.
			//
			// A deployment with no customers is the correct state to be left
			// in: a tenant is a customer, and one written by `init` was a
			// customer nobody asked for. What replaces it is the console, where
			// the first one is made the same way the hundredth is.
			cmd.Printf("\nthere are no customers yet, which is the right state to start in.\n")
			cmd.Printf("the console makes the first one -- a tenant, somebody in it, and the role\n")
			cmd.Printf("that lets them administer it. see docs/operating.md.\n")

			return nil
		}),
	}
}

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
	// The schema, so that a fresh database is one this can run against. A
	// deployment with migrations of its own does that instead; see payday's
	// `migrate`.
	if err := s.Ent.Schema.Create(ctx); err != nil {
		return Seeded{}, err
	}

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
const everything = "everything"

// everyRosterMethod is what that role holds: this app's own package, whatever
// is in it now and whatever a later release puts there.
const everyRosterMethod = "/" + string(protoPackage) + ".*/*"

// allow writes the role that says everything and binds it.
//
// Through `Ungated`, where there is no frame, so `mayGrantEverything` waives
// itself -- which is the only place it ever does. Every later grant of this
// descends from somebody who already held it.
func allow(ctx context.Context, s *Server, in pdid.Id, to pdid.Id) error {
	r, err := s.Ungated.Role().Add(ctx, app.RoleAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:  everything,
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
		Methods: []string{everyRosterMethod},
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
	if err := s.Ent.Schema.Create(ctx); err != nil {
		return pdid.Nil, "", err
	}

	// The same owner tenant `roster key add` uses, made here if this is a fresh
	// control plane. There is nothing to choose: a control plane has one owner.
	who, err := serviceOf(ctx, s, alias)
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
