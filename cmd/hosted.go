// Package cmd, this file: which tenant a browser arrived at.
//
// The data plane has many tenants and a sign-in is about one of them. The name
// the request came in on is the only thing that says which, and `Host` rows are
// where a tenant writes down the names it answers at -- the same fact
// `front.WhoseHost` answers for an app in front, read here for a browser that
// has no app in front of it.
package cmd

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/roster/internal/ent"
	enthost "github.com/lesomnus/roster/internal/ent/host"
	"github.com/lesomnus/roster/server/front"
)

// arrivedAt is the metadata a name can be in, in the order a deployment means
// them.
//
// `x-forwarded-host` first, because a deployment that ends TLS in front has the
// public name there and its own service name everywhere else -- the same fact
// `deploy/` learned about a cookie's `Secure` flag, which three places guessed
// from `r.TLS` and got wrong behind a terminator. `host` is what a transcoded
// HTTP request carries, and `:authority` is gRPC's own.
var arrivedAt = []string{"x-forwarded-host", "host", ":authority"}

// Hosted is [console.WithTenant] over `Host` rows: the tenant that claims the
// name this request arrived at, by alias.
//
// A name nothing claims is a **refusal**, and the refusal names it. Carrying on
// without a tenant would look somebody up in whichever one the query happened
// to reach, which is the failure `server/front` refuses in the same words; and
// the name is not a secret, because it is in DNS and in the address bar of the
// browser that just sent it.
func Hosted(db *ent.Client) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return "", status.Error(codes.FailedPrecondition,
				"a sign-in here is about the tenant whose name you arrived at, and this request carries none")
		}

		name := ""
		for _, k := range arrivedAt {
			for _, v := range md.Get(k) {
				if name = front.Hostname(v); name != "" {
					break
				}
			}
			if name != "" {
				break
			}
		}
		if name == "" {
			return "", status.Error(codes.FailedPrecondition,
				"a sign-in here is about the tenant whose name you arrived at, and this request carries none")
		}

		v, err := db.Host.Query().Where(enthost.NameEQ(name)).QueryTenant().First(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return "", status.Errorf(codes.FailedPrecondition,
					"no tenant here answers at %q; a `Host` row is how one says it does", name)
			}

			return "", err
		}

		return v.Alias, nil
	}
}
