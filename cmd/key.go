package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/keys"
)

// NewCmdKey is `roster key`: what the deployment's owner hands to their
// services.
//
// It is a command rather than an RPC because of what it writes to. The control
// plane is not served -- `ApiKeyService` is not registered and is closed to the
// batch, for the reason every verifier is -- so the only way in is a server
// instance this process holds, and the only thing holding one is this.
//
// That is a real limitation and it is written down rather than worked around: a
// deployment's owner needs a shell on the box to make a key. What replaces it
// is an admin console, which is a thing this app does not have and which would
// itself need a key to talk to. See PLAN.md.
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
		Brief: "mint a key for a service, and print it once",

		Flags: flg.Flags{
			&flg.String{Name: "service", Brief: "the holder in the control plane this key is for"},
			&flg.String{Name: "name", Brief: "what to call this key, unique per service"},
			&flg.String{Name: "allow", Brief: "the methods it may call, comma separated"},
			&flg.String{Name: "expires", Brief: "how long it lasts, e.g. 720h; empty is forever"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			service, _ := flg.Find[string](cmd, "service")
			if service == "" {
				return errors.New("--service: which service is this key for")
			}

			name, _ := flg.Find[string](cmd, "name")
			if name == "" {
				name = "default"
			}

			allow, _ := flg.Find[string](cmd, "allow")
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

			if s.Control == nil {
				return errors.New("this deployment has no control plane; see `control` in the configuration")
			}
			if err := s.Control.Ent.Schema.Create(ctx); err != nil {
				return err
			}

			who, err := serviceOf(ctx, s.Control, service)
			if err != nil {
				return err
			}

			// The deployment's own. `roster key add` is the operator at a
			// shell, and a key for somebody inside a tenant is not something a
			// shell on the box should be handing out.
			token, sum, err := keys.Mint(keys.PrefixDeployment)
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

			v, err := s.Control.Ungated.ApiKey().Add(ctx, req.Build())
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
				"key %s for @%s, allowing %d method(s). This is the only time it is shown.\n",
				k, service, len(methods))

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

			vs, err := s.Control.Ent.ApiKey.Query().WithHolder().All(ctx)
			if err != nil {
				return err
			}

			for _, v := range vs {
				who := "?"
				if v.Edges.Holder != nil {
					who = v.Edges.Holder.Alias
				}

				used := "never"
				if v.DateUsed != nil {
					used = v.DateUsed.Format(time.RFC3339)
				}

				fmt.Fprintf(os.Stdout, "%s\t@%s/%s\tused=%s\t%s\n",
					pdid.Id(v.ID), who, v.Alias, used, strings.Join(v.Methods, ","))
			}

			return nil
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

			_, err = s.Control.Ungated.ApiKey().Erase(ctx,
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

	return holderOf(ctx, s, pdid.Id(t.ID), alias)
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

// splitMethods is the `--allow` list, tolerating the spacing a shell leaves.
func splitMethods(v string) []string {
	var vs []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			vs = append(vs, s)
		}
	}

	return vs
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
