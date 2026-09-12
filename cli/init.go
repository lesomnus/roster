package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/roster/cmd"
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
// writes plus the two that write a way in. `ts/console/customers.tsx` is the
// screen, `cmd/newcustomer_test.go` is the whole sequence, and
// docs/usage/customers.md, § 'Standing a customer up', is why.
//
// `cmd.Seed` still writes one when it is asked for a tenant, because a test and the
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
// `cmd.Seed` is not asked this. A deployment raised by a Go call -- a test, the
// Wasm sandbox -- is exactly where `Plain` belongs, and this is the only
// command anybody types.
//
// # Running it twice
//
// An error, because an alias is unique and the database says so. That is the
// right answer -- an `init` that quietly did nothing is one somebody runs
// against the wrong deployment and believes.
func NewCmdInit(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "init",
		Brief: "put up the operator who runs this deployment, and what they may do",

		Flags: flg.Flags{
			&flg.String{Name: "operator", Brief: "the alias of the control plane holder who runs the console"},
			&flg.Switch{Name: "password-stdin", Brief: "read the operator's password from stdin instead of generating one"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			operator, ok := flg.Find[string](cl, "operator")
			if !ok || operator == "" {
				operator = "admin"
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
			// So the door is shut here rather than warned about. `cmd.Seed` is not
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

			s, err := cmd.Build(ctx, *c)
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
			if ok, _ := flg.Find[bool](cl, "password-stdin"); ok {
				b, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<10))
				if err != nil {
					return fmt.Errorf("the password on stdin: %w", err)
				}

				given = strings.TrimRight(string(b), "\r\n")
				if given == "" {
					return errors.New("--password-stdin was given and stdin was empty")
				}
			}

			// The schema, so that a fresh database is one this can run
			// against. `cmd.Seed` used to do this and cannot any more: it is
			// called by the Wasm sandbox, and the call that migrates is the
			// call that links Atlas. See this package's doc comment.
			if err := Migrate(ctx, s); err != nil {
				return err
			}

			v, err := cmd.Seed(ctx, s, cmd.Seeding{Operator: operator, Password: given})
			if err != nil {
				return err
			}

			cl.Printf("control plane\n")
			cl.Printf("  holder %s is %s\n", operator, v.Operator)
			cl.Printf("  bound to role %q = %s -- every Rpc roster serves, now and after an upgrade\n", cmd.Everyverb, cmd.EveryRosterMethod)
			cl.Printf("  password  %s\n", v.Password)
			cl.Printf("\nsign in to the console as %s. that password is shown once and is not stored -- write it down now.\n", operator)

			// And the next thing to do, because there is one and it is no
			// longer this command's.
			//
			// A deployment with no customers is the correct state to be left
			// in: a tenant is a customer, and one written by `init` was a
			// customer nobody asked for. What replaces it is the console, where
			// the first one is made the same way the hundredth is.
			cl.Printf("\nthere are no customers yet, which is the right state to start in.\n")
			cl.Printf("the console makes the first one -- a tenant, somebody in it, and the role\n")
			cl.Printf("that lets them administer it. see docs/operating.md.\n")

			return nil
		}),
	}
}
