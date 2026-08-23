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
	"github.com/lesomnus/roster/server/vouch"
)

// NewCmdInit is `roster init`: the first tenant, the first person, and the
// first thing either of them may do.
//
// It exists because there is nowhere else it could happen. A tenant is not put
// up from inside one, so the first row of a deployment cannot arrive over the
// API. What puts it there is [Server.Ungated], which is not a privilege anybody
// holds: it is a server instance this process was handed, reachable from this
// command and from nowhere a request can get to.
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
// So it binds a role, and the role is [Role.every_method] rather than a list.
// A list written here is a snapshot: the next release adds an RPC the first
// administrator cannot call, and cannot grant themselves either, because
// granting is refused for anything the granter does not already hold.
//
// # Running it twice
//
// An error, because an alias is unique and the database says so. That is the
// right answer -- an `init` that quietly did nothing is one somebody runs
// against the wrong deployment and believes.
func NewCmdInit(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "init",
		Brief: "put up the first tenant, somebody in it, and what they may do",

		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the alias of the tenant to create"},
			&flg.String{Name: "holder", Brief: "the alias of the holder to create in it"},
			&flg.String{Name: "operator", Brief: "the alias of the control plane holder who runs the console"},
			&flg.String{Name: "tenant-id", Brief: "the identifier to give the first tenant; one is minted by default"},
			&flg.Switch{Name: "password-stdin", Brief: "read the operator's password from stdin instead of generating one"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			tenant, ok := flg.Find[string](cmd, "tenant")
			if !ok || tenant == "" {
				tenant = "contoso"
			}

			holder, ok := flg.Find[string](cmd, "holder")
			if !ok || holder == "" {
				holder = "admin"
			}

			operator, ok := flg.Find[string](cmd, "operator")
			if !ok || operator == "" {
				operator = "ops"
			}

			// The identifier for the first tenant, when an app served by this
			// roster already has one written down for the same organisation.
			// See [Seeding.TenantId] for what goes wrong when the two disagree,
			// which is nothing loud.
			var at pdid.Id
			if v, ok := flg.Find[string](cmd, "tenant-id"); ok && v != "" {
				k, err := pdid.Parse(v)
				if err != nil {
					return fmt.Errorf("--tenant-id: %w", err)
				}

				at = k
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

			v, err := Seed(ctx, s, Seeding{
				Tenant:   tenant,
				Holder:   holder,
				Operator: operator,
				Password: given,
				TenantId: at,
			})
			if err != nil {
				return err
			}

			cmd.Printf("tenant %s is %s\n", tenant, v.Tenant)
			cmd.Printf("holder %s is %s\n", holder, v.Holder)
			cmd.Printf("  bound to role %q = %s -- every RPC roster serves, now and after an upgrade\n", everything, everyRosterMethod)
			cmd.Printf("\nsign in as: @%s/%s\n", tenant, holder)

			if v.Operator == pdid.Nil {
				cmd.Printf("\nno control plane is configured, so this deployment believes its callers.\n")
				cmd.Printf("see docs/operating.md before serving it anywhere.\n")

				return nil
			}

			cmd.Printf("\ncontrol plane\n")
			cmd.Printf("  holder %s is %s\n", operator, v.Operator)
			cmd.Printf("  bound to role %q = %s\n", everything, everyRosterMethod)
			cmd.Printf("  password  %s\n", v.Password)
			cmd.Printf("\nthat password is shown once and is not stored. write it down now.\n")

			return nil
		}),
	}
}

// Seeded is what a fresh deployment is: the two identifiers `init` prints, and
// the operator it made in the control plane.
//
// `Operator` is [pdid.Nil] and `Password` is empty where there was no control
// plane, which is a deployment that believes its callers and has no console.
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
	// Tenant, Holder and Operator are the aliases of the three rows made: the
	// first tenant, the first person in it, and the one who administers this
	// deployment from the control plane.
	Tenant   string
	Holder   string
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
		Desc:   "Every RPC roster serves, including ones added by a later release.",

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
// operator is a caller. See PLAN.md, D15.
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
	if err := allow(ctx, s, pdid.Id(t.ID), who); err != nil {
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
	if _, err := vouch.New(s.Ungated, s.Ungated).Set(ctx, app.VouchSetRequest_builder{
		Who:    app.VouchWho_builder{Id: who.Bytes()}.Build(),
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
