package account_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/roster/account"
	rstr "github.com/lesomnus/roster/rstr"
)

// TestARecoveryLinkGoesOutOnlyWhereItWouldWork: for each state an address can
// be in, the recovery form mails a link exactly where roster -- asked directly,
// with the tenant's key -- mints one that works, and nowhere it would mint a
// link to nobody. roster cannot say which it minted, so the front door asks
// what roster asks first; this holds the two to one answer.
//
// Red with the front door asking only whether the address is proved, as it
// did, and with it not asking whether the holder is suspended.
func TestARecoveryLinkGoesOutOnlyWhereItWouldWork(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())
	ctx := t.Context()

	conn, err := grpc.NewClient(d.grpc, grpc.WithTransportCredentials(insecure.NewCredentials()))
	x.NoError(err)
	t.Cleanup(func() { conn.Close() })
	vouch := rstr.NewVouchServiceClient(conn)
	asFabrikam := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+d.tokens["fabrikam"])

	// roster's own answer: a link it mints for the address is a row -- a link
	// that works -- or a token that resolves to nobody, and writes nothing.
	works := func(address string) bool {
		before := d.links(t)
		_, err := vouch.Link(asFabrikam, rstr.VouchLinkRequest_builder{
			Who: rstr.VouchWho_builder{Tenant: rstr.VouchTenant_builder{Alias: z.Ptr("fabrikam")}.Build(), Address: address}.Build(),
		}.Build())
		x.NoError(err)

		return d.links(t) > before
	}

	// Somebody at fabrikam with an address of theirs, proved or not.
	person := func(alias, address string, isProved bool) []byte {
		h, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Id: d.fabrikam.Bytes()}.Build(), Alias: alias,
		}.Build())
		x.NoError(err)
		if isProved {
			proved(t, d, h.GetId(), address)
		} else {
			_, err = d.ungated.Email().Add(ctx, rstr.EmailAddRequest_builder{
				Holder: rstr.HolderRef_builder{Id: h.GetId()}.Build(), Address: address,
			}.Build())
			x.NoError(err)
		}

		return h.GetId()
	}
	person("carol", "carol@fabrikam.com", false)
	dave := person("dave", "dave@fabrikam.com", true)
	_, err = d.ungated.Holder().Disable(ctx, rstr.HolderDisableRequest_builder{
		Ref: rstr.HolderRef_builder{Id: dave}.Build(),
	}.Build())
	x.NoError(err)
	erik := person("erik", "erik@fabrikam.com", true)
	_, err = d.ungated.Holder().Erase(ctx, rstr.HolderRef_builder{Id: erik}.Build())
	x.NoError(err)

	b := d.browser(t, "fabrikam.test")
	for _, c := range []struct {
		state   string
		address string
		works   bool
	}{
		{"somebody's, proved", "bob@fabrikam.com", true},
		{"nobody proved it", "carol@fabrikam.com", false},
		{"nobody's", "nobody@fabrikam.com", false},
		{"somebody suspended", "dave@fabrikam.com", false},
		{"somebody erased", "erik@fabrikam.com", false},
	} {
		x.Equal(c.works, works(c.address), "roster does not answer %q as this test says", c.state)

		code, _, body := b.asks(t, "/recover", `{"address":"`+c.address+`"}`)
		x.Equal(http.StatusAccepted, code, body)
		if c.works {
			x.Eventually(func() bool { return d.count(c.address) == 1 }, 5*time.Second, 20*time.Millisecond,
				"no link went where it works: %s", c.state)
		} else {
			x.Never(func() bool { return d.count(c.address) > 0 }, 300*time.Millisecond, 20*time.Millisecond,
				"a link that cannot work was mailed: %s", c.state)
		}
	}
}

// TestATenantWithNoPasswordsRefusesTheRecoveryForm: where the tenant's people
// have no password, the form answers what `/providers` has already said --
// there is no recovery here -- rather than 202 and a link that would hand over
// nothing. It is the tenant's configuration, the same for every address.
//
// Red with the form not asking the tenant.
func TestATenantWithNoPasswordsRefusesTheRecoveryForm(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())
	_, err := d.ungated.Tenant().Patch(t.Context(), rstr.TenantPatchRequest_builder{
		Ref:              rstr.TenantRef_builder{Id: d.fabrikam.Bytes()}.Build(),
		Config:           rstr.TenantConfig_builder{Password: proto.Bool(false)}.Build(),
		DateUpdatedForce: proto.Bool(true),
	}.Build())
	x.NoError(err)

	b := d.browser(t, "fabrikam.test")
	code, body := b.do(t, http.MethodGet, "/providers", "", nil)
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, `"recover":false`)

	before := d.links(t)
	for _, address := range []string{"bob@fabrikam.com", "nobody@fabrikam.com"} {
		code, _, body = b.asks(t, "/recover", `{"address":"`+address+`"}`)
		x.Equal(http.StatusForbidden, code, body)
		x.Contains(body, "none to recover")
	}
	x.Never(func() bool { return d.count("bob@fabrikam.com") > 0 }, 300*time.Millisecond, 20*time.Millisecond,
		"a link went out where it hands over nothing")
	x.Equal(before, d.links(t))
}

// TestARecoveryLinkThatCannotGiveAPasswordSaysSo: somebody who holds a role
// of roster's that this front door does not is not given a password by mail
// -- a front door does not write the credential of somebody who holds more
// than it does -- and the page their link opens says so, and who gives them
// one.
//
// # What it was
//
// The link was mailed and opening it spent it; then `Credential.Issue` was
// refused by the rule about a way in, and the page answered 500, *the link is
// good and the password could not be made; ask an operator*: a fault, where a
// refusal about this account was owed. The link is still spent:
// `Vouch.Redeem` cannot know a password is what it is spent for, and the
// front door cannot ask before it is without a second copy of the rule.
//
// The page says nothing of why: roster refuses this with the code it refuses
// the app's own key not allowed `CredentialService/Issue` with, and tells the
// two apart by no detail, so the reason is the log's.
func TestARecoveryLinkThatCannotGiveAPasswordSaysSo(t *testing.T) {
	x := require.New(t)
	d := serve(t, account.Invited())
	ctx := t.Context()

	// fay, at fabrikam, with a proved address and a role of roster's that
	// the front door's role does not name.
	fay, err := d.ungated.Holder().Add(ctx, rstr.HolderAddRequest_builder{
		Tenant: rstr.TenantRef_builder{Id: d.fabrikam.Bytes()}.Build(), Alias: "fay",
	}.Build())
	x.NoError(err)
	_, err = d.ungated.Credential().Set(ctx, rstr.CredentialSetRequest_builder{
		Holder: rstr.HolderRef_builder{Id: fay.GetId()}.Build(), Secret: []byte("correct horse battery staple"),
	}.Build())
	x.NoError(err)
	proved(t, d, fay.GetId(), "fay@fabrikam.com")
	role, err := d.ungated.Role().Add(ctx, rstr.RoleAddRequest_builder{
		Tenant:  rstr.TenantRef_builder{Id: d.fabrikam.Bytes()}.Build(),
		Alias:   "auditors",
		Methods: []string{rstr.AuditService_List_FullMethodName},
	}.Build())
	x.NoError(err)
	_, err = d.ungated.Binding().Add(ctx, rstr.BindingAddRequest_builder{
		Role:   rstr.RoleRef_builder{Id: role.GetId()}.Build(),
		Holder: rstr.HolderRef_builder{Id: fay.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	// Asking answers as it does for every address, and the link goes out, as
	// it did.
	b := d.browser(t, "fabrikam.test")
	code, _, body := b.asks(t, "/recover", `{"address":"fay@fabrikam.com"}`)
	x.Equal(http.StatusAccepted, code, body)
	var link string
	x.Eventually(func() bool { link = d.sent("fay@fabrikam.com"); return link != "" }, 5*time.Second, 20*time.Millisecond)

	code, page := b.spend(t, link)
	x.Equal(http.StatusForbidden, code, page)
	x.Contains(page, "A new password could not be given through this page")
	x.Contains(page, "Nothing has changed")
	x.Contains(page, "An administrator of your organisation gives you a new password")
	x.Contains(page, "roster vouch reset")

	// Spent, as every link is once its page is answered.
	code, _ = b.spend(t, link)
	x.Equal(http.StatusNotFound, code)

	// And nothing changed: the password is the one she had.
	code, _ = d.browser(t, "fabrikam.test").do(t, http.MethodPost, "/session", `{"alias":"fay","password":"correct horse battery staple"}`,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/json") })
	x.Equal(http.StatusNoContent, code)
}
