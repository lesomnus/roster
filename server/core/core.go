// Package core is what roster means that no schema can state.
//
// The schema says an identity is a `(provider, subject)` pair unique across the
// deployment. It cannot say that a subject which looks like an email address is
// almost certainly the wrong value, or that one person having two accounts at
// one provider is a link that went wrong rather than a fact. Those are
// judgements, and this is where they live.
package core

import (
	"entgo.io/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	app "github.com/lesomnus/roster/rstr"
)

// Core is the layer that answers what this app decided.
type Core struct {
	app.Overlay

	// holds is what a caller's bindings allow, which `cmd` reads for the policy
	// and hands over rather than this package asking a second time. See [Holds].
	holds Holds
}

func New(next app.Server, holds Holds) Core { return Core{app.NewOverlay(next), holds} }

// Build makes a builder of this layer so that it can be stacked.
func Build(holds Holds) app.Builder { return builder{holds} }

type builder struct{ holds Holds }

func (b builder) Build(next app.Server) (app.Server, error) { return New(next, b.holds), nil }

var (
	_ app.Server               = Core{}
	_ enttx.Binder[app.Server] = Core{}
)

// WithDriver answers with this stack running on `drv`.
//
// Every layer writes this and none can inherit it: an overlay holds what is
// behind it and has no way to make itself again, so a layer that did not write
// it would be missing from the rebuilt stack and the requests inside the
// transaction would go around it.
func (s Core) WithDriver(drv dialect.Driver) (app.Server, error) {
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	return New(next, s.holds), nil
}
