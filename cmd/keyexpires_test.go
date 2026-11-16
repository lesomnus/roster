package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestAKeyIsNotMintedAlreadyExpired: a key whose expiry is not to come is
// refused, at a terminal and on the wire.
//
// `--expires` is how long a key lasts, and `-1h` -- a sign the wrong way
// round, a variable that came out negative -- was added to now and written. So
// was a `date_expires` in the past, sent to `ApiKeyService/Issue`. Each
// answered a token under *this is the only time it is shown*, and the token
// had already stopped working: refused on its first call as any dead key is,
// with nothing to say that it had never been alive (#276). A delegation was
// always refused one.
func TestAKeyIsNotMintedAlreadyExpired(t *testing.T) {
	t.Run("at a terminal", func(t *testing.T) {
		x := require.New(t)

		c := seedbed(t)
		out, err := initRun(t, c)
		x.NoError(err, "init: %s", out)

		for _, v := range []string{"-1h", "0", "0s"} {
			err := cli.NewCmdControl(&c).Run(t.Context(), []string{"key", "add", "--allow", "/roster.HolderService/List", "--expires=" + v, "loft"})
			x.Error(err, "--expires %s minted a key", v)
			x.ErrorContains(err, "more than nothing")
		}

		s, err := cmd.Build(t.Context(), c)
		x.NoError(err)
		t.Cleanup(func() { s.Close() })

		vs, err := s.Control.Ungated.ApiKey().List(t.Context(), app.ApiKeyListRequest_builder{}.Build())
		x.NoError(err)
		for _, v := range vs.GetItems() {
			x.NotEqual("loft", v.GetAlias())
		}
	})

	t.Run("on the wire", func(t *testing.T) {
		x := require.New(t)

		const issue = "/roster.ApiKeyService/Issue"
		b := keyFor(t, issue)
		ctx := t.Context()
		permits(t, ctx, b, b.Contoso, b.Who, "self", issue)

		own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()
		as := acting(ctx, b.Token, signedInAt(t, ctx, b, b.Who, []string{issue}, time.Now()))
		cl := app.NewApiKeyServiceClient(b.Conn)

		_, err := cl.Issue(as, app.ApiKeyIssueRequest_builder{
			Holder: own, Alias: "dead", Methods: []string{issue},
			DateExpires: timestamppb.New(time.Now().Add(-time.Hour)),
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%v", err)

		v, err := cl.Issue(as, app.ApiKeyIssueRequest_builder{
			Holder: own, Alias: "alive", Methods: []string{issue},
			DateExpires: timestamppb.New(time.Now().Add(time.Hour)),
		}.Build())
		x.NoError(err)
		x.NotEmpty(v.GetToken())
	})
}
