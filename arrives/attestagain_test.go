package arrives

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	rstr "github.com/lesomnus/roster/rstr"
)

// unreadable is a roster whose addresses cannot be read, and which counts the
// attestations it is sent.
type unreadable struct {
	rstr.Client
	attests *int
}

func (r unreadable) Email() rstr.EmailServiceClient { return unreadableEmails{attests: r.attests} }

// unreadableEmails answers `Get` and `Attest`; anything else this package
// calls is a nil method, and a test that reaches one is told so by the panic.
type unreadableEmails struct {
	rstr.EmailServiceClient
	attests *int
}

func (unreadableEmails) Get(context.Context, *rstr.EmailGetRequest, ...grpc.CallOption) (*rstr.Email, error) {
	return nil, status.Error(codes.Unavailable, "roster is away")
}

func (e unreadableEmails) Attest(context.Context, *rstr.EmailAttestRequest, ...grpc.CallOption) (*rstr.Email, error) {
	*e.attests++

	return &rstr.Email{}, nil
}

// TestASignInWhoseAddressCannotBeReadAttestsAsItDid: the read before an
// attestation only saves a call that would write nothing, and decides nothing.
// One that fails is not taken for a row that is already so: the address is
// attested as it was before there was a read.
func TestASignInWhoseAddressCannotBeReadAttestsAsItDid(t *testing.T) {
	n := 0
	p := New(unreadable{attests: &n}, nil)

	p.attestAgain(t.Context(), pdid.New(2), &rstr.Identity{}, Caller{Tenant: pdid.New(1), Email: "dana@contoso.com", Verified: true})
	require.Equal(t, 1, n, "a read that failed was taken for an address already written down")
}
