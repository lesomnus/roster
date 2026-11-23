package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/roster/rstr"
)

// TestADirectorySaysNothingOfItself: `HolderService.Tell` of the caller's own
// row is refused, as `Deactivate` of it is.
//
// A directory says what it calls the people it speaks of, and it is not one of
// them. Its key could say an id for its own holder (answered OK; right is
// `PermissionDenied`), and `roster scim adopt` then listed that holder, with
// no address, and linked it -- after which `roster scim provision` refused to
// rotate the key of a holder that now had an account. The rule is about
// whoever holds the verb and not about the endpoint's key: an administrator
// telling of themselves is refused the same.
func TestADirectorySaysNothingOfItself(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	as := b.directory(t, ctx)
	self := app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "directory").Bytes()}.Build()

	tell := func(as context.Context, ref *app.HolderRef, subject string) error {
		_, err := b.Walled.Holder().Tell(as, app.HolderTellRequest_builder{Ref: ref, Entry: app.DirectoryEntry_builder{Subject: z.Ptr(subject)}.Build()}.Build())

		return err
	}

	err := tell(as, self, fredOid)
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
	v, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: self, Select: app.HolderSelect_builder{DirectorySubject: z.Ptr(true)}.Build(),
	}.Build())
	x.NoError(err)
	x.Empty(v.GetDirectorySubject(), "nothing is kept beside it")

	// Of somebody else, as before.
	fred := app.HolderRef_builder{Id: b.holder(t, ctx, b.Contoso, "fred").Bytes()}.Build()
	x.NoError(tell(as, fred, fredOid))

	// Whoever holds the verb, of themselves.
	admin := b.as(ctx, b.ContosoUser, b.Contoso)
	err = tell(admin, app.HolderRef_builder{Id: b.ContosoUser.Bytes()}.Build(), erinOid)
	x.Equal(codes.PermissionDenied, status.Code(err), "%v", err)
}
