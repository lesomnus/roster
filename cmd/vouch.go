package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/payday/pdcmd"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
	"github.com/lesomnus/roster/server/vouch"
)

// NewCmdVouch is `roster vouch`: the three things an operator does about
// somebody who cannot get in.
//
// # Why it is here and not only on a port
//
// It was only on a port, and the reason given was that generating a password is
// an act with a person on the other end of it -- so a terminal was the wrong
// place for it and the console was the right one.
//
// That is not a difference. An operator at a console is also a person at a
// screen reading a secret out; both reach the same `VouchService`, over the
// same rows, and `admin.addr` is one of them making an Rpc exactly as this is.
// What the reason actually described was which of the two the author had
// written, which is not a rule.
//
// The real line is the one every other local command is on: a shell holds the
// configuration file and the database, so what it can do is what the deployment
// can do. D57 made the same argument about `roster key add --tenant`, and this
// is the other half of it -- a key is a way in for a **machine**, and this is
// the way in for a person.
//
// # What it is not
//
// A way to read a password. There is none: what is stored is an argon2id hash,
// so `reset` generates and answers once, and `set` writes what somebody else
// chose. Neither can tell anybody what theirs was.
//
// # The keyring travels with it; the corpus is core's
//
// Built the way `cmd/admin.go` builds the same service. The keyring is handed
// to the vouch server, for the seed `enrol` wraps. The leaked-password corpus
// is not: it moved to `core` with the credential write, so `reset` and `set`
// run it through the layer -- this command reaches the same `Ungated` stack
// every caller does, and leaving the corpus off the vouch server here is not
// leaving it off the command.
//
// `WithReach` is left out here as it is there. It refuses somebody writing the
// credential of a person who holds more than they do, and it reads what the
// **caller** holds -- and a shell is not a caller. There is no frame at all, so
// the rule has nothing to compare and would refuse everything or nothing; the
// same waiver `mayGrant` takes for a call with no frame, said out loud.
func NewCmdVouch(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "vouch",
		Brief: "a person's way in: check it, mint it, hand it out, end it",

		Commands: xli.Commands{
			newCmdVouchReset(c),
			newCmdVouchSet(c),
			newCmdVouchUnlock(c),

			// The wire half, which dials where the three above open the
			// database; `vouchwire.go` says why the line falls there.
			newCmdVouchVerify(c),
			newCmdVouchDelegate(c),
			newCmdVouchContinue(c),
			newCmdVouchLink(c),
			newCmdVouchRedeem(c),
			newCmdVouchRevoke(c),
			newCmdVouchEnrol(c),
			newCmdVouchAccept(c),
		},
	}
}

// vouching is the service these three call, and the deployment that answers it.
//
// `Ungated` on both arguments, which is what `cmd/admin.go` passes as well:
// the wall narrows by a tenant the caller belongs to, and there is no caller.
func vouching(ctx context.Context, c *Config) (*Server, *vouch.Server, error) {
	s, err := Build(ctx, *c)
	if err != nil {
		return nil, nil, err
	}

	return s, vouch.New(s.Ungated, s.Ungated,
		vouch.WithKeys(s.Keyring)), nil
}

// whom is the person a command was pointed at.
//
// `pdcmd.ArgRef` and `whoIs` rather than a parser of this file's own, so that
// `@tenant/alias` means here what it means to `roster forget` and to every
// entity command.
func whom(ctx context.Context, s *Server, cmd *xli.Command) (*app.VouchWho, error) {
	ref, named := arg.Get[pdcmd.Ref](cmd, "REF")
	if !named {
		return nil, errors.New("REF: who, as @tenant/alias or an identifier")
	}
	if err := ref.Expect(pd.HolderDomain); err != nil {
		return nil, err
	}

	who, err := whoIs(ctx, s.Ent, ref)
	if err != nil {
		return nil, err
	}

	return app.VouchWho_builder{Id: who.Bytes()}.Build(), nil
}

func refArg() arg.Args {
	return arg.Args{
		&pdcmd.ArgRef{Name: "REF", Brief: "who, as @tenant/alias or an identifier"},
	}
}

// newCmdVouchReset generates a password and prints it once.
//
// Generated and not typed, which is the same decision `roster init` makes about
// the operator's own and `IssueService` makes about a key: a secret the caller
// chose is a secret the caller knows, and thirty-two bytes of `crypto/rand` is
// not a word anybody will recognise. What makes it safe is that it is shown
// once and the person is expected to change it.
func newCmdVouchReset(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "reset",
		Brief: "give somebody a new password, generated here and printed once",

		Args: refArg(),

		Flags: flg.Flags{
			&flg.String{Name: "kind", Brief: "which credential; empty is the password"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			s, v, err := vouching(ctx, c)
			if err != nil {
				return err
			}
			defer s.Close()

			who, err := whom(ctx, s, cmd)
			if err != nil {
				return err
			}

			kind, _ := flg.Find[string](cmd, "kind")

			res, err := v.Reset(ctx, app.VouchResetRequest_builder{Who: who, Kind: kind}.Build())
			if err != nil {
				return err
			}

			// To stdout and nowhere else, the way `roster key add` prints a
			// key: a credential that reaches a log has been given away, and
			// `$(roster vouch reset …)` should be the secret and nothing else.
			fmt.Fprintf(os.Stdout, "%s\n", res.GetSecret())
			fmt.Fprintln(os.Stderr,
				"Shown once. Everything they had signed in with stopped working: "+
					"a reset that left the old sessions alive would not be a reset.")

			return nil
		}),
	}
}

// newCmdVouchSet writes a password somebody else chose.
//
// On a pipe and never as an argument, which is `roster init --password-stdin`'s
// rule and `roster key add`'s reason for refusing to take a key: an argument is
// in the shell history and in the process list.
func newCmdVouchSet(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "set",
		Brief: "write a password somebody chose, read from stdin",

		Args: refArg(),

		Flags: flg.Flags{
			&flg.Switch{Name: "password-stdin", Brief: "read the password from stdin; required"},
			&flg.String{Name: "kind", Brief: "which credential; empty is the password"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			if v, _ := flg.Find[bool](cmd, "password-stdin"); !v {
				return errors.New(
					"--password-stdin: there is no flag to pass a password on, " +
						"because an argument is in the shell history and in the process list")
			}

			b, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<10))
			if err != nil {
				return fmt.Errorf("the password on stdin: %w", err)
			}

			secret := strings.TrimRight(string(b), "\r\n")
			if secret == "" {
				return errors.New("--password-stdin was given and stdin was empty")
			}

			s, _, err := vouching(ctx, c)
			if err != nil {
				return err
			}
			defer s.Close()

			who, err := whom(ctx, s, cmd)
			if err != nil {
				return err
			}

			kind, _ := flg.Find[string](cmd, "kind")

			// Set is a `Credential` write now, named by reference. Locally,
			// through `Ungated`, where a frameless caller waives the reach rule.
			if _, err := s.Ungated.Credential().Set(ctx, app.CredentialSetRequest_builder{
				Ref:    app.HolderRef_builder{Id: who.GetId()}.Build(),
				Kind:   kind,
				Secret: []byte(secret),
			}.Build()); err != nil {
				return err
			}

			fmt.Fprintln(os.Stderr, "set.")

			return nil
		}),
	}
}

// newCmdVouchUnlock opens an account that wrong answers closed.
//
// A lockout releases itself, so this is a convenience -- and it is also the
// answer to what locking by name costs: an account can be held closed by
// somebody else, and a person on site can simply open it. The secret is
// untouched, which is what makes it different from a reset.
func newCmdVouchUnlock(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "unlock",
		Brief: "open an account that wrong answers closed, without changing the secret",

		Args: refArg(),

		Flags: flg.Flags{
			&flg.String{Name: "kind", Brief: "which credential; empty is the password"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			s, _, err := vouching(ctx, c)
			if err != nil {
				return err
			}
			defer s.Close()

			who, err := whom(ctx, s, cmd)
			if err != nil {
				return err
			}

			kind, _ := flg.Find[string](cmd, "kind")

			// Unlock is a `Credential` write now (`Vouch.Unlock` moved onto the
			// entity), named by reference. Locally, through `Ungated`, so the
			// escalation rule waives itself for a frameless caller.
			res, err := s.Ungated.Credential().Unlock(ctx, app.CredentialUnlockRequest_builder{
				Ref:  app.HolderRef_builder{Id: who.GetId()}.Build(),
				Kind: kind,
			}.Build())
			if err != nil {
				return err
			}

			// What it was, so that an operator can tell "it was locked and now
			// is not" from "it was never locked" -- which is the difference
			// between having fixed something and having looked in the wrong
			// place.
			if u := res.GetWasLockedUntil(); u != nil {
				fmt.Fprintf(os.Stderr, "was locked until %s; open now.\n", u.AsTime().Format("15:04:05"))
			} else {
				fmt.Fprintln(os.Stderr, "was not locked.")
			}

			return nil
		}),
	}
}
