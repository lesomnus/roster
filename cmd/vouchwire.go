package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lesomnus/z"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// The wire half of `roster vouch`: the sign-in surface, from a shell.
//
// # Why these dial where reset, set and unlock open the database
//
// The local three write facts -- a password, a lockout -- and a shell that
// holds the configuration file and the database may write them. These eight
// are calls a **caller** makes: a delegation, a link and a continuation are
// each bound to the caller they were issued to, and a local run has no caller
// at all -- the server refuses a frameless mint, deliberately. So each of
// these is remote only, and `client.auth` is not a formality: what a command
// may hand out (`--allow`) and whose credential it may write are measured
// against that credential, exactly as they would be for any app.
//
// # Secrets, in and out
//
// In: never on the command line. A password, a code, a link and a delegation
// are read from stdin, for `--password-stdin`'s reason -- an argument is in
// the shell history and in the process list. The one exception is a
// continuation, which is single use, lives five minutes and is worth nothing
// to a caller it was not issued to; it is passed as a flag because it is the
// value the previous command just printed and the round trip through stdin
// would make the two-step flow untypeable.
//
// Out: to stdout and nowhere else, once, the way `roster key add` prints a
// key. Everything that is commentary goes to stderr, so `$(roster vouch …)`
// is the secret and nothing else.
//
// # The uniform no survives the terminal
//
// The server made a wrong secret, an unknown name, a disabled account and a
// spent token indistinguishable on purpose. These commands keep it that way:
// every no is the same sentence, and `vouch link` prints the same shape for
// somebody who is not here as for somebody who is.

// wired is the connection, or the sentence somebody needs when there is none.
func wired(ctx context.Context, c *Config) (app.VouchServiceClient, func(), error) {
	conn, done, err := dialed(ctx, c)
	if err != nil {
		return nil, nil, err
	}

	return app.NewVouchServiceClient(conn), done, nil
}

// dialed is the wire these commands call a served deployment over, with the one
// refusal that is the same for all of them: this half needs a caller, so it
// needs `client.addr`. `enrol` reaches for the connection rather than a
// `VouchService` client on it, because the write it makes is `CredentialService`'s
// now.
func dialed(ctx context.Context, c *Config) (pdcmd.Conn, func(), error) {
	if c.Client.Local || c.Client.Addr == "" {
		return nil, nil, errors.New(
			"this half of `vouch` calls a served deployment as a caller -- a delegation, a link " +
				"and a continuation are bound to the caller they are issued to, and a local run " +
				"has none. name client.addr; the local half is `vouch reset|set|unlock`")
	}

	return remote{c}.Connect(ctx)
}

// refFrom is [whoSaid]'s answer as a `HolderRef`, for the writes that name a
// holder rather than a sign-in form. An address is refused here rather than at
// the server: `Credential.Enrol` names a holder, and an email is a sign-in
// form's way of finding one -- which recovery does and this does not.
func refFrom(who *app.VouchWho) (*app.HolderRef, error) {
	if id := who.GetId(); len(id) > 0 {
		return app.HolderRef_builder{Id: id}.Build(), nil
	}
	if who.GetAddress() != "" {
		return nil, errors.New(
			"enrol names a holder by @tenant/alias or an identifier, not an address -- " +
				"an email is how recovery finds somebody, not how a factor is added")
	}
	if who.GetTenant() != "" && who.GetAlias() != "" {
		return app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Tenant: app.TenantRef_builder{Alias: z.Ptr(who.GetTenant())}.Build(),
				Alias:  z.Ptr(who.GetAlias()),
			}.Build(),
		}.Build(), nil
	}

	return nil, errors.New("who: @tenant/alias or an identifier")
}

// whoSaid is the person, in the words a sign-in form collects.
//
// Unlike [whom], nothing is resolved here: a remote shell has no database to
// look a slug up in, and `VouchWho` already carries the name for the server to
// resolve -- which is also what keeps `@tenant/ghost` and a wrong password one
// indistinguishable no.
func whoSaid(cmd *xli.Command) (*app.VouchWho, error) {
	tenant, _ := flg.Find[string](cmd, "tenant")
	address, _ := flg.Find[string](cmd, "address")
	ref, named := arg.Get[pdcmd.Ref](cmd, "WHO")

	if address != "" {
		if named {
			return nil, errors.New("WHO or --address; naming somebody twice is not naming them better")
		}

		// The tenant does not travel inside an address, deliberately -- the
		// server refuses an address alone, so the refusal here can name the
		// flag instead of echoing the server's sentence.
		return app.VouchWho_builder{Tenant: tenant, Address: address}.Build(), nil
	}
	if tenant != "" {
		return nil, errors.New("--tenant goes with --address; with WHO the tenant is in the name")
	}
	if !named {
		return nil, errors.New("WHO: who, as @tenant/alias or an identifier -- or --tenant with --address")
	}
	if err := ref.Expect(pd.HolderDomain); err != nil {
		return nil, err
	}

	if !ref.Id.IsZero() {
		return app.VouchWho_builder{Id: ref.Id.Bytes()}.Build(), nil
	}

	return app.VouchWho_builder{Tenant: ref.Tenant, Alias: ref.Alias}.Build(), nil
}

// whoTyped is the WHO a command was given, back in the words it was given in,
// so a hint that echoes it names the same person the command just did.
func whoTyped(cmd *xli.Command) string {
	if tenant, _ := flg.Find[string](cmd, "tenant"); tenant != "" {
		if address, _ := flg.Find[string](cmd, "address"); address != "" {
			return fmt.Sprintf("--tenant %s --address %s", tenant, address)
		}
	}
	if ref, named := arg.Get[pdcmd.Ref](cmd, "WHO"); named {
		return pdcmd.RefParser{}.ToString(ref)
	}

	return ""
}

// nameFlag is ` --name "x"`, or nothing when the factor is the unnamed one --
// so the hint it goes in names the row the enrolment wrote and not another.
func nameFlag(name string) string {
	if name == "" {
		return ""
	}

	return fmt.Sprintf(" --name %q", name)
}

func whoArg() arg.Args {
	return arg.Args{
		&pdcmd.ArgRef{Name: "WHO", Optional: true, Brief: "who, as @tenant/alias or an identifier"},
	}
}

func whoFlags() flg.Flags {
	return flg.Flags{
		&flg.String{Name: "tenant", Brief: "the tenant's alias, for --address; with WHO it is in the name"},
		&flg.String{Name: "address", Brief: "an email address instead of a name, looked up within --tenant"},
	}
}

// secretFrom is [--password-stdin]'s rule under a wider name: what is proved
// here may be a password or a six-digit code, and neither belongs in the shell
// history or the process list.
func secretFrom(cmd *xli.Command, name string) ([]byte, error) {
	if on, _ := flg.Find[bool](cmd, name); !on {
		return nil, fmt.Errorf(
			"--%s: there is no flag to pass a secret on, because an argument is in the "+
				"shell history and in the process list", name)
	}

	b, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<10))
	if err != nil {
		return nil, err
	}
	if b = bytes.TrimRight(b, "\r\n"); len(b) == 0 {
		return nil, fmt.Errorf("--%s was given and stdin was empty", name)
	}

	return b, nil
}

// allowed is `--allow` exactly as `roster key add` takes it, refused when it
// is empty in the server's own words.
func allowed(cmd *xli.Command) ([]string, error) {
	vs, _ := flg.Find[[]string](cmd, "allow")
	methods := splitMethods(vs)
	if len(methods) == 0 {
		return nil, errors.New("--allow: a delegation that allows nothing opens no door; name the methods")
	}

	return methods, nil
}

// expiresOf turns `--expires 20m` into the absolute instant the schema takes.
func expiresOf(cmd *xli.Command) (*timestamppb.Timestamp, error) {
	v, _ := flg.Find[string](cmd, "expires")
	if v == "" {
		return nil, nil
	}

	d, err := time.ParseDuration(v)
	if err != nil {
		return nil, fmt.Errorf("--expires: %w", err)
	}

	return timestamppb.New(time.Now().Add(d)), nil
}

// answered renders a sign-in-shaped answer at a shell.
//
// Three answers, three renderings. Finished with a token: the token to
// stdout, once. A continuation: the continuation to stdout -- it is the thing
// the next command needs, and refusing the exit code would break the
// `$(roster vouch delegate …)` capture the two-step flow is typed as -- with
// the factors still open on stderr, which also says it is not a credential.
// No: an error, and deliberately one sentence for every reason.
func answered(v *app.VouchVerifyResponse, token string, expires *timestamppb.Timestamp) error {
	if w := v.GetLockedUntil(); w != nil {
		return fmt.Errorf("locked until %s. Attempts do not extend it, and `roster vouch unlock` is the operator's answer",
			w.AsTime().Format(time.RFC3339))
	}

	if cont := v.GetContinuation(); cont != "" {
		// The continuation still goes to stdout, so the two-step flow is
		// `cont=$(roster vouch delegate …)` and then a second call with it --
		// `$(…)` captures stdout whatever the exit code is.
		fmt.Fprintf(os.Stdout, "%s\n", cont)

		open := make([]string, 0, len(v.GetAvailable()))
		for _, f := range v.GetAvailable() {
			w := f.GetKind()
			if n := f.GetName(); n != "" {
				w += " " + fmt.Sprintf("%q", n)
			}
			if u := f.GetLockedUntil(); u != nil {
				w += " (locked until " + u.AsTime().Format(time.RFC3339) + ")"
			}

			open = append(open, w)
		}

		// But the exit is non-zero, and that is the fix for the one hazard a
		// continuation carries: it is a `vc_` on the same stdout a finished
		// `delegate` prints an `rd_` on, so `rd=$(roster vouch delegate …)`
		// would silently capture the wrong kind of token the day the account
		// grows a second factor. A distinct exit is what lets a script that
		// expected a delegation fail fast instead, while a two-step flow that
		// meant to carry on reads the continuation off stdout regardless.
		return fmt.Errorf(
			"not signed in yet: %s proved; still open: %s. The continuation is on stdout -- "+
				"single use, minutes, not a credential -- hand it with the next code to "+
				"`roster vouch delegate --continuation` to end in a token, or to `vouch continue` to only prove",
			strings.Join(v.GetSatisfied(), ", "), strings.Join(open, ", "))
	}

	if !v.GetOk() {
		return errors.New("no -- and one no for every reason: a wrong secret, an unknown name and a stopped account answer alike")
	}

	if token == "" {
		fmt.Fprintf(os.Stderr, "ok.\n")

		return nil
	}

	fmt.Fprintf(os.Stdout, "%s\n", token)
	fmt.Fprintf(os.Stderr,
		"a delegation, until %s. This is the only time it is shown. It rides in `roster-as` "+
			"beside the key that minted it, and is worth nothing without that key.\n",
		expires.AsTime().Format(time.RFC3339))

	return nil
}

// newCmdVouchVerify answers whether a secret is somebody's, and mints nothing.
//
// It is also the confirm step of an enrolment: a freshly enrolled factor does
// not count until one code has verified against it, and the confirming call
// names the row -- `--kind totp --name <what they called it>`.
func newCmdVouchVerify(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "verify",
		Brief: "answer whether a secret is somebody's; mints nothing",

		Args: whoArg(),
		Flags: append(whoFlags(),
			&flg.Switch{Name: "secret-stdin", Brief: "read the secret from stdin; required"},
			&flg.String{Name: "kind", Brief: "which kind of secret; empty is the password"},
			&flg.String{Name: "name", Brief: "which one, when they have two of a kind; confirming an enrolment names it"},
		),

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			cl, done, err := wired(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			who, err := whoSaid(cmd)
			if err != nil {
				return err
			}
			secret, err := secretFrom(cmd, "secret-stdin")
			if err != nil {
				return err
			}

			kind, _ := flg.Find[string](cmd, "kind")
			name, _ := flg.Find[string](cmd, "name")

			v, err := cl.Verify(ctx, app.VouchVerifyRequest_builder{
				Who:    who,
				Kind:   kind,
				Name:   name,
				Secret: secret,
			}.Build())
			if err != nil {
				return err
			}
			if err := answered(v, "", nil); err != nil {
				return err
			}

			return next(ctx)
		}),
	}
}

// newCmdVouchDelegate is verify and one more thing: on a finished proof it
// mints the credential to act as the person.
//
// The two-step flow is this command twice: the first run proves the password
// and prints a continuation, the second takes `--continuation` and the next
// factor's code and prints the token. `vouch continue` is the middle for
// flows with more than two steps, and it never mints.
func newCmdVouchDelegate(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "delegate",
		Brief: "sign somebody in and mint the credential that acts as them",

		Args: whoArg(),
		Flags: append(whoFlags(),
			&flg.Switch{Name: "secret-stdin", Brief: "read the secret from stdin; required"},
			&flg.String{Name: "kind", Brief: "which kind of secret; empty is the password"},
			&flg.String{Name: "name", Brief: "which one, when they have two of a kind"},
			&flg.String{Name: "continuation", Brief: "carry on a half-done sign-in instead of naming somebody"},
			&flg.Strings{Name: "allow", Brief: "the methods the delegation may call; repeat it, or comma separate"},
			&flg.String{Name: "expires", Brief: "how long it lasts, e.g. 20m; empty is the deployment's default"},
		),

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			cl, done, err := wired(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			req := app.VouchDelegateRequest_builder{}
			cont, _ := flg.Find[string](cmd, "continuation")
			_, named := arg.Get[pdcmd.Ref](cmd, "WHO")
			tenant, _ := flg.Find[string](cmd, "tenant")
			address, _ := flg.Find[string](cmd, "address")

			switch {
			case cont != "" && (named || tenant != "" || address != ""):
				// The server refuses who-and-continuation rather than resolving
				// one by precedence, so that a call cannot name one person and
				// sign in another. Said here, where the flags can be named.
				return errors.New(
					"a continuation carries on a sign-in already begun; naming somebody too " +
						"would be a second answer to who this is. Give one -- WHO to start, " +
						"--continuation to carry on")

			case cont != "":
				req.Continuation = cont

			default:
				who, err := whoSaid(cmd)
				if err != nil {
					return err
				}

				req.Who = who
			}

			secret, err := secretFrom(cmd, "secret-stdin")
			if err != nil {
				return err
			}
			req.Secret = secret

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

			kind, _ := flg.Find[string](cmd, "kind")
			name, _ := flg.Find[string](cmd, "name")
			req.Kind = kind
			req.Name = name

			v, err := cl.Delegate(ctx, req.Build())
			if err != nil {
				return err
			}
			if err := answered(v.GetVerified(), v.GetToken(), v.GetExpires()); err != nil {
				return err
			}
			if w := Widest(methods); w != "" && v.GetToken() != "" {
				fmt.Fprintf(os.Stderr, "\n%s\n", w)
			}

			return next(ctx)
		}),
	}
}

// newCmdVouchContinue proves the next factor, and only proves.
//
// There is exactly one method in the service that mints, and it is Delegate.
// A sign-in ended here is proven with nothing to show for it, and the command
// says so rather than leaving an empty success on the screen.
func newCmdVouchContinue(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "continue",
		Brief: "prove the next factor; Continue proves and Delegate mints",

		Flags: flg.Flags{
			&flg.String{Name: "continuation", Brief: "what the last call printed"},
			&flg.Switch{Name: "secret-stdin", Brief: "read the code from stdin; required"},
			&flg.String{Name: "kind", Brief: "which kind of secret; empty is the password"},
			&flg.String{Name: "name", Brief: "which one, when they have two of a kind"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			cl, done, err := wired(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			cont, _ := flg.Find[string](cmd, "continuation")
			if cont == "" {
				return errors.New("--continuation: what the last call printed; there is nothing to carry on from")
			}
			secret, err := secretFrom(cmd, "secret-stdin")
			if err != nil {
				return err
			}

			kind, _ := flg.Find[string](cmd, "kind")
			name, _ := flg.Find[string](cmd, "name")

			v, err := cl.Continue(ctx, app.VouchContinueRequest_builder{
				Continuation: cont,
				Kind:         kind,
				Name:         name,
				Secret:       secret,
			}.Build())
			if err != nil {
				return err
			}

			if w := v.GetVerified(); w.GetOk() {
				fmt.Fprintf(os.Stderr,
					"ok -- proven, and nothing was minted: Continue proves and Delegate mints. "+
						"To end in a token, give the last factor to `vouch delegate --continuation` instead.\n")

				return next(ctx)
			}
			if err := answered(v.GetVerified(), "", nil); err != nil {
				return err
			}

			return next(ctx)
		}),
	}
}

// newCmdVouchLink mints a way in and prints it once; delivering it is yours.
//
// The answer is the same for somebody who is not here -- a token, and an
// expiry -- because a recovery form is filled in by strangers, and a form
// that answered differently would answer *is this address here*. The dud
// fails at redeem the way every bad token does.
func newCmdVouchLink(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "link",
		Brief: "mint a way in for somebody, printed once; delivering it is yours",

		Args: whoArg(),
		Flags: append(whoFlags(),
			&flg.String{Name: "expires", Brief: "how long it lasts, e.g. 10m; less than the default, never more"},
		),

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			cl, done, err := wired(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			who, err := whoSaid(cmd)
			if err != nil {
				return err
			}
			expires, err := expiresOf(cmd)
			if err != nil {
				return err
			}

			v, err := cl.Link(ctx, app.VouchLinkRequest_builder{
				Who:     who,
				Expires: expires,
			}.Build())
			if err != nil {
				return err
			}

			fmt.Fprintf(os.Stdout, "%s\n", v.GetToken())
			fmt.Fprintf(os.Stderr,
				"until %s, single use. This is the only time it is shown, and roster does not "+
					"deliver it -- that is the point. It answers the same for a name that is "+
					"nobody's; a dud fails at redeem like any bad token.\n",
				v.GetExpires().AsTime().Format(time.RFC3339))

			return next(ctx)
		}),
	}
}

// newCmdVouchRedeem spends a link: a first factor, exactly as a password
// would have been -- a second factor is still asked for, because a link that
// skipped one would turn a mailbox into an account.
func newCmdVouchRedeem(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "redeem",
		Brief: "spend a link: a first factor, exactly as a password would be",

		Flags: flg.Flags{
			&flg.Switch{Name: "token-stdin", Brief: "read the link from stdin; required"},
			&flg.Strings{Name: "allow", Brief: "the methods the delegation may call; repeat it, or comma separate"},
			&flg.String{Name: "expires", Brief: "how long the delegation lasts, e.g. 20m; empty is the default"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			cl, done, err := wired(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			token, err := secretFrom(cmd, "token-stdin")
			if err != nil {
				return err
			}
			methods, err := allowed(cmd)
			if err != nil {
				return err
			}
			expires, err := expiresOf(cmd)
			if err != nil {
				return err
			}

			v, err := cl.Redeem(ctx, app.VouchRedeemRequest_builder{
				Token:   string(token),
				Methods: methods,
				Expires: expires,
			}.Build())
			if err != nil {
				return err
			}
			if err := answered(v.GetVerified(), v.GetToken(), v.GetExpires()); err != nil {
				return err
			}

			return next(ctx)
		}),
	}
}

// newCmdVouchRevoke ends a delegation before its expiry.
//
// Possession is the authorization: it takes the token itself, and a token
// that was never here, already gone, or somebody else's succeeds identically
// -- Revoke answers nothing a caller could learn from.
func newCmdVouchRevoke(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "revoke",
		Brief: "end a delegation now, before its expiry",

		Flags: flg.Flags{
			&flg.Switch{Name: "token-stdin", Brief: "read the delegation from stdin; required"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			conn, done, err := dialed(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			token, err := secretFrom(cmd, "token-stdin")
			if err != nil {
				return err
			}

			// `Revoke` is a `Delegation` write now (`Vouch.Revoke` moved onto the
			// entity), so this dials the `DelegationService` client. The command
			// keeps its `vouch revoke` name -- what a person types is not where
			// the Rpc lives.
			if _, err := app.NewDelegationServiceClient(conn).Revoke(ctx, app.DelegationRevokeRequest_builder{
				Token: string(token),
			}.Build()); err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr,
				"gone, if it was yours. A token that was never here, already gone, or "+
					"somebody else's answers the same.\n")

			return next(ctx)
		}),
	}
}

// newCmdVouchEnrol makes a second factor for somebody and prints it once.
//
// TOTP only: a WebAuthn enrolment is a browser ceremony -- the authenticator
// makes the secret and roster is handed the public half -- and a shell has
// nothing to attest with.
//
// What lands on stdout is the `otpauth://` Uri, which carries the seed in its
// `secret` parameter: one line that a QR encoder takes whole and a person can
// still read the seed out of.
//
// Remote, because this is the write a customer makes for their own account: a
// person with a key adds a factor to it, the way `set` and `unlock` are the
// operator's local writes. `Vouch.Enrol` moved onto `Credential`, so it names a
// holder by reference now -- `refFrom` turns the sign-in form the shell collects
// into one, and refuses an address, which is recovery's way of naming somebody
// and not this.
func newCmdVouchEnrol(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "enrol",
		Brief: "make a second factor for somebody, printed once as an otpauth:// Uri",

		Args: whoArg(),
		Flags: append(whoFlags(),
			&flg.String{Name: "kind", Brief: "the kind of factor; empty is totp, and webauthn needs a browser"},
			&flg.String{Name: "name", Brief: "what they call it, when there is more than one to tell apart"},
			&flg.String{Name: "issuer", Brief: "what their authenticator app lists it under; empty is \"roster\""},
		),

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			kind, _ := flg.Find[string](cmd, "kind")
			if kind == "" {
				kind = "totp"
			}
			if kind == "webauthn" {
				return errors.New(
					"--kind webauthn: an authenticator makes that one and roster is handed the " +
						"public half; there is nothing a shell can attest with")
			}

			conn, done, err := dialed(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			who, err := whoSaid(cmd)
			if err != nil {
				return err
			}
			ref, err := refFrom(who)
			if err != nil {
				return err
			}

			name, _ := flg.Find[string](cmd, "name")
			issuer, _ := flg.Find[string](cmd, "issuer")

			v, err := app.NewCredentialServiceClient(conn).Enrol(ctx, app.CredentialEnrolRequest_builder{
				Ref:    ref,
				Kind:   kind,
				Name:   name,
				Issuer: issuer,
			}.Build())
			if err != nil {
				return err
			}

			fmt.Fprintf(os.Stdout, "%s\n", v.GetUri())
			fmt.Fprintf(os.Stderr,
				"the seed is the `secret` parameter of the Uri, and this is the only time it is "+
					"shown. It does not count until it is proved: one code, via\n  roster vouch "+
					"verify --kind %s%s --secret-stdin %s\n",
				kind, nameFlag(name), whoTyped(cmd))

			return next(ctx)
		}),
	}
}

// newCmdVouchAccept mints for somebody a front door already checked, and
// checks nothing itself.
//
// The grant is the whole of the control -- `roster key add` warns when a key
// holds it -- and unlike everything else on this surface the answers here are
// real status codes: the caller holds a grant and is guessing nothing, so an
// unknown identity is NotFound rather than a uniform no.
func newCmdVouchAccept(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "accept",
		Brief: "mint for somebody a front door already checked; roster verifies nothing",

		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the tenant, as an identifier -- what `front whose-host` answers"},
			&flg.String{Name: "provider", Brief: "the deployment's name for the issuer, as Identity uses it"},
			&flg.String{Name: "subject", Brief: "the subject as the provider issued it"},
			&flg.Strings{Name: "allow", Brief: "the methods the delegation may call; repeat it, or comma separate"},
			&flg.String{Name: "expires", Brief: "how long it lasts, e.g. 20m; empty is minutes"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			cl, done, err := wired(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			tenant, _ := flg.Find[string](cmd, "tenant")
			provider, _ := flg.Find[string](cmd, "provider")
			subject, _ := flg.Find[string](cmd, "subject")

			k, err := pdid.Parse(tenant)
			if err != nil {
				return fmt.Errorf("--tenant: an identifier, which is what `front whose-host` answers: %w", err)
			}

			methods, err := allowed(cmd)
			if err != nil {
				return err
			}
			expires, err := expiresOf(cmd)
			if err != nil {
				return err
			}

			v, err := cl.Accept(ctx, app.VouchAcceptRequest_builder{
				Claim: app.VouchClaim_builder{
					Tenant:   k.Bytes(),
					Provider: provider,
					Subject:  subject,
				}.Build(),
				Methods: methods,
				Expires: expires,
			}.Build())
			if err != nil {
				return err
			}
			if err := answered(v.GetVerified(), v.GetToken(), v.GetExpires()); err != nil {
				return err
			}

			return next(ctx)
		}),
	}
}
