package account_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/roster/account"
	"github.com/lesomnus/roster/cmd"
	rstr "github.com/lesomnus/roster/rstr"
)

// attestedPerson is somebody of contoso who signs in through `example` as
// `subject`, written the way an administrator writes them: a person, and the
// account at the directory they sign in by. It answers them and that account.
func (d *deployment) attestedPerson(t *testing.T, alias, subject string) (holder, identity []byte) {
	t.Helper()
	ctx := t.Context()

	h, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(), Alias: alias,
	}.Build())
	require.NoError(t, err)
	v, err := d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
		Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(), Provider: "example", Subject: subject,
	}.Build())
	require.NoError(t, err)

	return h.GetId(), v.GetId()
}

// holdsEverything binds somebody to the role that holds every one of roster's
// methods (`cmd.Everyverb`): more than the front door holds, so it may not
// write a way into their account.
func (d *deployment) holdsEverything(t *testing.T, holder []byte) {
	t.Helper()
	ctx := t.Context()

	r, err := d.ungated.Role().Get(ctx, rstr.RoleGetRequest_builder{
		Ref: rstr.RoleRef_builder{Slug: rstr.RoleRefBySlug_builder{
			Alias:  proto.String(cmd.Everyverb),
			Tenant: rstr.TenantRef_builder{Id: d.contoso.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	require.NoError(t, err)
	_, err = d.ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role: rstr.RoleRef_builder{Id: r.GetId()}.Build(), Holder: rstr.HolderRef_builder{Id: holder}.Build(),
	}.Build())
	require.NoError(t, err)
}

// signsInAs is one sign-in at contoso through `connection`, as `subject`, with
// what the directory says in the token. The sign-in itself has to work.
func (d *deployment) signsInAs(t *testing.T, connection, subject string, claims map[string]any) {
	t.Helper()

	d.idp.Subject = subject
	d.idp.Claims = claims
	code, body := d.browser(t, "contoso.test").do(t, http.MethodGet, "/login?connection="+connection, "", nil)
	require.Equal(t, http.StatusOK, code, body)
}

// addressAt is the row contoso holds `address` in, read below every rule, with
// what an attestation could change on it; nil where there is none.
func (d *deployment) addressAt(t *testing.T, address string) *rstr.Email {
	t.Helper()

	v, err := d.ungated.Email().Get(t.Context(), rstr.EmailGetRequest_builder{
		Ref: rstr.EmailRef_builder{At: rstr.EmailRefByAt_builder{
			TenantId: d.contoso.Bytes(), Address: proto.String(address),
		}.Build()}.Build(),
		Select: rstr.EmailSelect_builder{
			DateUpdated:  proto.Bool(true),
			DateVerified: proto.Bool(true),
			Holder:       rstr.HolderSelect_builder{}.Build(),
			VouchedBy:    rstr.IdentitySelect_builder{}.Build(),
		}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return nil
	}
	require.NoError(t, err)

	return v
}

// TestASecondSignInOfSomebodyWhoHoldsMoreSendsNoAttestation: every sign-in of
// an administrator through a directory left a `PermissionDenied` at roster,
// for an address that was already theirs.
//
// The front door attests at every sign-in, and `EmailService/Attest` is held to
// the rule that no front door writes a way into the account of somebody who
// holds more than it does. So the address written down at somebody's first
// sign-in, before they held anything, was refused again at each one after they
// were made an administrator -- a write that would have written nothing,
// refused by a rule that is right. Answered `PermissionDenied` at every sign-in;
// right is no call at all, since the front door reads the row first and there
// is nothing to say. The rule is not loosened for it: an answer before it
// would tell a caller who may call `Attest`, and may not read addresses, whose
// address it is.
func TestASecondSignInOfSomebodyWhoHoldsMoreSendsNoAttestation(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())

	dana, voucher := d.attestedPerson(t, "dana", "4001")
	claims := map[string]any{"email": "Dana@Contoso.com", "email_verified": true}

	// Her first sign-in, holding nothing: the address is written down, proved.
	d.signsInAs(t, "example", "4001", claims)
	first := d.addressAt(t, "dana@contoso.com")
	x.NotNil(first, "the address the directory handed over was not written down")
	x.Equal(dana, first.GetHolder().GetId())
	x.Equal(voucher, first.GetVouchedBy().GetId())
	x.NotNil(first.GetDateVerified())

	d.holdsEverything(t, dana)
	asked := len(d.askedOf(rstr.EmailService_Attest_FullMethodName))

	for _, said := range []map[string]any{
		claims,
		// And a directory that stops saying it checked takes nothing back.
		{"email": "dana@contoso.com"},
	} {
		d.signsInAs(t, "example", "4001", said)
		x.Len(d.askedOf(rstr.EmailService_Attest_FullMethodName), asked,
			"roster was asked to attest an address that is already theirs, and refused it: %v",
			d.askedOf(rstr.EmailService_Attest_FullMethodName))

		again := d.addressAt(t, "dana@contoso.com")
		x.True(proto.Equal(first, again), "the row moved: %v, then %v", first, again)
	}
}

// TestASignInStillAttestsWhatIsNew: reading first saves the call that would
// write nothing, and only that. A first sign-in writes the address with no
// read before it; a directory newly saying it checked an address stamps it; a
// changed address is written; and for somebody who holds more than the front
// door, a changed address is still refused at roster -- the one refusal that
// remains, which is the rule working -- with the sign-in unharmed.
func TestASignInStillAttestsWhatIsNew(t *testing.T) {
	attests := func(d *deployment) []codes.Code { return d.askedOf(rstr.EmailService_Attest_FullMethodName) }

	t.Run("a first sign-in writes, and reads nothing first", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Enrolling())

		gets := len(d.askedOf(rstr.EmailService_Get_FullMethodName))
		d.signsInAs(t, "example", "newcomer-at-the-directory", map[string]any{"email": "newcomer@contoso.com", "email_verified": true})

		x.Equal([]codes.Code{codes.OK}, attests(d))
		// One read of the address, which is the enrolment's own look for an
		// invitation (`arrives.Enrolling`) and not a second one before the
		// attestation: a read that finds nothing is a line at roster.
		x.Len(d.askedOf(rstr.EmailService_Get_FullMethodName), gets+1, "a first sign-in read the address before attesting it")
		v := d.addressAt(t, "newcomer@contoso.com")
		x.NotNil(v, "a first sign-in did not write the address down")
		x.NotNil(v.GetDateVerified())
	})

	t.Run("a directory that newly says it checked is attested", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())

		d.signsInAs(t, "example", "3001", map[string]any{"email": "erin@contoso.com"})
		x.Nil(d.addressAt(t, "erin@contoso.com").GetDateVerified())

		d.signsInAs(t, "example", "3001", map[string]any{"email": "erin@contoso.com", "email_verified": true})
		x.Equal([]codes.Code{codes.OK, codes.OK}, attests(d))
		x.NotNil(d.addressAt(t, "erin@contoso.com").GetDateVerified(), "the directory's word that it checked was not written down")
	})

	t.Run("a changed address is attested", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())

		d.signsInAs(t, "example", "3001", map[string]any{"email": "erin@contoso.com", "email_verified": true})
		d.signsInAs(t, "example", "3001", map[string]any{"email": "erin.new@contoso.com", "email_verified": true})
		x.Equal([]codes.Code{codes.OK, codes.OK}, attests(d))
		x.NotNil(d.addressAt(t, "erin.new@contoso.com"), "the address the directory now says was not written down")
	})

	t.Run("and for somebody who holds more, refused at roster as it is today", func(t *testing.T) {
		x := require.New(t)
		d := serve(t, account.Invited())

		dana, _ := d.attestedPerson(t, "dana", "4001")
		d.signsInAs(t, "example", "4001", map[string]any{"email": "dana@contoso.com", "email_verified": true})
		d.holdsEverything(t, dana)

		d.signsInAs(t, "example", "4001", map[string]any{"email": "dana.new@contoso.com", "email_verified": true})
		x.Equal([]codes.Code{codes.OK, codes.PermissionDenied}, attests(d))
		x.Nil(d.addressAt(t, "dana.new@contoso.com"), "a front door wrote a way into the account of somebody who holds more than it does")
	})
}

// TestTheFrontDoorSkipsAnAttestationOnlyWhereRosterWouldWriteNothing holds the
// front door's reading of `Attest` to `Attest` itself, so that the two cannot
// drift: `arrives` decides, before it calls, whether the call would change
// anything, and the rule is the server's (`server/core/email.go`, `Attest`).
//
// Each case is a state of one address and what a directory says of it at a
// sign-in after the first. It is set up twice, for two people: one is attested
// at the server directly, below every rule about who may, to see whether the
// call writes; the other signs in, to see whether the front door calls. Where
// the front door does not call, the server must write nothing; a case where it
// calls is one roster answers as it always has.
func TestTheFrontDoorSkipsAnAttestationOnlyWhereRosterWouldWriteNothing(t *testing.T) {
	type state struct {
		// mine is whether the row is the signing-in person's own, and absent
		// is that there is no row at all; otherwise it is somebody else's.
		absent, mine bool

		// proved is whether the row is stamped, and by whom it was vouched:
		// "self" the account they sign in by, "other" another account of
		// theirs, "" nobody (an administrator typed it).
		proved   bool
		vouching string

		// verified is what the directory says at this sign-in.
		verified bool
	}
	cases := []struct {
		name         string
		state        state
		calls, write bool
	}{
		{"theirs, unproved, the directory does not say it checked", state{mine: true, vouching: "self"}, false, false},
		{"theirs, proved, the directory does not say it checked", state{mine: true, proved: true, vouching: "self"}, false, false},
		{"theirs, proved, the directory says it checked", state{mine: true, proved: true, vouching: "self", verified: true}, false, false},
		{"theirs, proved, vouched by another account of theirs", state{mine: true, proved: true, vouching: "other", verified: true}, false, false},
		{"theirs, unproved, vouched by another account of theirs", state{mine: true, vouching: "other"}, false, false},
		{"theirs, typed by an administrator, vouched by nobody", state{mine: true}, false, false},
		{"theirs, unproved, the directory newly says it checked", state{mine: true, vouching: "self", verified: true}, true, true},
		{"nobody's yet", state{absent: true}, true, true},
		{"nobody's yet, and checked", state{absent: true, verified: true}, true, true},
		{"somebody else's, unproved, and checked", state{verified: true}, true, true},
		{"somebody else's, unproved", state{}, true, false},
		{"somebody else's, proved, and checked", state{proved: true, verified: true}, true, false},
	}

	x := require.New(t)
	d := serve(t, account.Invited())
	ctx := t.Context()

	// as is one person in one case's state, and the address it is about.
	n := 0
	as := func(s state) (holder, voucher []byte, subject, address string) {
		n++
		subject = fmt.Sprintf("50%02d", n)
		address = fmt.Sprintf("case%d@contoso.com", n)
		holder, voucher = d.attestedPerson(t, fmt.Sprintf("case-%d", n), subject)
		if s.absent {
			return holder, voucher, subject, address
		}

		owner := holder
		if !s.mine {
			owner, _ = d.attestedPerson(t, fmt.Sprintf("case-%d-other", n), subject+"-other")
		}
		add := rstr.EmailAddRequest_builder{Holder: rstr.HolderRef_builder{Id: owner}.Build(), Address: address}
		switch s.vouching {
		case "self":
			add.VouchedBy = rstr.IdentityRef_builder{Id: voucher}.Build()
		case "other":
			v, err := d.ungated.Identity().Add(ctx, rstr.IdentityAddRequest_builder{
				Holder: rstr.HolderRef_builder{Id: owner}.Build(), Provider: "other", Subject: subject + "-elsewhere",
			}.Build())
			x.NoError(err)
			add.VouchedBy = rstr.IdentityRef_builder{Id: v.GetId()}.Build()
		}
		row, err := d.ungated.Email().Add(ctx, add.Build())
		x.NoError(err)
		if s.proved {
			_, err = d.ungated.Email().Patch(ctx, rstr.EmailPatchRequest_builder{
				Ref:          rstr.EmailRef_builder{Id: row.GetId()}.Build(),
				DateVerified: timestamppb.Now(),
				DateUpdated:  row.GetDateUpdated(),
			}.Build())
			x.NoError(err)
		}

		return holder, voucher, subject, address
	}

	for _, c := range cases {
		// At the server: does the call write?
		holder, voucher, _, address := as(c.state)
		before := d.addressAt(t, address)
		_, _ = d.ungated.Email().Attest(ctx, rstr.EmailAttestRequest_builder{
			Holder:    rstr.HolderRef_builder{Id: holder}.Build(),
			Address:   address,
			VouchedBy: rstr.IdentityRef_builder{Id: voucher}.Build(),
			Verified:  c.state.verified,
		}.Build())
		writes := !proto.Equal(before, d.addressAt(t, address))

		// At the front door: does it call?
		_, _, subject, address := as(c.state)
		asked := len(d.askedOf(rstr.EmailService_Attest_FullMethodName))
		claims := map[string]any{"email": address}
		if c.state.verified {
			claims["email_verified"] = true
		}
		d.signsInAs(t, "example", subject, claims)
		calls := len(d.askedOf(rstr.EmailService_Attest_FullMethodName)) > asked

		x.Equal(c.write, writes, "%s: whether the server writes", c.name)
		x.Equal(c.calls, calls, "%s: whether the front door calls", c.name)
		if !calls {
			x.False(writes, "%s: the front door skips a call the server would have written with", c.name)
		}
	}
}
