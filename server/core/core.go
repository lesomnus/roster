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

	// rules is what a caller holds, which `cmd` reads for `gate.Policy` and
	// hands over rather than this package asking a second time. See [Rules].
	rules Rules
}

// Rules is what this layer has to know about a caller and cannot work out.
//
// Both answers come from the same rows `gate.Policy` reads -- bindings, group
// memberships, team memberships -- and `cmd` reads them against ent, because
// working out what somebody may do cannot itself require permission. Asking
// again here would be a second implementation of one question, and two
// implementations of one question drift.
//
// A zero value refuses everything a frame carries, which is the safe direction
// for a stack somebody assembled without it.
type Rules struct {
	// Holds answers whether somebody may call a method, for a team. See [Holds].
	Holds Holds

	// Granted is every method somebody holds **through a binding**, which is
	// what they may pass on. See [Granted].
	Granted Granted

	// Joining is what a group holds, which is what putting somebody into it
	// hands them. See [Joining].
	Joining Joining
}

func New(next app.Server, rules Rules) Core { return Core{app.NewOverlay(next), rules} }

// Build makes a builder of this layer so that it can be stacked.
func Build(rules Rules) app.Builder { return builder{rules} }

type builder struct{ rules Rules }

func (b builder) Build(next app.Server) (app.Server, error) { return New(next, b.rules), nil }

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

	return New(next, s.rules), nil
}
