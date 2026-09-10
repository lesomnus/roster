package core

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
)

// A row a file declared is a row the file owns.
//
// # Why refusing is kinder than allowing
//
// `cmd/resources.go` applies what a deployment declared, every time it starts.
// So an operator who edits a declared `Connection` in the console has made a
// change that survives until the next restart and then vanishes -- and the
// restart is a config change, an image bump, a node draining, none of which
// look related. They would be left with a setting that was right on Tuesday and
// wrong on Wednesday, and nothing anywhere saying why.
//
// Refused, they find out immediately and the message says where the row is
// declared. That is the whole of it: the edit was already futile, and this is
// the difference between being told and finding out.
//
// # It costs something, and the cost is real
//
// An operator cannot fix a declared row by hand during an outage. A wrong
// issuer is a git round trip -- a commit, a sync, a restart -- when what they
// want is thirty seconds and a text field. That is the price of the row having
// one source of truth, and a deployment that would rather have the text field
// declares fewer things.
//
// # What it does not guard
//
// Erasure. A declared row can still be erased, which then comes back on the
// next start -- because this layer is about *two writers disagreeing*, and an
// erase followed by a re-add is the provisioner winning, which is the answer
// anyway. `Connection.Erase` has its own reasons to be careful and they are
// not this layer's.

// declaredBy is the label a provisioner writes, and its presence is the whole
// test. `cmd.Declared` is the name; it is spelled out here rather than imported
// because `server` may not import `cmd` -- the sandbox's dependency graph is
// the reason, and `cmd/consumers.go` has it.
const declaredBy = "roster.declared"

// mayWriteDeclared refuses a write to a row a file owns, unless the caller is
// the one that owns it.
//
// The provisioner is told apart by its **scope**: it frames itself over every
// tenant, which is what nothing reaching a listener can have. A caller that got
// here from a port was resolved to a tenant and narrowed to it; the provisioner
// runs in-process, before anything is served, and so does a person holding the
// database with `roster connection update` -- which is right, because they have
// the file too.
func (s Core) mayWriteDeclared(ctx context.Context, field string, labels map[string]string) error {
	if _, ok := labels[declaredBy]; !ok {
		return nil
	}
	if f, ok := frame.From(ctx); ok && f.Scope.All() {
		return nil
	}

	return status.Error(codes.FailedPrecondition, fmt.Sprintf(
		"%s: this row is declared in %s and is written from there, not here", field, labels[declaredBy]))
}
