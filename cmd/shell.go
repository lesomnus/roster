// Package cmd, this file: who is at a shell on the box, and what the access
// record is told of the calls they make there (#221).
//
// # The calls are the deployment's, and the record names a person
//
// A command at a shell on the server's own box reaches `Ungated` on a pipe in
// its own process (`cli/entity.go`): no wall, no gate, and no frame -- and the
// layers read *no frame* as the deployment's own work, which is what the box
// is (`server/core/declared.go`, `deployment`). That stays exactly as it is.
//
// What it lacked was the record. A read through the API is a row of payday's
// access record (payday#111) naming the caller its frame names; a read at the
// box named nobody, because nothing was recording it at all -- the pipe carried
// none of the serving chain's interceptors, the way it carried no publish
// before #214. And somebody at a shell reading people's rows is who an access
// record is for.
//
// So the pipe records, with the same `Subjects` and `Refs` an RPC's record
// reads, and two things only a shell needs (payday's `access.Config`):
//
//   - `Operator`: who is at the shell, named with `--as`. A row names them
//     where it would have named a frame's caller -- a read's and a write's alike
//     -- and nothing else reads it. A frame would have been the wrong tool: the
//     layers decide what a call may do by the frame, so one naming the operator
//     would hold the deployment's own work to the rules for a caller and refuse
//     half of what the box is for.
//   - `AtOnce`: a read's row is written before its answer leaves, and the read
//     refused when it cannot be. A command is one call in a process that ends
//     with it, so a read queued for the second (`access.Writer`) is a row
//     nothing writes, and data shown that the record never heard of.
//
// # Who: an operator of the control plane, named every time
//
// The same holder the admin console's session names, so a reviewer finds one
// person's reads at the admin console and at the shell under one actor -- the
// operator's identifier, held by the control plane's one tenant.
//
// Named rather than worked out, because nothing at the box can work it out.
// The shell's user is the image's in a container, the same for everybody who
// runs `kubectl exec`; a default operator is one person named for everyone the
// day there are two. What is left is a name the person gives, checked against
// the operators the deployment has: one that names nobody, or somebody whose
// account is stopped, is refused. The shell's user and host go beside it as
// where the call came from -- the place, not the person.
//
// It is a person's word and not a credential, and that is the threat model the
// box already has rather than a gap: whoever holds a shell there holds the
// database and could read it with no command at all (docs/operating.md, "The
// CLI on the box is the deployment"). The record is for the operator who reads
// through the command, and what it guarantees is that such a read is never
// left out of it, nor recorded as nobody's.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lesomnus/z"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// Operator is who is at a shell on the box (#221), as the access record names
// them, and where the shell is.
type Operator struct {
	// Id is the operator, a holder of the control plane, and Tenant is the
	// control plane's one tenant they are held by: what an admin console's
	// session frames the same person as. Both nil where the deployment has no
	// control plane, which has no operators to name.
	Id     pdid.Id
	Tenant pdid.Id

	// Alias is what `--as` said, for a command to say back.
	Alias string

	// Where is the shell: its user and its host, `user@host`, or empty where
	// neither can be told. It is the row's address, beside the operator.
	Where string
}

// ErrUnnamed is a command at the box that reads or writes people's rows and
// was not told who is at the shell. Refused rather than recorded as nobody's,
// which is exactly the read the access record exists not to miss.
var ErrUnnamed = errors.New(
	"--as: who is at this shell. A command here reads the deployment's database directly, " +
		"and what it shows or writes of anybody is on the access record, naming the operator " +
		"who ran it -- name yourself, an operator of this deployment's control plane: " +
		"roster --as <alias> ...")

// OperatorNamed is the operator `--as` names, by alias (`admin` or `@admin`)
// or identifier, on this deployment's control plane: looked up and never made,
// and refused when it names nobody or somebody whose account is stopped.
//
// Never made, unlike [HolderNamed] beside it: a name that matches nobody is a
// typo or a guess, and a row made for it would be an operator nobody is,
// recorded as having read whatever this command read.
func OperatorNamed(ctx context.Context, s *Server, as string) (Operator, error) {
	if s.Control == nil {
		return Operator{}, errors.New(
			"--as: this deployment has no control plane, so it has no operators to name; " +
				"drop --as, and the record names the deployment and this shell")
	}

	as = strings.TrimPrefix(strings.TrimSpace(as), "@")
	if as == "" {
		return Operator{}, ErrUnnamed
	}

	ts, err := s.Control.Ungated.Tenant().List(ctx, app.TenantListRequest_builder{Size: 1}.Build())
	if err != nil {
		return Operator{}, err
	}
	if len(ts.GetItems()) == 0 {
		return Operator{}, fmt.Errorf("--as %s: this control plane has no operators yet; `roster init` makes the first", as)
	}
	tenant, err := pdid.From(ts.GetItems()[0].GetId())
	if err != nil {
		return Operator{}, err
	}

	ref := app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
		Alias:  z.Ptr(as),
		Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
	}.Build()}
	if k, err := pdid.Parse(as); err == nil {
		ref = app.HolderRef_builder{Id: k.Bytes()}
	}

	v, err := s.Control.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref.Build()}.Build())
	if status.Code(err) == codes.NotFound {
		return Operator{}, fmt.Errorf("--as %s: no operator is called that on this deployment's control plane", as)
	}
	if err != nil {
		return Operator{}, fmt.Errorf("--as %s: %w", as, err)
	}
	if v.GetDateDisabled() != nil {
		// Stopped, by `roster control holder disable` or a console: they are
		// not to sign in anywhere, and a shell naming them is somebody else's
		// word for what they did.
		return Operator{}, fmt.Errorf("--as %s: that operator is disabled, and a disabled operator does not act", as)
	}

	who, err := pdid.From(v.GetId())
	if err != nil {
		return Operator{}, err
	}

	return Operator{Id: who, Tenant: tenant, Alias: v.GetAlias()}, nil
}

// Shell is what the access record is told of a call made at a shell on the
// box by `by`, on this plane (#221): an RPC's configuration -- whose data a
// message carries, read off the `personal` declarations -- with the operator
// named where no frame names anybody, the shell for where the call came from,
// and each read written before its answer leaves. See this file's comment.
//
// This plane's, because a row is filed in the database the data is in: the
// control plane's own entity tree records on the control plane.
func (s *Server) Shell(by Operator) access.Config {
	c := s.accessed()
	c.Address = func(context.Context) string { return by.address() }
	c.Operator = func(context.Context) (pdid.Id, pdid.Id) { return by.Id, by.Tenant }
	c.AtOnce = true

	return c
}

// AtTheShell runs `read` as a call of `method` made at the shell by `by`, on
// this plane, recorded as one ([Server.Shell]): whose data `read` says it
// showed (`access.Saw`) is a row naming `by`, written before this answers --
// and refused when it cannot be written, so a command prints what `read`
// gathered only once this has answered without an error.
//
// For what a command shows that no RPC answered: a column of names read past
// the servers, which no declaration can see. `method` is the RPC it stands
// for, which is what a reviewer reads the row by.
func (s *Server) AtTheShell(ctx context.Context, by Operator, method string, read func(ctx context.Context) error) error {
	_, err := s.Shell(by).Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method},
		func(ctx context.Context, _ any) (any, error) { return nil, read(ctx) })

	return err
}

// address is where a call at the shell came from, as a row says it: `shell:`
// and the shell's user and host, which no network address can be mistaken for.
func (o Operator) address() string {
	if o.Where == "" {
		return "shell"
	}

	return "shell:" + o.Where
}

// HearsOnlyItself is whether a watch on this plane hears nothing but what its
// own process publishes: `memory`, which publishes inside the process that made
// it. A broker that crosses processes (`postgres`) hears the deployment, and
// `none` serves no watch at all and says so itself.
//
// Asked by the box (#221), whose process is not the one that serves: under
// `memory` a watch there hears its own command and nothing the deployment does.
func (s *Server) HearsOnlyItself() bool {
	return s.broker == config.BrokerMemory
}
