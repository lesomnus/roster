package account

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	rstr "github.com/lesomnus/roster/rstr"
)

// TestRedeemableAsksWhatRosterAsks: each question `Vouch.Link` asks before it
// mints a link that works, asked of the row the recovery form reads -- and a
// row that passes them all is one a link goes to.
//
// Red with any one of them not asked: a holder on the row, the address proved,
// the tenant read, the tenant offering passwords, the holder not suspended, the
// tenant not closed.
func TestRedeemableAsksWhatRosterAsks(t *testing.T) {
	now := timestamppb.Now()
	row := func(change func(e *rstr.Email_builder, h *rstr.Holder_builder, tn *rstr.Tenant_builder)) *rstr.Email {
		tn := rstr.Tenant_builder{Id: []byte("tenant-0123456789")}
		h := rstr.Holder_builder{Id: []byte("holder-0123456789")}
		e := rstr.Email_builder{Address: "bob@fabrikam.com", DateVerified: now}
		change(&e, &h, &tn)
		if h.Tenant == nil {
			h.Tenant = tn.Build()
		}
		if e.Holder == nil {
			e.Holder = h.Build()
		}

		return e.Build()
	}
	nothing := func(e *rstr.Email_builder, h *rstr.Holder_builder, tn *rstr.Tenant_builder) {}

	require.True(t, redeemable(row(nothing)), "a link would not go where it works")

	for name, change := range map[string]func(e *rstr.Email_builder, h *rstr.Holder_builder, tn *rstr.Tenant_builder){
		"nobody on the row": func(e *rstr.Email_builder, h *rstr.Holder_builder, tn *rstr.Tenant_builder) {
			h.Id = nil
		},
		"an address nobody proved": func(e *rstr.Email_builder, h *rstr.Holder_builder, tn *rstr.Tenant_builder) {
			e.DateVerified = nil
		},
		"a tenant nobody read": func(e *rstr.Email_builder, h *rstr.Holder_builder, tn *rstr.Tenant_builder) {
			e.Holder = rstr.Holder_builder{Id: h.Id}.Build()
		},
		"a tenant with no passwords": func(e *rstr.Email_builder, h *rstr.Holder_builder, tn *rstr.Tenant_builder) {
			tn.Config = rstr.TenantConfig_builder{Password: proto.Bool(false)}.Build()
		},
		"somebody suspended": func(e *rstr.Email_builder, h *rstr.Holder_builder, tn *rstr.Tenant_builder) {
			h.DateDisabled = now
		},
		"a tenant closed": func(e *rstr.Email_builder, h *rstr.Holder_builder, tn *rstr.Tenant_builder) {
			tn.DateClosed = now
		},
	} {
		require.False(t, redeemable(row(change)), "a link would go to %s", name)
	}
}
