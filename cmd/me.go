package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// newCmdMe is `roster me`: what the caller is, and the four things they may do
// about themselves.
//
// # It is the first of D58's list, and the one that says why there is a list
//
// `MeService` is what a **person inside a tenant** asks about their own record,
// and until now it had no command at all -- so the caller this CLI is most
// obviously for could read every entity and not the one answer that is about
// them. The console had it and a terminal did not, which is the shape D58 is
// about.
//
// # It is remote only, and that is not an omission
//
// Every method here answers from the **frame**: no request carries a subject,
// which is `server/me`'s whole design -- a role naming `MeService/IssueKey`
// means *may mint a key that acts as you* and cannot be pointed at anybody
// else. A local run has no frame, because there is no caller; it opens the
// database.
//
// So this reaches the connection the entity commands reach, through
// `pdcmd.MustConn`, and a deployment with no `client.addr` is told to name one
// rather than answered from the rows. `roster holder get @you/you` is the local
// question and it is a different question: it says what is written down, not
// what you may do.
func newCmdMe(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "me",
		Brief: "what this credential is, and what it may do about itself",

		Commands: xli.Commands{
			newCmdMeGet(c),
			newCmdMeIssueKey(c),
			newCmdMeRevokeKey(c),
			newCmdMeLink(c),
			newCmdMeUnlink(c),
			newCmdMeSignOut(c),
		},
	}
}

// calling is the connection, or the sentence somebody needs when there is none.
//
// `MustConn` panics where the tree was not chained, which is a wiring mistake
// and stays one. What is checked here is the other thing: a **local**
// invocation, which has a connection to an in-process server that does not
// serve this service at all. Answering "unimplemented" would be true and
// useless.
func calling(ctx context.Context, c *Config) (pdcmd.Conn, error) {
	if c.Client.Local || c.Client.Addr == "" {
		return nil, errors.New(
			"`me` is about the caller, and a local run has none: it opens the database " +
				"rather than calling a server. name client.addr, and a credential to call with")
	}

	return pdcmd.MustConn(ctx), nil
}

func newCmdMeGet(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "get",
		Brief: "who this credential is, and what it may call",

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			conn, err := calling(ctx, c)
			if err != nil {
				return err
			}

			v, err := app.NewMeServiceClient(conn).Get(ctx, app.MeGetRequest_builder{}.Build())
			if err != nil {
				return err
			}

			k, _ := pdid.From(v.GetId())
			t, _ := pdid.From(v.GetTenant())

			cmd.Printf("%s\t%s\n", "alias", v.GetAlias())
			if n := v.GetName(); n != "" {
				cmd.Printf("%s\t%s\n", "name", n)
			}
			cmd.Printf("%s\t%s\n", "id", k)
			cmd.Printf("%s\t%s\n", "tenant", t)

			// The pattern and not what it expands to. A client that expanded it
			// would be showing the methods that exist in **this** binary, and
			// during a rolling deploy two of them would say different things
			// about one person.
			cmd.Printf("%s\t%s\n", "may call", strings.Join(v.GetMethods(), ", "))

			if v.GetEverySite() {
				cmd.Printf("%s\t%s\n", "sites", "every site")
			} else if n := len(v.GetSites()); n > 0 {
				cmd.Printf("%s\t%d\n", "sites", n)
			}

			for _, w := range v.GetIdentities() {
				cmd.Printf("%s\t%s:%s\n", "signs in with", w.GetProvider(), w.GetSubject())
			}
			for _, w := range v.GetCredentials() {
				cmd.Printf("%s\t%s\n", "signs in with", w.GetKind())
			}
			for _, w := range v.GetKeys() {
				j, _ := pdid.From(w.GetId())
				cmd.Printf("%s\t%s\t%s\t%s\n", "key", w.GetAlias(), j, strings.Join(w.GetMethods(), ","))
			}

			return nil
		}),
	}
}

// newCmdMeIssueKey mints one for yourself.
//
// No subject in the request, which is the whole of what makes this safe to
// grant: `IssueService.IssueKey` takes a `HolderRef` the wall narrows to the
// caller's tenant, so the smallest role covering *mint a key for myself* there
// is *mint one for anybody here*. See `me.proto`'s IssueKey comment.
func newCmdMeIssueKey(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "issue-key",
		Brief: "mint a key that acts as you, and print it once",

		Flags: flg.Flags{
			&flg.String{Name: "name", Brief: "what to call it, unique among yours"},
			&flg.Strings{Name: "allow", Brief: "the methods it may call; repeat it, or comma separate"},
			&flg.String{Name: "expires", Brief: "how long it lasts, e.g. 720h; empty is forever"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			conn, err := calling(ctx, c)
			if err != nil {
				return err
			}

			name, _ := flg.Find[string](cmd, "name")
			if name == "" {
				name = "default"
			}

			allow, _ := flg.Find[[]string](cmd, "allow")
			methods := splitMethods(allow)
			if len(methods) == 0 {
				return errors.New("--allow: a key that allows nothing is not a key; name the methods")
			}

			req := app.MeIssueKeyRequest_builder{Alias: name, Methods: methods}
			if v, _ := flg.Find[string](cmd, "expires"); v != "" {
				d, err := time.ParseDuration(v)
				if err != nil {
					return fmt.Errorf("--expires: %w", err)
				}

				req.Expires = timestamppb.New(time.Now().Add(d))
			}

			v, err := app.NewMeServiceClient(conn).IssueKey(ctx, req.Build())
			if err != nil {
				return err
			}

			// To stdout and nowhere else, as `roster key add` prints one.
			fmt.Fprintf(os.Stdout, "%s\n", v.GetToken())
			fmt.Fprintf(os.Stderr,
				"key %q, allowing %d method(s). This is the only time it is shown.\n",
				name, len(methods))

			if w := Widest(methods); w != "" {
				fmt.Fprintf(os.Stderr, "\n%s\n", w)
			}

			return nil
		}),
	}
}

func newCmdMeRevokeKey(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "revoke-key",
		Brief: "stop one of your keys, now",

		Args: arg.Args{
			&arg.String{Name: "Id", Brief: "the key, as `me get` prints it"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			conn, err := calling(ctx, c)
			if err != nil {
				return err
			}

			v, ok := arg.Get[string](cmd, "Id")
			if !ok || v == "" {
				return errors.New("Id: which key")
			}

			k, err := pdid.Parse(v)
			if err != nil {
				return fmt.Errorf("Id: %w", err)
			}

			_, err = app.NewMeServiceClient(conn).RevokeKey(ctx,
				app.MeRevokeKeyRequest_builder{Id: k.Bytes()}.Build())

			return err
		}),
	}
}

// newCmdMeLink attaches a provider account you have already proved is yours.
//
// It is **not** waived by `aboutYourself` and there is no role that comes with
// it: attaching a way in is a feature a deployment offers rather than something
// nobody may be locked out of. So this is refused unless somebody granted it,
// which is the answer and not a limitation.
func newCmdMeLink(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "link",
		Brief: "attach a provider account of yours",

		Args: arg.Args{
			&arg.String{Name: "PROVIDER", Brief: `the provider, e.g. "entra"`},
			&arg.String{Name: "SUBJECT", Brief: "who that provider says you are"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			conn, err := calling(ctx, c)
			if err != nil {
				return err
			}

			provider, _ := arg.Get[string](cmd, "PROVIDER")
			subject, _ := arg.Get[string](cmd, "SUBJECT")
			if provider == "" || subject == "" {
				return errors.New("PROVIDER and SUBJECT: which account, at which provider")
			}

			_, err = app.NewMeServiceClient(conn).Link(ctx,
				app.MeLinkRequest_builder{Provider: provider, Subject: subject}.Build())

			return err
		}),
	}
}

// newCmdMeUnlink takes one back.
//
// It refuses your **only** way in, which is the one rule here that is not about
// permissions: somebody who unlinks their last provider and holds no password
// is locked out of an account nobody can let them back into.
func newCmdMeUnlink(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "unlink",
		Brief: "take back a provider account of yours",

		Args: arg.Args{
			&arg.String{Name: "Id", Brief: "the identity, as `me get` prints it"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			conn, err := calling(ctx, c)
			if err != nil {
				return err
			}

			v, ok := arg.Get[string](cmd, "Id")
			if !ok || v == "" {
				return errors.New("Id: which identity")
			}

			k, err := pdid.Parse(v)
			if err != nil {
				return fmt.Errorf("Id: %w", err)
			}

			_, err = app.NewMeServiceClient(conn).Unlink(ctx,
				app.MeUnlinkRequest_builder{Id: k.Bytes()}.Build())

			return err
		}),
	}
}

// newCmdMeSignOut voids the sessions and delegations issued before now.
//
// Not the keys, and that is the schema's decision rather than an omission here:
// `Holder.date_invalidated` says in as many words that it *deliberately does
// not void an `ApiKey`*, because a key is named, listed and revoked one at a
// time and killing somebody's scripts silently under "sign out everywhere" is
// an outage with nothing anywhere saying why. `me revoke-key` is the second act
// and has the second name.
//
// So this says what it does and no more. There is no undo and no time to give,
// because the server stamps the moment.
func newCmdMeSignOut(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "sign-out-everywhere",
		Brief: "void the sessions and delegations issued before now",

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			conn, err := calling(ctx, c)
			if err != nil {
				return err
			}

			v, err := app.NewMeServiceClient(conn).SignOutEverywhere(ctx,
				app.MeSignOutEverywhereRequest_builder{}.Build())
			if err != nil {
				return err
			}

			// Said exactly, because the difference is the one somebody would
			// otherwise find out about from a script that went on working.
			cmd.Printf("sessions and delegations issued before %s are void.\n",
				v.GetDateInvalidated().AsTime().Format(time.RFC3339))
			cmd.Printf("your keys are not: `me get` lists them, `me revoke-key` stops one.\n")

			return nil
		}),
	}
}
