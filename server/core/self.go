package core

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	app "github.com/lesomnus/roster/rstr"
)

// Self is the rule that a **caller** writes their own password and nobody
// else's.
//
// One method, and a layer of its own rather than a branch in [Core], because
// what makes it work is where it sits. `Vouch.Reset` -- the verb for giving
// somebody a password they did not choose -- is handed the stack from [Core]
// down and never passes through here, so it keeps naming anybody `mayReach`
// allows while a caller cannot. Written as a branch inside `Core` it would have
// closed `Reset` too, which is the door it points at.
//
// That is the general shape and it is worth naming: a rule that must hold for
// one caller and not another is a rule in a layer the other does not enter.
// `CLAUDE.md`, "Layers, and the four cannot's that are not".
//
// # Why a password and not every credential write
//
// Because a password is the one that outlives everything. A second factor is
// `Enrol`, and enrolling one for somebody is what a help desk does; an erase is
// held by the last-way-in count. A password given to somebody by a caller who
// is merely wider than them is a way into that account which does not expire
// and which they are not told about, and the one door for it is `Vouch.Reset`,
// where it is generated rather than chosen.
type Self struct {
	app.Overlay
}

// NewSelf puts the rule in front of next.
func NewSelf(next app.Server) Self { return Self{Overlay: app.NewOverlay(next)} }

// SelfBuild is [NewSelf] as a link in the chain.
func SelfBuild() app.Builder { return selfBuilder{} }

type selfBuilder struct{}

func (selfBuilder) Build(next app.Server) (app.Server, error) { return NewSelf(next), nil }

func (s Self) Credential() app.CredentialServiceServer {
	return selfCredential{s, s.Next().Credential()}
}

// WithDriver is what nothing inherits; see `CLAUDE.md`, "Writing a layer".
func (s Self) WithDriver(drv dialect.Driver) (app.Server, error) {
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	return NewSelf(next), nil
}

type selfCredential struct {
	s Self
	app.CredentialServiceServer
}

func (s selfCredential) Set(ctx context.Context, req *app.CredentialSetRequest) (*app.CredentialSetResponse, error) {
	// No frame is the deployment's own work through a server the wall was never
	// installed on -- `roster init`, `roster vouch set`, the sandbox. There is
	// nobody to refuse, and that door is a line of wiring a reader can find
	// rather than a privilege anybody holds. `mayReach` reads the same way.
	f, ok := frame.From(ctx)
	if !ok || f.Actor.IsZero() {
		return s.CredentialServiceServer.Set(ctx, req)
	}

	who, err := s.s.Next().Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref:    req.GetRef(),
		Select: app.HolderSelect_builder{}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}
	holder, err := pdid.From(who.GetId())
	if err != nil {
		return nil, err
	}
	if holder != f.Actor {
		return nil, status.Error(codes.PermissionDenied,
			"ref: Set writes your own password; somebody else's is Vouch.Reset, which generates one and answers with it once")
	}

	return s.CredentialServiceServer.Set(ctx, req)
}

var (
	_ app.Server               = Self{}
	_ enttx.Binder[app.Server] = Self{}
)
