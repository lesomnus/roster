package account

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// Caller is who a provider said signed in, before this deployment has decided
// whether that is anybody here.
type Caller struct {
	// Which operator they arrived at, from the host -- the fact this app
	// established and roster cannot read off a token.
	Tenant      pdid.Id
	TenantAlias string

	// Provider is the `Connection.name`, which is what `Identity.provider`
	// stores; Subject is the provider's immutable identifier for them, never an
	// address or a username.
	Provider string
	Subject  string

	// What else the token carried, for a policy that names people by it.
	Email    string
	Verified bool
	Name     string
}

// Enrol decides what happens when somebody signs in at a provider and roster
// has never seen them.
//
// roster cannot answer that: whether a stranger with a valid Google account
// gets an account here, and as whom, is a policy and every deployment has a
// different one. A policy answers with the holder they are; the app links the
// identity to that holder itself, so a policy cannot forget it or write it a
// second way (`server/core/identity.go` has the rules, and they only work if
// everything goes through them). [ErrUninvited] is the answer that says no.
type Enrol func(ctx context.Context, c rstr.Client, who Caller) (pdid.Id, error)

// ErrUninvited is what an [Enrol] answers when this person gets no account.
var ErrUninvited = errors.New("account: nobody here")

// Invited refuses everybody roster has not been told about. The default, and
// the right one for a deployment where people are put in by an operator.
func Invited() Enrol {
	return func(context.Context, rstr.Client, Caller) (pdid.Id, error) {
		return pdid.Nil, ErrUninvited
	}
}

// Enrolling makes an account for anybody the provider vouches for, named by the
// local part of their address. For a deployment where signing in at the
// operator's own directory *is* the invitation -- which is a decision about the
// provider, and one to take knowing the tenant's key has to hold
// `HolderService.Add` for it.
func Enrolling() Enrol {
	return func(ctx context.Context, c rstr.Client, who Caller) (pdid.Id, error) {
		alias, _, ok := strings.Cut(who.Email, "@")
		if !ok || alias == "" {
			return pdid.Nil, fmt.Errorf("enrol %s/%s: no email to name them by", who.Provider, who.Subject)
		}

		v, err := c.Holder().Add(ctx, rstr.HolderAddRequest_builder{
			Tenant: rstr.TenantRef_builder{Alias: proto.String(who.TenantAlias)}.Build(),
			Alias:  alias,
			Name:   who.Name,
		}.Build())
		if err != nil {
			return pdid.Nil, fmt.Errorf("enrol %s: %w", who.Email, err)
		}

		return pdid.From(v.GetId())
	}
}
