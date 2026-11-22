package cli

import (
	"context"

	"github.com/lesomnus/payday/version"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
)

// revision is the commit this binary was built from, when the build said so:
//
//	go build -ldflags "-X github.com/lesomnus/roster/cli.revision=$(git rev-parse HEAD)"
//
// The toolchain stamps `vcs.revision` from the checkout itself, and payday's
// `version` reads that alone. An image is built from a context with no `.git/`
// (`.dockerignore`), so in every image the toolchain had nothing to stamp and
// `version --short` printed an empty line: the commit was the image's label
// and nothing the binary could say. The Dockerfile hands the commit in as
// `APP_REVISION`, beside `APP_VERSION`, and this is where it lands.
var revision string

// stamped has `version` read [revision] where the toolchain recorded none.
//
// The toolchain's word comes first where there is one, since it also says
// whether the tree was clean; the two are one commit wherever both are said.
// What is printed is payday's, the short form and the whole line alike, so an
// image says what a build at a desk from the same commit says.
func stamped(c *xli.Command) {
	c.Handler = xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
		v := version.Get()
		if v.Revision == "" {
			v.Revision = revision
		}

		if short, _ := flg.Find[bool](cmd, "short"); short {
			cmd.Println(v.Short())
			return nil
		}

		cmd.Println(v.String())

		return nil
	})
}
