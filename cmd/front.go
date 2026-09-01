package cmd

import (
	"context"
	"errors"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"

	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// newCmdFront is `roster front`: the two questions a front door asks before it
// knows who anybody is.
//
// They are worth a command because they are the questions an operator debugs
// with. "Why does this name not resolve" and "where do we send @contoso.com"
// are asked at a shell long before they are asked by a browser, and the answer
// that matters is the served one -- what a front door would actually be told --
// not what the rows say.
//
// # It is remote only, like `me`, and for the adjacent reason
//
// These are questions to a **served deployment**, and the local pipe mounts
// the entity services alone. A shell on the box reading the rows is
// `roster host ls` and `roster maildomain ls`, which is a different question:
// what is written down, not what a front door is told.
func newCmdFront(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "front",
		Brief: "what a front door is told, before anybody is signed in",

		Commands: xli.Commands{
			newCmdFrontWhoseHost(c),
			newCmdFrontWhereFrom(c),
		},
	}
}

// fronting is the connection, or the sentence somebody needs when there is
// none. See [calling], whose shape this is.
func fronting(ctx context.Context, c *Config) (pdcmd.Conn, error) {
	if c.Client.Local || c.Client.Addr == "" {
		return nil, errors.New(
			"`front` asks a served deployment what a front door would be told, and a local run " +
				"reads the database instead. name client.addr; the rows behind the answers are " +
				"`roster host ls` and `roster maildomain ls`")
	}

	return pdcmd.MustConn(ctx), nil
}

func newCmdFrontWhoseHost(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "whose-host",
		Brief: "the tenant a name belongs to",

		Args: arg.Args{
			&arg.String{Name: "HOST", Brief: "the name a browser arrived at; a port is fine"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			conn, err := fronting(ctx, c)
			if err != nil {
				return err
			}

			host, _ := arg.Get[string](cmd, "HOST")
			if host == "" {
				return errors.New("HOST: which name")
			}

			v, err := app.NewFrontServiceClient(conn).WhoseHost(ctx,
				app.FrontWhoseHostRequest_builder{Host: host}.Build())
			if err != nil {
				return err
			}

			// The identifier and nothing else, exactly as the Rpc answers --
			// so `$(roster front whose-host …)` is a tenant for the next
			// command, the same way a front door uses it.
			k, _ := pdid.From(v.GetTenant())
			cmd.Printf("%s\n", k)

			return next(ctx)
		}),
	}
}

func newCmdFrontWhereFrom(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "where-from",
		Brief: "which provider the people at an address authenticate with",

		Args: arg.Args{
			&arg.String{Name: "TENANT", Brief: "the tenant, as whose-host answers it"},
			&arg.String{Name: "ADDRESS", Brief: "an address, or the domain alone"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			conn, err := fronting(ctx, c)
			if err != nil {
				return err
			}

			t, _ := arg.Get[string](cmd, "TENANT")
			addr, _ := arg.Get[string](cmd, "ADDRESS")
			if t == "" || addr == "" {
				return errors.New("both a tenant and an address")
			}

			k, err := pdid.Parse(t)
			if err != nil {
				return errors.New("TENANT: an identifier, which is what whose-host answers; " +
					"an alias would be a read this caller may not have")
			}

			v, err := app.NewFrontServiceClient(conn).WhereFrom(ctx,
				app.FrontWhereFromRequest_builder{Tenant: k.Bytes(), Address: addr}.Build())
			if err != nil {
				return err
			}

			// Empty is an answer -- a domain this deployment says nothing
			// about -- and it prints as one, because a front door that learns
			// nothing offers whatever it offers everybody.
			cmd.Printf("%s\n", v.GetProvider())

			return next(ctx)
		}),
	}
}
