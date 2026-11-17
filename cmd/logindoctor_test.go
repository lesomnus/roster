package cmd_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lesomnus/xli/cfg"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	"github.com/lesomnus/roster/cli"
	"github.com/lesomnus/roster/cmd"
	entmigrate "github.com/lesomnus/roster/internal/ent/migrate"
	"github.com/lesomnus/roster/login"
	app "github.com/lesomnus/roster/rstr"
)

// TestDoctorDoesNotFailARunForAProductOfSeveralTenants is `roster login
// doctor` asked about the registration the design is for.
//
// Which tenant a flow is about is read off the redirect it named, and the
// reason given is that one product serving a hundred tenants is one client
// (`login/at.go`, docs/login.md). `doctor` reported exactly that client as
// broken, and a broken finding is the command exiting 1 -- a Job that fails
// for every deployment that did what the manual says. It answered *1 thing(s)
// here cannot sign anybody in*; right is a line that says whose the redirects
// are, and no error.
//
// A client with a flow that names no redirect is the exception, and still
// fails the run: of two tenants' redirects, a device flow is about neither.
func TestDoctorDoesNotFailARunForAProductOfSeveralTenants(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	drv, dsn := pdtest.DB(t)
	cdrv, cdsn := pdtest.DB(t)
	c := cmd.Config{
		Db:      config.DbConfig{Driver: drv, Dsn: dsn},
		Watch:   config.WatchConfig{Broker: config.BrokerMemory},
		Control: cmd.ControlConfig{Db: config.DbConfig{Driver: cdrv, Dsn: cdsn}},
	}

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))
	x.NoError(entmigrate.NewSchema(s.Control.Drv).Create(ctx))
	for _, alias := range []string{"contoso", "fabrikam"} {
		tn, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias}.Build())
		x.NoError(err)
		_, err = s.Ungated.Host().Add(ctx, app.HostAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tn.GetId()}.Build(),
			Name:   alias + ".product.example",
		}.Build())
		x.NoError(err)
	}
	x.NoError(s.Close())

	// As much of Hydra as `doctor` reads: one client, registered at both
	// tenants' names.
	product := map[string]any{
		"client_id":                  "product",
		"client_name":                "one product, two tenants",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"scope":                      "openid offline profile email",
		"redirect_uris":              []string{"https://contoso.product.example/callback", "https://fabrikam.product.example/callback"},
		"post_logout_redirect_uris":  []string{"https://contoso.product.example", "https://fabrikam.product.example"},
		"token_endpoint_auth_method": login.AuthMethod,
	}
	m := http.NewServeMux()
	m.HandleFunc("GET /admin/clients", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{product})
	})
	m.HandleFunc("GET /admin/clients/product", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(product)
	})
	hydra := httptest.NewServer(m)
	t.Cleanup(hydra.Close)

	c.Login.Hydra.Admin = hydra.URL
	doctor := func() error {
		return cli.NewCmdLogin(cfg.New(cmd.Name, &c), &c).Run(ctx, []string{"doctor"})
	}

	x.NoError(doctor(), "a client whose redirects are two tenants' failed the run")

	product["grant_types"] = []string{"authorization_code", "refresh_token", login.DeviceGrant}
	x.ErrorContains(doctor(), "cannot sign anybody in",
		"a client whose device flows are about no one tenant passed the run")
}
