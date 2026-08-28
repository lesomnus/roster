package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/pdcmd"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// NewCmdIssue is `roster issue`: minting over the wire, which `roster key add`
// and `roster vouch reset` do from a shell on the box.
//
// # Why it exists beside them
//
// The local pair write through `Ungated` because a shell that holds the
// configuration file and the database may. An operator whose terminal is not
// on the box has neither -- what they have is an address and a credential, the
// same as a console -- and `IssueService` is the service a console mints
// through. These are that service's two calls, so a remote terminal can finish
// what it starts exactly as D57 made the local one able to.
//
// # What is deliberately not here: minting a service's key
//
// `IssueKeyRequest.service` exists and no flag reaches it, because no
// credential a terminal holds can succeed at it: minting is granting, the
// grant rule reads what the caller holds *through a binding*, and a key holds
// none -- by design, or a key could replicate itself wider. The callers with
// bindings are people, and a person at a terminal is a shell (`roster key
// add --service`) or a console. A flag here would be a command that
// structurally never works, which is the thing D58 refuses to mint.
func NewCmdIssue(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "issue",
		Brief: "mint over the wire: a key, or somebody's first password",

		Commands: xli.Commands{
			newCmdIssueKey(c),
			newCmdIssuePassword(c),
		},
	}
}

// issuing is the connection, or the sentence somebody needs when there is none.
func issuing(ctx context.Context, c *Config) (app.IssueServiceClient, func(), error) {
	if c.Client.Local || c.Client.Addr == "" {
		return nil, nil, errors.New(
			"`issue` mints over the wire, as a caller; the shell-on-the-box form is " +
				"`roster key add` and `roster vouch reset`. name client.addr")
	}

	conn, done, err := remote{c}.Connect(ctx)
	if err != nil {
		return nil, nil, err
	}

	return app.NewIssueServiceClient(conn), done, nil
}

func newCmdIssueKey(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "key",
		Brief: "mint a key for a customer's person, printed once",

		Args: arg.Args{
			&pdcmd.ArgRef{Name: "WHO",
				Brief: "a customer's person, as @tenant/alias or an identifier"},
		},
		Flags: flg.Flags{
			&flg.String{Name: "name", Brief: "what to call this key, unique per holder"},
			&flg.Strings{Name: "allow", Brief: "the methods it may call; repeat it, or comma separate"},
			&flg.String{Name: "expires", Brief: "how long it lasts, e.g. 720h; empty is forever"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			cl, done, err := issuing(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			req := app.IssueKeyRequest_builder{}

			ref, named := arg.Get[pdcmd.Ref](cmd, "WHO")
			if !named {
				return errors.New("WHO: whose key this is, as @tenant/alias or an identifier")
			}
			if err := ref.Expect(pd.HolderDomain); err != nil {
				return err
			}
			if !ref.Id.IsZero() {
				req.Holder = app.HolderRef_builder{Id: ref.Id.Bytes()}.Build()
			} else {
				// A slug needs its tenant here: this plane has many, and
				// `@alias` alone names one person per customer. Refused rather
				// than sent with an empty tenant, which the server can only
				// answer as a NotFound that names the wrong thing.
				if ref.Tenant == "" {
					return errors.New(
						"WHO: a key is for a person in a named tenant -- @tenant/alias, not @alias; " +
							"this plane has more than one")
				}

				req.Holder = app.HolderRef_builder{
					Slug: app.HolderRefBySlug_builder{
						Tenant: app.TenantRef_builder{Alias: z.Ptr(ref.Tenant)}.Build(),
						Alias:  z.Ptr(ref.Alias),
					}.Build(),
				}.Build()
			}

			methods, err := allowed(cmd)
			if err != nil {
				return err
			}
			req.Methods = methods

			expires, err := expiresOf(cmd)
			if err != nil {
				return err
			}
			req.Expires = expires

			name, _ := flg.Find[string](cmd, "name")
			req.Alias = name

			v, err := cl.IssueKey(ctx, req.Build())
			if err != nil {
				return err
			}

			// To stdout and nowhere else, as `roster key add` prints one.
			fmt.Fprintf(os.Stdout, "%s\n", v.GetToken())
			fmt.Fprintf(os.Stderr,
				"key %q, allowing %d method(s). This is the only time it is shown.\n",
				v.GetKey().GetAlias(), len(methods))
			if w := Widest(methods); w != "" {
				fmt.Fprintf(os.Stderr, "\n%s\n", w)
			}

			return next(ctx)
		}),
	}
}

// newCmdIssuePassword is for the operator somebody has just created, who has
// no way in yet. What they do with it is change it -- `VouchService.Set`,
// which needs the old one and is therefore not this.
//
// It is the control plane's alone, and the server is what says so: pointed at
// the data port it answers `Unimplemented`, because a bare alias names one
// person only where there is one tenant, and writing a customer's password
// with no tenant and no escalation check was a way in wider than the caller's.
// A customer's person gets a password the guarded way, `roster vouch reset`.
func newCmdIssuePassword(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "password",
		Brief: "set somebody's password to a generated one, printed once",

		Args: arg.Args{
			&arg.String{Name: "ALIAS", Brief: "whose, within this plane's one tenant; control plane only"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			cl, done, err := issuing(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			alias, _ := arg.Get[string](cmd, "ALIAS")
			if alias == "" {
				return errors.New("ALIAS: whose")
			}

			v, err := cl.IssuePassword(ctx, app.IssuePasswordRequest_builder{
				Alias: alias,
			}.Build())
			if err != nil {
				return err
			}

			// To stdout and nowhere else, the way `roster vouch reset` prints
			// one: `$(roster issue password …)` is the secret and nothing else.
			fmt.Fprintf(os.Stdout, "%s\n", v.GetPassword())
			fmt.Fprintf(os.Stderr,
				"shown once. They are expected to change it, which is `VouchService.Set` and needs this one.\n")

			return next(ctx)
		}),
	}
}
