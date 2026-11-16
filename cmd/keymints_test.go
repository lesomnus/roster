package cmd_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestAKeyThatMintsKeysMintsWhatItsHolderHolds: what a key may put on a key is
// what its **holder** is bound, and the list it was itself minted with is not
// asked.
//
// Decided, and written down as wider than it reads
// (`docs/usage/wide-grants.md`, *A key that mints keys*): a key listing
// `ApiKeyService/Issue` alone mints one, for its own holder, listing anything
// they hold. It is how a terminal is signed in -- the account page's
// delegation lists the methods the page calls and mints the key
// `roster sign-in` asked for (`account`,
// `TestATerminalIsNeverAllowedMoreThanWhoeverApprovesIt`) -- so the list of
// the credential that asks is not the bound (#276).
//
// What is the bound is pinned here, since it is the one a deployment has to
// arrange: a key that has to mint is put on a holder bound only what it should
// hand out.
func TestAKeyThatMintsKeysMintsWhatItsHolderHolds(t *testing.T) {
	const (
		issue = "/roster.ApiKeyService/Issue"
		list  = "/roster.HolderService/List"
		erase = "/roster.HolderService/Erase"
	)

	b := keyFor(t, issue)
	ctx := t.Context()
	x := require.New(t)

	// Somebody bound two methods, and a key of theirs that lists one.
	permits(t, ctx, b, b.Contoso, b.Who, "mints", issue, list)
	own := app.HolderRef_builder{Id: b.Who.Bytes()}.Build()
	narrow := bearing(ctx, mintFor(t, ctx, b, b.Who, "mints-only", []string{issue}, time.Time{}))

	cl := app.NewApiKeyServiceClient(b.Conn)
	holders := app.NewHolderServiceClient(b.Conn)

	// The key calls what its list names of what they hold, and no more.
	_, err := holders.List(narrow, app.HolderListRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)

	// And mints what they hold, which its list does not name.
	v, err := cl.Issue(narrow, app.ApiKeyIssueRequest_builder{
		Holder: own, Alias: "wider-than-the-key-that-made-it", Methods: []string{list},
	}.Build())
	x.NoError(err)

	_, err = holders.List(bearing(ctx, v.GetToken()), app.HolderListRequest_builder{}.Build())
	x.NoError(err)

	// Not what they do not hold: the bound that is asked.
	_, err = cl.Issue(narrow, app.ApiKeyIssueRequest_builder{
		Holder: own, Alias: "wider-than-its-holder", Methods: []string{erase},
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
}
