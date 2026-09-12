package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/lesomnus/roster/cmd"
	"os"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdid"

	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// NewCmdKey is `roster key`: the keys this deployment is called with, on
// either plane.
//
// It is a command rather than an RPC because of what the first of those writes
// to. A deployment's own key is minted before there is anything to mint it with
// -- and `ApiKey.Add`, which takes a caller-chosen verifier, is shut a method
// at a time for the reason every verifier is -- so the only way in is a server
// instance this process holds, and the only thing holding one is this.
//
// (`ApiKey.Issue` mints one over the wire for a console and is what a
// customer's person reaches; neither is what a shell has before the first key
// exists.)
//
// # And a customer's, which it refused to mint
//
// `--tenant` and `--holder` mint an `rt_` for one of a customer's people. This
// said, in as many words, that *a key for somebody inside a tenant is not
// something a shell on the box should be handing out* -- and the console was
// the answer.
//
// The premise went away. `roster init` seeds no customer (D56), so the first
// one is created by somebody, and everything that creates it is already here:
// `roster tenant add`, `holder add`, `role add`, `binding add`, all local, all
// through `Ungated`, with no rules at all. A shell that has just bound
// `/roster.*/*` to a person and cannot then mint them a key is not a boundary,
// it is a missing step -- and what it made necessary was a browser, for a
// deployment somebody is running from a terminal.
//
// The boundary is who holds the configuration file and the database, which is
// what `docs/usage/cli.md` says about the local CLI everywhere else.
func NewCmdKey(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "key",
		Brief: "the keys this deployment's services call it with",

		Commands: xli.Commands{
			newCmdKeyAdd(c),
			newCmdKeyList(c),
			newCmdKeyRevoke(c),
		},
	}
}

// newCmdKeyAdd mints one, and prints it once.
//
// Once because what is stored is a hash. This deployment cannot tell anybody
// what their key was any more than it can tell them their password, and a
// command that could print it again would be one that had kept it.
func newCmdKeyAdd(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "add",
		Brief: "mint a key for a service or for one of a customer's people, and print it once",

		Flags: flg.Flags{
			&flg.String{Name: "service", Brief: "the holder in the control plane this key is for"},
			&flg.String{Name: "tenant", Brief: "the customer, by alias or identifier, for a key on the data plane"},
			&flg.String{Name: "holder", Brief: "whose key this is, inside --tenant"},
			&flg.String{Name: "name", Brief: "what to call this key, unique per service"},
			&flg.Strings{Name: "allow", Brief: "the methods it may call; repeat it, or comma separate"},
			&flg.String{Name: "expires", Brief: "how long it lasts, e.g. 720h; empty is forever"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			service, _ := flg.Find[string](cl, "service")
			tenant, _ := flg.Find[string](cl, "tenant")
			holder, _ := flg.Find[string](cl, "holder")

			// Which plane, said by which flags were given rather than by a
			// `--kind` nobody would get right. The prefix follows from the
			// plane and is never something a caller names -- `issue.proto` is
			// explicit that a caller who could name one could ask the
			// customer-facing door for the deployment's own kind.
			switch {
			case service != "" && (tenant != "" || holder != ""):
				return errors.New(
					"--service is the deployment's own and --tenant/--holder is a customer's; name one")
			case service == "" && tenant == "" && holder == "":
				return errors.New(
					"--service: which service is this key for, " +
						"or --tenant and --holder for one of a customer's people")
			case holder != "" && tenant == "":
				return fmt.Errorf("--tenant: which customer's %q, since an alias names one per tenant", holder)
			case tenant != "" && holder == "":
				return fmt.Errorf("--holder: whose key this is, inside %q", tenant)
			}

			name, _ := flg.Find[string](cl, "name")
			if name == "" {
				name = "default"
			}

			allow, _ := flg.Find[[]string](cl, "allow")
			methods := cmd.SplitMethods(allow)
			if len(methods) == 0 {
				// Refused rather than defaulted, in either direction. Defaulting
				// to everything hands out more than anybody asked for, and
				// defaulting to nothing mints a key that silently does not work
				// -- which is worse to debug than being told now.
				return errors.New("--allow: a key that allows nothing is not a key; name the methods")
			}

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// Which server the row lands in, who it hangs off, and what the
			// token is called. The three answers travel together because they
			// are one decision.
			var (
				at     app.Server
				who    pdid.Id
				prefix string
				whose  string
			)
			if service != "" {
				if s.Control == nil {
					return errors.New("this deployment has no control plane; see `control` in the configuration")
				}
				if err := entmigrate.NewSchema(s.Control.Drv).Create(ctx); err != nil {
					return err
				}

				// Named into existence, which is `cmd.ServiceOf`'s decision and the
				// right one there: a service is not something somebody sets up
				// on purpose before they need it, and the control plane has one
				// tenant so an alias names one person.
				who, err = cmd.ServiceOf(ctx, s.Control, service)
				if err != nil {
					return err
				}

				at, prefix, whose = s.Control.Ungated, keys.PrefixDeployment, "@"+service
			} else {
				// Looked up and never created. A customer's people are the
				// customer's, and a command that made one by mentioning them
				// would be a way to write rows into somebody else's tenant by
				// typo -- which is the rule `IssueKeyRequest.holder` already
				// states about the same act over the wire.
				who, err = cmd.CustomerOf(ctx, s, tenant, holder)
				if err != nil {
					return err
				}

				at, prefix, whose = s.Ungated, keys.PrefixTenant, "@"+tenant+"/"+holder
			}

			token, sum, err := keys.Mint(prefix)
			if err != nil {
				return err
			}

			req := app.ApiKeyAddRequest_builder{
				Holder:  app.HolderRef_builder{Id: who.Bytes()}.Build(),
				Alias:   name,
				Secret:  sum,
				Methods: methods,
			}

			if v, _ := flg.Find[string](cl, "expires"); v != "" {
				d, err := time.ParseDuration(v)
				if err != nil {
					return fmt.Errorf("--expires: %w", err)
				}

				req.DateExpires = timestamppb.New(time.Now().Add(d))
			}

			v, err := at.ApiKey().Add(ctx, req.Build())
			if err != nil {
				return err
			}

			k, err := pdid.From(v.GetId())
			if err != nil {
				return err
			}

			// To stdout and nowhere else. It is not logged, because a
			// credential that reaches a log has been given away.
			fmt.Fprintf(os.Stdout, "%s\n", token)
			fmt.Fprintf(os.Stderr,
				"key %s for %s, allowing %d method(s). This is the only time it is shown.\n",
				k, whose, len(methods))

			if v := cmd.Widest(methods); v != "" {
				fmt.Fprintf(os.Stderr, "\n%s\n", v)
			}

			return nil
		}),
	}
}

// newCmdKeyList says what exists, and never what any of them are.
func newCmdKeyList(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "list",
		Brief: "what keys exist, and what each may call",

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			if s.Control == nil {
				return errors.New("this deployment has no control plane")
			}

			// Both planes, because `add` writes to both. This listed the
			// control plane's alone, so a customer's `rt_` -- which the command
			// beside it mints -- appeared nowhere, and the only way to see one
			// was `roster apikey ls`, which is a different command answering a
			// different question.
			if err := cmd.ListKeys(ctx, os.Stdout, s.Control.Ent, ""); err != nil {
				return err
			}

			return cmd.ListKeys(ctx, os.Stdout, s.Ent, "")
		}),
	}
}

// newCmdKeyRevoke stops one, now.
//
// A delete and not a flag, which is the whole reason a key is a row: the next
// call carrying it finds nothing and is refused, with no window and nothing to
// expire. The trail keeps what it was.
func newCmdKeyRevoke(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "revoke",
		Brief: "stop a key, now",

		Flags: flg.Flags{
			&flg.String{Name: "id", Brief: "the key, as `key list` prints it"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			v, _ := flg.Find[string](cl, "id")
			if v == "" {
				return errors.New("--id: which key")
			}

			k, err := pdid.Parse(v)
			if err != nil {
				return fmt.Errorf("--id: %w", err)
			}

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			if s.Control == nil {
				return errors.New("this deployment has no control plane")
			}

			// Which plane holds it, asked before anything is erased.
			//
			// This erased on the control plane and nowhere else, and the
			// generated `Erase` answers no error for a row that is not there --
			// so revoking a **customer's** key, which the command beside this
			// one mints, reported success and left the key working. An operator
			// stopping a leaked credential was told it had stopped.
			//
			// Nothing about the identifier says which plane it is on: both are
			// `ApiKey` rows in the same domain, minted by the same generator.
			// So it is a lookup and not a guess, and a key on neither plane is
			// a refusal rather than a silent no-op.
			at, err := cmd.KeyPlane(ctx, s, k)
			if err != nil {
				return err
			}

			_, err = at.Ungated.ApiKey().Erase(ctx,
				app.ApiKeyRef_builder{Id: k.Bytes()}.Build())

			return err
		}),
	}
}
