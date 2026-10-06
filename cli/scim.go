package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/cfg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/cmd"
	rstr "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/scim"
	"github.com/lesomnus/roster/server/keys"
)

// NewCmdScim is `roster scim`: roster as a SCIM 2.0 endpoint, for a directory
// that provisions the people it signs in (`docs/scim.md`).
//
// `serve` is the endpoint as a process of its own, for `roster account`'s
// reason; `roster serve` opens the same thing when `scim:` names an address.
// `provision` is the operator's half: a tenant's directory, and the key it
// presents.
func NewCmdScim(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:     "scim",
		Brief:    "roster as a SCIM 2.0 endpoint, where a directory provisions its people",
		Commands: xli.Commands{newCmdScimServe(l, c), newCmdScimProvision(c)},
	}
}

func newCmdScimServe(l *cfg.Loader[cmd.Config], c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "answer SCIM at /scim/v2, as the directory's tenant key",
		// Bound to `scim:`; see `cli/account.go`.
		Flags: flg.Flags{
			cfg.Bind(l, &c.Scim.Addr, &flg.String{Name: "listen", Brief: "where to speak HTTP; :8080 if empty"}),
			cfg.Bind(l, &c.Scim.Roster, &flg.String{Name: "roster", Brief: "roster's data plane, gRPC: host:port"}),
			cfg.Bind(l, &c.Scim.Insecure, &flg.Switch{Name: "insecure", Brief: "dial roster without TLS"}),
		},
		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			ctx, stop, err := telemetry(ctx, c, "roster-scim")
			if err != nil {
				return err
			}
			defer stop()

			// The block, with the flags bound above already in it.
			sc := c.Scim
			if sc.Roster == "" {
				return errors.New("--roster (or scim.roster): where roster speaks gRPC")
			}
			if sc.Addr == "" {
				sc.Addr = ":8080"
			}

			return serveScim(ctx, sc)
		}),
	}
}

// serveScim answers SCIM until ctx is done. Told a [cmd.ScimConfig] and nothing
// else, for the reason `serveAccount` is: two callers build it the same way.
func serveScim(ctx context.Context, sc cmd.ScimConfig) error {
	s, err := scim.New(scim.Config{
		Roster:      sc.Roster,
		Insecure:    sc.Insecure,
		Concurrency: sc.Concurrency,
		Log:         log.From(ctx),
	})
	if err != nil {
		return err
	}
	defer s.Close()

	l, err := net.Listen("tcp", sc.Addr)
	if err != nil {
		return err
	}
	log.From(ctx).InfoContext(ctx, "scim", slog.String("addr", l.Addr().String()))

	srv := &http.Server{Handler: s}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// scimOf is `scim:` with the deployment's own data plane as its roster, for
// `roster serve` running it in the same process.
func scimOf(c *cmd.Config, l net.Listener) cmd.ScimConfig {
	sc := c.Scim
	if sc.Roster == "" {
		sc.Roster = l.Addr().String()
	}

	return sc
}

// ScimHolder is what a tenant's directory is called in it.
const ScimHolder = "scim"

func newCmdScimProvision(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "provision",
		Brief: "let a tenant's directory provision its people through one connection, and mint the key it presents; printed once",
		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
			&flg.String{Name: "connection", Brief: "the connection its people sign in through, by name; the directory links them there"},
		},
		Handler: xli.OnRun(func(ctx context.Context, cl *xli.Command, next xli.Next) error {
			tenant, _ := flg.Find[string](cl, "tenant")
			connection, _ := flg.Find[string](cl, "connection")
			if tenant == "" {
				return errors.New("--tenant: whose directory")
			}
			if connection == "" {
				return errors.New("--connection: which connection its people sign in through")
			}

			s, err := controlled(ctx, c)
			if err != nil {
				return err
			}
			defer s.Close()

			token, err := provisionScim(ctx, s, tenant, connection)
			if err != nil {
				return err
			}

			fmt.Fprintln(os.Stdout, token)
			fmt.Fprintf(os.Stderr, "roster: the key for %s's directory, allowing %d method(s). This is the only time it is shown; "+
				"running this again mints another and the one above stops working.\n", tenant, len(scim.Methods))

			return nil
		}),
	}
}

// provisionScim is [newCmdScimProvision] without the process.
//
// # What it writes
//
// The connection marked as the one the tenant provisions through, unless a
// file declares it -- then the file says so, or the next start would take it
// back. A holder for the directory, a role holding [scim.Methods] exactly, the
// binding, and a tenant key on that holder: replaced, never added to, so
// running it again is a rotation.
//
// A tenant key and not a deployment key: a directory is one tenant's, and a
// key that is one tenant by being minted in it cannot be pointed at another.
func provisionScim(ctx context.Context, s *cmd.Server, tenant, connection string) (string, error) {
	at := rstr.TenantRef_builder{Alias: z.Ptr(tenant)}.Build()
	t, err := s.Ungated.Tenant().Get(ctx, rstr.TenantGetRequest_builder{Ref: at}.Build())
	if err != nil {
		return "", fmt.Errorf("--tenant %s: %w", tenant, err)
	}
	at = rstr.TenantRef_builder{Id: t.GetId()}.Build()

	conn, err := s.Ungated.Connection().Get(ctx, rstr.ConnectionGetRequest_builder{
		Ref: rstr.ConnectionRef_builder{At: rstr.ConnectionRefByAt_builder{
			Tenant: at, Name: z.Ptr(connection),
		}.Build()}.Build(),
		Select: rstr.ConnectionSelect_builder{All: z.Ptr(true)}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		return "", fmt.Errorf("--connection %s: %s has no connection of that name", connection, tenant)
	}
	if err != nil {
		return "", err
	}
	if !conn.GetProvisions() {
		if conn.GetLabels()[cmd.Declared] != "" {
			return "", fmt.Errorf("--connection %s: a file declares it, so the file says it is the one the directory provisions "+
				"through -- `provisions: true` on it -- or the next start takes this back", connection)
		}
		if _, err := s.Ungated.Connection().Patch(ctx, rstr.ConnectionPatchRequest_builder{
			Ref:         rstr.ConnectionRef_builder{Id: conn.GetId()}.Build(),
			Provisions:  z.Ptr(true),
			DateUpdated: conn.GetDateUpdated(),
		}.Build()); err != nil {
			return "", fmt.Errorf("--connection %s: %w", connection, err)
		}
	}

	who, _, err := holderNamed(ctx, s, at, ScimHolder, nil)
	if err != nil {
		return "", err
	}
	role, err := ensureRoleNamed(ctx, s, at, ScimHolder, scim.Methods, nil)
	if err != nil {
		return "", err
	}
	if err := ensureBinding(ctx, s, role, who, nil); err != nil {
		return "", err
	}

	return mintNamed(ctx, s.Ungated, who, ScimHolder, scim.Methods, keys.PrefixTenant)
}
