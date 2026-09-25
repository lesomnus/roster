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

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// NewCmdKey is `roster key`: a customer's keys, and every key's listing and
// revocation.
//
// It is a command rather than an RPC because of what the first of those writes
// to. A key is minted before there is anything to mint it with -- and
// `ApiKey.Add`, which takes a caller-chosen verifier, is shut a method at a time
// for the reason every verifier is -- so the only way in is a server instance
// this process holds, and the only thing holding one is this.
//
// (`ApiKey.Issue` mints one over the wire for a console and is what a
// customer's person reaches; neither is what a shell has before the first key
// exists.)
//
// # `add` is one plane, `list` and `revoke` are both
//
// `add` writes, and a write is on one database: this one is the data plane's,
// and the control plane's is `roster control key add`. `list` and `revoke`
// stay here and answer across both, because an operator stopping a leaked key
// holds its identifier and nothing else -- and nothing in an identifier says
// which plane it is on.
//
// # A customer's, which it once refused to mint
//
// `--tenant` and `--holder` mint an `rt_` for one of a customer's people. This
// said, in as many words, that *a key for somebody inside a tenant is not
// something a shell on the box should be handing out* -- and the admin console was
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
		Brief: "a customer's keys to mint, and any key to list or revoke",

		Commands: xli.Commands{
			newCmdKeyAdd(c),
			newCmdKeyList(c),
			newCmdKeyRevoke(c),
		},
	}
}

// newCmdKeyAdd mints one for a customer's person, and prints it once.
//
// Once because what is stored is a hash. This deployment cannot tell anybody
// what their key was any more than it can tell them their password, and a
// command that could print it again would be one that had kept it.
//
// The data plane's alone. A key for this deployment's own services is `roster
// control key add`: it was `--service` here, a flag that turned this command
// into one writing to a different database -- and "service" read, to somebody
// running a tenant, as *my tenant's machine*, which is this command and not
// that one. See `control.go`.
func newCmdKeyAdd(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "add",
		Brief: "mint a key for one of a customer's people, and print it once",

		Flags: append(flg.Flags{
			&flg.String{Name: "tenant", Brief: "the customer, by alias or identifier"},
			&flg.String{Name: "holder", Brief: "whose key this is, inside --tenant"},
		}, mintFlags()...),

		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			tenant, _ := flg.Find[string](cl, "tenant")
			holder, _ := flg.Find[string](cl, "holder")

			switch {
			case tenant == "" && holder == "":
				return errors.New(
					"--tenant and --holder: whose key this is; " +
						"a key for one of this deployment's own services is `roster control key add`")
			case tenant == "":
				return fmt.Errorf("--tenant: which customer's %q, since an alias names one per tenant", holder)
			case holder == "":
				return fmt.Errorf("--holder: whose key this is, inside %q", tenant)
			}

			m, err := mintingOf(cl)
			if err != nil {
				return err
			}

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// Looked up and never created. A customer's people are the
			// customer's, and a command that made one by mentioning them would
			// be a way to write rows into somebody else's tenant by typo --
			// which is the rule `IssueKeyRequest.holder` already states about
			// the same act over the wire.
			who, err := cmd.CustomerOf(ctx, s, tenant, holder)
			if err != nil {
				return err
			}

			return m.mint(ctx, s.Ungated, who, keys.PrefixTenant, "@"+tenant+"/"+holder)
		}),
	}
}

// mintFlags are what a key is, whichever plane it lands on.
func mintFlags() flg.Flags {
	return flg.Flags{
		&flg.String{Name: "name", Brief: "what to call this key, unique per holder"},
		&flg.Strings{Name: "allow", Brief: "the methods it may call; repeat it, or comma separate"},
		&flg.String{Name: "expires", Brief: "how long it lasts, e.g. 720h; empty is forever"},
	}
}

// minting is a key about to be minted, read from [mintFlags] before anything
// is opened -- so a key that allows nothing is refused without a database.
type minting struct {
	name    string
	methods []string
	expires *timestamppb.Timestamp
}

func mintingOf(cl *xli.Command) (minting, error) {
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
		return minting{}, errors.New("--allow: a key that allows nothing is not a key; name the methods")
	}

	m := minting{name: name, methods: methods}
	if v, _ := flg.Find[string](cl, "expires"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return minting{}, fmt.Errorf("--expires: %w", err)
		}

		m.expires = timestamppb.New(time.Now().Add(d))
	}

	return m, nil
}

// mint writes the row on `at`, hung off `who`, and prints the token once.
//
// The plane is the caller's to have decided, and the prefix travels with it:
// it is never something anybody names -- `issue.proto` is explicit that a
// caller who could name one could ask the customer-facing door for the
// deployment's own kind.
func (m minting) mint(ctx context.Context, at app.Server, who pdid.Id, prefix string, whose string) error {
	token, sum, err := keys.Mint(prefix)
	if err != nil {
		return err
	}

	v, err := at.ApiKey().Add(ctx, app.ApiKeyAddRequest_builder{
		Holder:      app.HolderRef_builder{Id: who.Bytes()}.Build(),
		Alias:       m.name,
		Secret:      sum,
		Methods:     m.methods,
		DateExpires: m.expires,
	}.Build())
	if err != nil {
		return err
	}

	k, err := pdid.From(v.GetId())
	if err != nil {
		return err
	}

	// To stdout and nowhere else. It is not logged, because a credential that
	// reaches a log has been given away.
	fmt.Fprintf(os.Stdout, "%s\n", token)
	fmt.Fprintf(os.Stderr,
		"key %s for %s, allowing %d method(s). This is the only time it is shown.\n",
		k, whose, len(m.methods))

	if v := cmd.Widest(m.methods); v != "" {
		fmt.Fprintf(os.Stderr, "\n%s\n", v)
	}

	return nil
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
