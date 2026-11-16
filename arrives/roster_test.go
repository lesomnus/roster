package arrives

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/roster/internal/idptest"
	rstr "github.com/lesomnus/roster/rstr"
)

// roster is as much of roster as this package asks, answered from memory: one
// connection, one tenant's profile setting, and what was written about one
// person.
//
// The front doors' own tests ask a real one (`account/`, `login/`), and that
// is where a rule about rows is held. This is for what is decided here before
// roster is asked anything, or whatever it answers: where a provider is asked,
// and what an address nobody checked is used for. Anything else this package
// calls is a nil method, and a test that reaches one is told so by the panic.
type roster struct {
	rstr.Client

	// connection is the one row `Connection.Get` answers with, whoever asks.
	connection *rstr.Connection

	// profile is what the tenant says about filling its people's profiles.
	profile *rstr.TenantProfile

	// added is every `Holder.Add`, and filled what `Holder.Fill` has written:
	// a name and a picture, each once, as roster keeps them.
	added   []*rstr.HolderAddRequest
	name    string
	picture bool
}

func (r *roster) Connection() rstr.ConnectionServiceClient { return connections{r: r} }
func (r *roster) Tenant() rstr.TenantServiceClient         { return tenants{r: r} }
func (r *roster) Holder() rstr.HolderServiceClient         { return holders{r: r} }
func (r *roster) Email() rstr.EmailServiceClient           { return emails{} }

type connections struct {
	rstr.ConnectionServiceClient
	r *roster
}

func (c connections) Get(context.Context, *rstr.ConnectionGetRequest, ...grpc.CallOption) (*rstr.Connection, error) {
	if c.r.connection == nil {
		return nil, status.Error(codes.NotFound, "no such connection")
	}

	return c.r.connection, nil
}

type tenants struct {
	rstr.TenantServiceClient
	r *roster
}

func (c tenants) Get(context.Context, *rstr.TenantGetRequest, ...grpc.CallOption) (*rstr.Tenant, error) {
	return rstr.Tenant_builder{
		Config: rstr.TenantConfig_builder{Profile: c.r.profile}.Build(),
	}.Build(), nil
}

type holders struct {
	rstr.HolderServiceClient
	r *roster
}

func (c holders) Add(_ context.Context, in *rstr.HolderAddRequest, _ ...grpc.CallOption) (*rstr.Holder, error) {
	c.r.added = append(c.r.added, in)

	return rstr.Holder_builder{Id: pdid.New(2).Bytes(), Alias: in.GetAlias()}.Build(), nil
}

// Fill writes a blank and nothing else, and answers what is still blank.
func (c holders) Fill(_ context.Context, in *rstr.HolderFillRequest, _ ...grpc.CallOption) (*rstr.HolderFillResponse, error) {
	if c.r.name == "" {
		c.r.name = in.GetDisplayName()
	}
	if len(in.GetImage()) > 0 || in.GetPicture() != "" {
		c.r.picture = true
	}

	return rstr.HolderFillResponse_builder{Nameless: c.r.name == "", Pictureless: !c.r.picture}.Build(), nil
}

// emails is a tenant nobody was entered into ahead of their arrival.
type emails struct{ rstr.EmailServiceClient }

func (emails) Get(context.Context, *rstr.EmailGetRequest, ...grpc.CallOption) (*rstr.Email, error) {
	return nil, status.Error(codes.NotFound, "no such address")
}

// onThisMachine is a relying party whose deployment said its providers may be
// on this machine, which is where every fake is and where no front door asks a
// provider otherwise (`dial.go`).
func onThisMachine(t *testing.T, c rstr.Client) *Providers {
	t.Helper()

	p := New(c, nil)
	hosts, err := ParsePrivateHosts(idptest.Loopback)
	require.NoError(t, err)
	p.PrivateHosts = hosts

	return p
}
