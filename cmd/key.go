package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"
	"google.golang.org/protobuf/types/known/timestamppb"

	"uuid"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/ent"
	entapikey "github.com/lesomnus/roster/internal/ent/apikey"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// NewCmdKey is `roster key`: the keys this deployment is called with, on
// either plane.
//
// It is a command rather than an RPC because of what the first of those writes
// to. The control plane is not served -- `ApiKeyService` is not registered and
// is closed to the batch, for the reason every verifier is -- so the only way
// in is a server instance this process holds, and the only thing holding one is
// this.
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
// what `docs/operating.md` says about the local CLI everywhere else.
func NewCmdKey(c *Config) *xli.Command {
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
func newCmdKeyAdd(c *Config) *xli.Command {
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

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			service, _ := flg.Find[string](cmd, "service")
			tenant, _ := flg.Find[string](cmd, "tenant")
			holder, _ := flg.Find[string](cmd, "holder")

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

			name, _ := flg.Find[string](cmd, "name")
			if name == "" {
				name = "default"
			}

			allow, _ := flg.Find[[]string](cmd, "allow")
			methods := splitMethods(allow)
			if len(methods) == 0 {
				// Refused rather than defaulted, in either direction. Defaulting
				// to everything hands out more than anybody asked for, and
				// defaulting to nothing mints a key that silently does not work
				// -- which is worse to debug than being told now.
				return errors.New("--allow: a key that allows nothing is not a key; name the methods")
			}

			s, err := Build(ctx, *c)
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
				if err := s.Control.Ent.Schema.Create(ctx); err != nil {
					return err
				}

				// Named into existence, which is `serviceOf`'s decision and the
				// right one there: a service is not something somebody sets up
				// on purpose before they need it, and the control plane has one
				// tenant so an alias names one person.
				who, err = serviceOf(ctx, s.Control, service)
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
				who, err = customerOf(ctx, s, tenant, holder)
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

			if v, _ := flg.Find[string](cmd, "expires"); v != "" {
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

			if v := Widest(methods); v != "" {
				fmt.Fprintf(os.Stderr, "\n%s\n", v)
			}

			return nil
		}),
	}
}

// newCmdKeyList says what exists, and never what any of them are.
func newCmdKeyList(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "list",
		Brief: "what keys exist, and what each may call",

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			s, err := Build(ctx, *c)
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
			if err := listKeys(ctx, os.Stdout, s.Control.Ent, ""); err != nil {
				return err
			}

			return listKeys(ctx, os.Stdout, s.Ent, "")
		}),
	}
}

// newCmdKeyRevoke stops one, now.
//
// A delete and not a flag, which is the whole reason a key is a row: the next
// call carrying it finds nothing and is refused, with no window and nothing to
// expire. The trail keeps what it was.
func newCmdKeyRevoke(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "revoke",
		Brief: "stop a key, now",

		Flags: flg.Flags{
			&flg.String{Name: "id", Brief: "the key, as `key list` prints it"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			v, _ := flg.Find[string](cmd, "id")
			if v == "" {
				return errors.New("--id: which key")
			}

			k, err := pdid.Parse(v)
			if err != nil {
				return fmt.Errorf("--id: %w", err)
			}

			s, err := Build(ctx, *c)
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
			at, err := keyPlane(ctx, s, k)
			if err != nil {
				return err
			}

			_, err = at.Ungated.ApiKey().Erase(ctx,
				app.ApiKeyRef_builder{Id: k.Bytes()}.Build())

			return err
		}),
	}
}

// serviceOf is the holder a key is for, made if this is the first key for it.
//
// Made rather than refused, because a service is not a thing somebody creates
// on purpose before they need it: `key add --service custody` is the moment
// custody becomes a caller of this deployment, and asking for two commands to
// express one intent is how a runbook grows a step nobody remembers.
//
// The tenant it goes in is the control plane's only one, made here if the
// database is new. There is nothing to choose: a control plane has one owner.
// ServiceOf is [serviceOf], exported for a test that stands in for the command.
func ServiceOf(ctx context.Context, s *Server, alias string) (pdid.Id, error) {
	return serviceOf(ctx, s, alias)
}

func serviceOf(ctx context.Context, s *Server, alias string) (pdid.Id, error) {
	t, err := s.Ent.Tenant.Query().First(ctx)
	if err != nil {
		v, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{
			Alias: "owner",
		}.Build())
		if err != nil {
			return pdid.Nil, err
		}

		return holderOf(ctx, s, mustFrom(v.GetId()), alias)
	}

	return holderOf(ctx, s, pdid.Id(t.Id), alias)
}

// customerOf is one of a customer's people, by the tenant they are in and their
// alias, and is a refusal where there is no such person.
//
// Looked up and never created, which is the difference between this and
// [serviceOf] beside it. The control plane has one tenant and a service is a
// row somebody names when they need it; the data plane has many, a customer's
// people are the customer's, and a command that made one by mentioning them
// would write rows into somebody else's tenant by typo.
//
// The tenant is an alias or an identifier, because both are things somebody has
// to hand: `roster tenant ls` prints the first and an app that anchors on this
// organisation was given the second.
func customerOf(ctx context.Context, s *Server, tenant string, alias string) (pdid.Id, error) {
	v, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Alias:  z.Ptr(alias),
				Tenant: tenantRef(tenant),
			}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return pdid.Nil, fmt.Errorf("@%s/%s: %w", tenant, alias, err)
	}

	return pdid.From(v.GetId())
}

// tenantRef is what somebody typed, as the reference an Rpc takes.
//
// An identifier if it parses as one and an alias otherwise. There is no
// ambiguity to resolve: an alias is `[a-z0-9-]` shaped and a UUID is not
// something anybody chooses as one.
func tenantRef(v string) *app.TenantRef {
	if k, err := pdid.Parse(v); err == nil {
		return app.TenantRef_builder{Id: k.Bytes()}.Build()
	}

	return app.TenantRef_builder{Alias: z.Ptr(v)}.Build()
}

func holderOf(ctx context.Context, s *Server, in pdid.Id, alias string) (pdid.Id, error) {
	v, err := s.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{
			Slug: app.HolderRefBySlug_builder{
				Alias:  z.Ptr(alias),
				Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	if err == nil {
		return pdid.From(v.GetId())
	}

	w, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: in.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	if err != nil {
		return pdid.Nil, err
	}

	return pdid.From(w.GetId())
}

func mustFrom(b []byte) pdid.Id {
	k, _ := pdid.From(b)

	return k
}

// splitMethods is the `--allow` list, however it was written.
//
// Every occurrence of the flag, and every comma inside one, so these are the
// same key:
//
//	--allow a,b --allow c
//	--allow a --allow b --allow c
//	--allow 'a, b, c'
//
// The flag is `flg.Strings` and not `flg.String`, which is the whole of the
// fix and was the whole of the bug. A scalar flag takes the **last**
// occurrence -- which is right for `--config` and every other *choose one*
// flag, and silently wrong for a list. `roster key add --service kamino
// --allow /roster.VouchService/Verify --allow /roster.HolderService/Get` minted
// a key allowing the second and nothing else, and the only sign was the line
// this prints saying `allowing 1 method(s)`.
//
// It was documented that way in another app, which is how it was found: an
// operator following a runbook gets a key that fails on its first call.
//
// xli already had the type. `flg.Strings` is `Multi[string, StringParser]` and
// appends per occurrence -- so nothing was missing anywhere but here.
//
// The spacing is tolerated because a list somebody wrapped for a runbook has
// some.
func splitMethods(vs []string) []string {
	var out []string
	for _, v := range vs {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}

	return out
}

// Widest says so when a key's methods reach the one read that is wider than
// every other one put together.
//
// # Why this and nothing else
//
// A key is the deployment's, and the deployment is every tenant in it -- so
// `cmd.Policy.Where` answers `frame.Everything` and the wall narrows nothing.
// That is the design and it is right: a key allowed `/roster.HolderService/List`
// reads every customer's people, and a service that manages customers has to.
//
// `AuditService` is the same property with a different magnitude. `Audit.value`
// is the row as each write left it, so one method answers **every table's
// contents, across every tenant, across all time** -- including rows long since
// deleted, since nothing erases a trail row. It is the single widest read this
// app has, and `cmd/trailkey_test.go` is what says so.
//
// Said rather than refused. A compliance exporter is a real service and this is
// the method it needs; what is wrong is granting it by reaching for `*` and not
// noticing. So this is the sentence that makes somebody notice, once, at the
// moment they could still choose otherwise.
func Widest(methods []string) string {
	wide := []struct {
		method string
		why    string
	}{
		{app.AuditService_List_FullMethodName, readsTheTrail},
		{app.AuditService_Get_FullMethodName, readsTheTrail},
		{app.VouchService_Accept_FullMethodName, mintsForAnybody},
		{app.IssueService_IssuePassword_FullMethodName, becomesAnOperator},
	}

	for _, held := range methods {
		for _, v := range wide {
			if !frame.Covers(held, v.method) {
				continue
			}

			return "NOTE: `" + held + "` " + v.why
		}
	}

	return ""
}

const readsTheTrail = "reaches the audit trail, which holds the contents of every\n" +
	"write in this deployment, in every tenant, for as long as the retention policy\n" +
	"keeps them. A key is not walled by tenant. Grant it only to something that has\n" +
	"to read other customers' history."

// accepts is the other one, and it is the sharper of the two.
//
// `Vouch.Accept` mints a delegation for **anybody**, with no secret and no
// proof: it exists so that a front door which ran an OIDC flow can say who it
// checked, and the grant is the whole of the control. Reading a trail is
// reading; this is acting as somebody.
const mintsForAnybody = "mints a credential for anybody, on the caller's word.\n" +
	"`Vouch.Accept` is for a front door that did its own checking -- an OIDC flow --\n" +
	"and it verifies nothing itself. An app that checks passwords through `Verify`\n" +
	"does not need it, and should not have it."

// And the third, found by pointing `roster issue` at the control port: the
// grant rule reads bindings and a key holds none, so a key can never mint a
// key -- but writing a credential asks the *reach* rule, which everything a
// deployment key carries covers. So a key whose methods reach `IssuePassword`
// hands out first passwords, and a first password for an operator is a way to
// become them.
const becomesAnOperator = "hands out first passwords, on the caller's word.\n" +
	"Against the control plane that is every operator of this deployment: a key is\n" +
	"not a person, so no binding narrows whose credential it may write. Grant it\n" +
	"only to a console."

// listKeys writes one plane's keys, in the shape `roster key list` prints them.
//
// The tenant is printed as well as the alias, which the control plane's version
// did not need and this one does: an alias is unique within a tenant, so
// `@alice/laptop` is ambiguous across customers and `@newco/alice/laptop` is
// not.
func listKeys(ctx context.Context, w io.Writer, db *ent.Client, _ string) error {
	// Erased rows left out, which reading ent directly does not do for you:
	// erasure is applied by the servers and this is under them. A `list` that
	// showed a key somebody had just revoked would be worse than one that is a
	// moment behind.
	vs, err := db.ApiKey.Query().
		Where(entapikey.DateErasedIsNil()).
		WithHolder(func(q *ent.HolderQuery) { q.WithTenant() }).
		All(ctx)
	if err != nil {
		return err
	}

	for _, v := range vs {
		who := "?"
		if h := v.Edges.Holder; h != nil {
			who = h.Alias
			if t := h.Edges.Tenant; t != nil {
				who = t.Alias + "/" + who
			}
		}

		used := "never"
		if v.DateUsed != nil {
			used = v.DateUsed.Format(time.RFC3339)
		}

		fmt.Fprintf(w, "%s\t@%s/%s\tused=%s\t%s\n",
			pdid.Id(v.Id), who, v.Alias, used, strings.Join(v.Methods, ","))
	}

	return nil
}

// keyPlane is the server holding the key with this identifier.
//
// Both planes are asked because both mint them and nothing about an identifier
// says which one it came from -- they are `ApiKey` rows in the same domain from
// the same generator. The control plane first, since that is where a key
// somebody is revoking in a hurry usually is.
//
// A key on neither is an error. It was a silent success, which is the direction
// that matters for this particular act: somebody stopping a credential they
// believe is leaked must not be told it stopped when it did not.
func keyPlane(ctx context.Context, s *Server, k pdid.Id) (*Server, error) {
	for _, at := range []*Server{s.Control, s} {
		n, err := at.Ent.ApiKey.Query().
			Where(entapikey.IdEQ(uuid.UUID(k)), entapikey.DateErasedIsNil()).
			Count(ctx)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			return at, nil
		}
	}

	return nil, fmt.Errorf("--id: no key %s on either plane", k)
}
