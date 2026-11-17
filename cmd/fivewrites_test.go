package cmd_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	app "github.com/lesomnus/roster/rstr"
)

// TestARowAFileDeclaresIsNotRenamed: a holder a file declares is renamed in
// the file, as it is edited and suspended there.
//
// `Update` and `Disable` of a declared holder were refused and `Realias` asked
// nothing: OK, and the row was called something else. What applies the file
// finds its holder by the name the file says, so the next start found nobody
// by it and made a second, while the first kept the role, the binding and the
// label of a row the configuration no longer knew. Right is
// FailedPrecondition, saying where the row is declared.
//
// Signing them out everywhere stays: it edits nothing a file wrote.
func TestARowAFileDeclaresIsNotRenamed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	admin := b.as(ctx, b.ContosoUser, b.Contoso)

	v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		Alias:  "front",
		Labels: map[string]string{cmd.Declared: "config: account"},
	}.Build())
	x.NoError(err)
	ref := app.HolderRef_builder{Id: v.GetId()}.Build()

	_, err = b.Walled.Holder().Realias(admin, app.HolderRealiasRequest_builder{
		Ref: ref, Alias: z.Ptr("door"), DateUpdated: v.GetDateUpdated(),
	}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	x.Contains(status.Convert(err).Message(), "config: account")

	got, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	x.Equal("front", got.GetAlias())

	_, err = b.Walled.Holder().Invalidate(admin, app.HolderInvalidateRequest_builder{Ref: ref}.Build())
	x.NoError(err, "signing a declared holder out everywhere edits nothing of it")

	// What applies the file is what renames one.
	got, err = b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	renamed, err := b.Ungated.Holder().Realias(ctx, app.HolderRealiasRequest_builder{
		Ref: ref, Alias: z.Ptr("door"), DateUpdated: got.GetDateUpdated(),
	}.Build())
	x.NoError(err)
	x.Equal("door", renamed.GetAlias())
}

// TestTheLastAdministratorWhoAsksToLeaveStaysSignedIn: leaving is two writes,
// signed out everywhere and then erased, and a refusal at the second undoes
// the first.
//
// The rule that a tenant keeps somebody administering it is asked where the
// person is erased. Leaving signed them out first, so the last administrator
// who asked to leave was answered FailedPrecondition, *give it to somebody
// else first*, with `date_invalidated` written: refused, and signed out of
// everything, the call's own sign-in included. Right is the refusal and
// nothing else -- they are who has to give it to somebody else.
func TestTheLastAdministratorWhoAsksToLeaveStaysSignedIn(t *testing.T) {
	x := require.New(t)
	b := keyFor(t, verify)
	ctx := t.Context()
	offersLeave(t, ctx, b.Server, b.Contoso, true)

	v, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
			Alias: z.Ptr("admin"), Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	admin := mustId(t, v.GetId())

	c := app.NewMeServiceClient(b.Conn)
	used := signedInAt(t, ctx, b, admin, []string{leaveMe, getMe}, time.Now())
	as := acting(ctx, b.Token, used)

	_, err = c.Leave(as, app.MeLeaveRequest_builder{Confirm: "admin"}.Build())
	x.Equal(codes.FailedPrecondition, status.Code(err), "%v", err)
	x.Contains(status.Convert(err).Message(), "nobody administering")

	erased, invalidated := erasedAt(t, ctx, b.Server, admin)
	x.Nil(erased)
	x.Nil(invalidated, "refused, and signed out everywhere on the way to being refused")

	_, err = c.Get(as, app.MeGetRequest_builder{}.Build())
	x.NoError(err, "the sign-in the refused call came with")

	// Somebody else administers it, and then they leave: both writes, kept.
	role, err := b.Ungated.Role().Get(ctx, app.RoleGetRequest_builder{
		Ref: app.RoleRef_builder{Slug: app.RoleRefBySlug_builder{
			Alias: z.Ptr("everything"), Tenant: app.TenantRef_builder{Id: b.Contoso.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	x.NoError(err)
	_, err = b.Ungated.Binding().Add(ctx, app.BindingAddRequest_builder{
		Role:   app.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: b.Who.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	_, err = c.Leave(as, app.MeLeaveRequest_builder{Confirm: "admin"}.Build())
	x.NoError(err)
	erased, invalidated = erasedAt(t, ctx, b.Server, admin)
	x.NotNil(erased)
	x.NotNil(invalidated, "signed out everywhere, with the erase")
}

// TestNoLinkProvesAnAddressForSomebodyWhoMayNotSignIn: `EmailService/Verify`
// mints no link for somebody suspended, as `Confirm` spends none.
//
// `Confirm` refuses a link whose person is suspended, erased or of a closed
// tenant (#277), and `Verify` asked none of it: it minted the link and
// answered it, a front door mailed it, and the person who clicked it was told
// there was no such link -- until they were enabled, when the same link
// confirmed. Right is the refusal where there is somebody to tell: NotFound,
// which is what `Confirm` says, for the address's own person and for a
// claimant.
func TestNoLinkProvesAnAddressForSomebodyWhoMayNotSignIn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	admin := b.holder(t, ctx, b.Contoso, "admin")
	b.mayAnything(admin, b.Contoso)
	emails := app.NewEmailServiceClient(served(t, b.Server))
	wire := asOverTheWire(ctx, admin)

	who := b.holder(t, ctx, b.Contoso, "stopped")
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()
	row, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{Holder: ref, Address: "stopped@contoso.example"}.Build())
	x.NoError(err)
	verify := func(claimant *app.HolderRef, of []byte) (*app.EmailVerifyResponse, error) {
		return emails.Verify(wire, app.EmailVerifyRequest_builder{Ref: app.EmailRef_builder{Id: of}.Build(), Holder: claimant}.Build())
	}

	_, err = b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: ref}.Build())
	x.NoError(err)

	t.Run("not for their own address", func(t *testing.T) {
		res, err := verify(nil, row.GetId())
		require.Equal(t, codes.NotFound, status.Code(err), "%v", err)
		require.Empty(t, res.GetToken())
	})

	t.Run("nor for one they claim", func(t *testing.T) {
		other := b.holder(t, ctx, b.Contoso, "squatter")
		held, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder: app.HolderRef_builder{Id: other.Bytes()}.Build(), Address: "wanted@contoso.example",
		}.Build())
		require.NoError(t, err)

		res, err := verify(ref, held.GetId())
		require.Equal(t, codes.NotFound, status.Code(err), "%v", err)
		require.Empty(t, res.GetToken())
	})

	t.Run("and once they may sign in again, one is minted and confirms", func(t *testing.T) {
		x := require.New(t)
		_, err := b.Ungated.Holder().Enable(ctx, app.HolderEnableRequest_builder{Ref: ref}.Build())
		x.NoError(err)

		res, err := verify(nil, row.GetId())
		x.NoError(err)
		x.NotEmpty(res.GetToken())

		done, err := emails.Confirm(wire, app.EmailConfirmRequest_builder{Token: res.GetToken()}.Build())
		x.NoError(err)
		x.NotNil(done.GetEmail().GetDateVerified())
	})

	t.Run("nor for somebody erased", func(t *testing.T) {
		gone := b.holder(t, ctx, b.Contoso, "gone")
		theirs, err := b.Ungated.Email().Add(ctx, app.EmailAddRequest_builder{
			Holder: app.HolderRef_builder{Id: gone.Bytes()}.Build(), Address: "gone@contoso.example",
		}.Build())
		require.NoError(t, err)
		_, err = b.Ungated.Holder().Erase(ctx, app.HolderRef_builder{Id: gone.Bytes()}.Build())
		require.NoError(t, err)

		res, err := verify(nil, theirs.GetId())
		require.Equal(t, codes.NotFound, status.Code(err), "%v", err)
		require.Empty(t, res.GetToken())
	})
}

// TestASecondSuspensionKeepsTheFirstMoment: `date_disabled` says since when
// somebody has been suspended, and suspending them again does not move it.
//
// A second `Disable` wrote the moment again, so somebody suspended in March
// and suspended again in June had been suspended since June. The first moment
// stays, as the moment an address was first proved does. What a second
// `Disable` still does is make the suspension the operator's, where it was
// the directory's.
func TestASecondSuspensionKeepsTheFirstMoment(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	who := b.holder(t, ctx, b.Contoso, "twice")
	ref := app.HolderRef_builder{Id: who.Bytes()}.Build()

	one, err := b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	time.Sleep(5 * time.Millisecond)
	two, err := b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	x.True(one.GetDateDisabled().AsTime().Equal(two.GetDateDisabled().AsTime()),
		"suspended at %s, and since %s once suspended again", one.GetDateDisabled().AsTime(), two.GetDateDisabled().AsTime())

	// Lifted and suspended again is a new suspension, and a new moment.
	_, err = b.Ungated.Holder().Enable(ctx, app.HolderEnableRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	time.Sleep(5 * time.Millisecond)
	three, err := b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: ref}.Build())
	x.NoError(err)
	x.True(three.GetDateDisabled().AsTime().After(one.GetDateDisabled().AsTime()))

	// The directory's suspension, made the operator's: the same moment, and no
	// longer the directory's to lift.
	as := b.directory(t, ctx)
	h, err := b.provision(as, "erin", erinOid, "")
	x.NoError(err)
	erin := app.HolderRef_builder{Id: h.GetId()}.Build()
	said, err := b.Walled.Holder().Deactivate(as, app.HolderDeactivateRequest_builder{Ref: erin}.Build())
	x.NoError(err)
	time.Sleep(5 * time.Millisecond)
	_, err = b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: erin}.Build())
	x.NoError(err)

	got, err := b.Ent.Holder.Get(ctx, mustId(t, h.GetId()).Uuid())
	x.NoError(err)
	x.True(said.GetDateDisabled().AsTime().Equal(*got.DateDisabled), "the moment the directory suspended them")
	x.Empty(got.Directory, "and the operator's suspension now")
}

// TestADirectorysWordAndAnOperatorsSuspensionAtOnce: an operator's suspension
// that answered OK stands, whatever the directory says at the same moment.
//
// `Activate` and `Deactivate` read the row, decided, and wrote with no
// version and behind no lock, so what they decided on was a moment old by the
// time they wrote. An operator's `Disable` landing in that moment answered OK
// and was then written over: by `Activate`, lifting a suspension the operator
// had just made -- somebody stopped, signing in again -- and by `Deactivate`,
// calling it the directory's, which the directory lifts the next time it
// says `active`. Seen on a run, with the operator's call beginning within two
// milliseconds after the directory's: 111 of 200 left not suspended, and 119
// of 200 left the directory's.
//
// The directory's read, its decision and its write are one act behind a lock
// on the person's row now, so the operator's write is before it, and seen, or
// after it, and last.
func TestADirectorysWordAndAnOperatorsSuspensionAtOnce(t *testing.T) {
	b, ctx := build(t)
	as := b.directory(t, ctx)

	const people = 60

	// The two calls, the operator's beginning a little after the directory's:
	// the gap is swept, since the moment that matters is between a read and a
	// write a millisecond apart.
	race := func(ref *app.HolderRef, i int, directory func() error) error {
		var wg sync.WaitGroup
		var disabled error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_ = directory()
		}()
		go func() {
			defer wg.Done()
			<-start
			time.Sleep(time.Duration(i%20) * 100 * time.Microsecond)
			_, disabled = b.Ungated.Holder().Disable(ctx, app.HolderDisableRequest_builder{Ref: ref}.Build())
		}()
		close(start)
		wg.Wait()

		return disabled
	}

	t.Run("the directory does not lift it", func(t *testing.T) {
		x := require.New(t)
		lifted := 0
		for i := range people {
			h, err := b.provision(as, fmt.Sprintf("a-%d", i), fmt.Sprintf("00000000-0000-4000-8000-%012d", i), "")
			x.NoError(err)
			ref := app.HolderRef_builder{Id: h.GetId()}.Build()
			_, err = b.Walled.Holder().Deactivate(as, app.HolderDeactivateRequest_builder{Ref: ref}.Build())
			x.NoError(err)

			disabled := race(ref, i, func() error {
				_, err := b.Walled.Holder().Activate(as, app.HolderActivateRequest_builder{Ref: ref}.Build())

				return err
			})
			x.NoError(disabled, "the operator's suspension")

			v, err := b.Ent.Holder.Get(ctx, mustId(t, h.GetId()).Uuid())
			x.NoError(err)
			if v.DateDisabled == nil {
				lifted++
			}
		}
		x.Zero(lifted, "of %d people an operator suspended, answered OK, this many were left not suspended", people)
	})

	t.Run("nor call it its own", func(t *testing.T) {
		x := require.New(t)
		directorys := 0
		for i := range people {
			h, err := b.provision(as, fmt.Sprintf("d-%d", i), fmt.Sprintf("00000000-0000-4000-9000-%012d", i), "")
			x.NoError(err)
			ref := app.HolderRef_builder{Id: h.GetId()}.Build()

			disabled := race(ref, i, func() error {
				_, err := b.Walled.Holder().Deactivate(as, app.HolderDeactivateRequest_builder{Ref: ref}.Build())

				return err
			})
			x.NoError(disabled, "the operator's suspension")

			v, err := b.Ent.Holder.Get(ctx, mustId(t, h.GetId()).Uuid())
			x.NoError(err)
			x.NotNil(v.DateDisabled)
			if v.Directory != "" {
				directorys++
			}
		}
		x.Zero(directorys, "of %d suspensions an operator made, answered OK, this many were left the directory's to lift", people)
	})
}
