package cmd

import (
	"context"
	"fmt"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
	"github.com/lesomnus/roster/server/pd"
)

// Resolver turns a claim into the frame a request is served in.
//
// It is the app's and not payday's, and this is the whole of why: looking
// somebody up is a query against servers generated from this app's schema, and
// payday has no name for them. What payday supplies is the interface and the
// interceptor that uses it -- and the rule that what comes back is a row read
// from the database rather than one the caller described.
//
// It reads through the server the wall was never installed on. Working out who
// is calling happens **before** there is anybody to be walled by, so a resolver
// behind the wall would be asking a question whose answer it needs in order to
// ask it.
func Resolver(s app.Server) auth.Resolver {
	return auth.ResolverFunc(func(ctx context.Context, id auth.Identity) (*frame.Frame, error) {
		if f, ok := keyed(id); ok {
			return f, nil
		}

		ref, err := refOf(id)
		if err != nil {
			return nil, err
		}

		v, err := s.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: ref,
			// The tenant travels with the actor, since almost every rule about
			// a request is about the tenant it is from.
			Select: app.HolderSelect_builder{
				All:    z.Ptr(true),
				Tenant: app.TenantSelect_builder{All: z.Ptr(true)}.Build(),
			}.Build(),
		}.Build())
		if err != nil {
			if status.Code(err) == codes.NotFound {
				// A credential that names nobody who is here is a **bad**
				// credential and not a missing one, which is what
				// `auth.Resolver` asks for -- and the difference is what a
				// client is told to do about it. NotFound is a client that
				// retries the same call and keeps failing; Unauthenticated is
				// one that goes and authenticates again, which is right,
				// because a session held by somebody who has since been erased
				// is exactly this case.
				return nil, fmt.Errorf("%w: %s", auth.ErrNoCredential, err)
			}

			return nil, err
		}

		actor, err := pdid.From(v.GetId())
		if err != nil {
			return nil, err
		}
		tenant, err := pdid.From(v.GetTenant().GetId())
		if err != nil {
			return nil, err
		}

		// The grant is not set here. The interceptor takes it from the
		// credential rather than from whatever a resolver felt like answering,
		// so a resolver cannot widen a token by forgetting.
		return frame.New(actor, tenant, frame.Grant{}).WithRow(v), nil
	})
}

// refOf is the claim as a reference: an identifier when the credential named
// one, and otherwise the pair that names a holder inside a tenant.
func refOf(id auth.Identity) (*app.HolderRef, error) {
	if id.Id != "" {
		k, err := pdid.Parse(id.Id)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", auth.ErrNoCredential, err)
		}

		return app.HolderRef_builder{Id: k.Bytes()}.Build(), nil
	}
	if id.Tenant == "" || id.Alias == "" {
		return nil, fmt.Errorf("names nobody: %w", auth.ErrNoCredential)
	}

	return app.HolderRef_builder{
		Slug: app.HolderRefBySlug_builder{
			Alias:  z.Ptr(id.Alias),
			Tenant: app.TenantRef_builder{Alias: z.Ptr(id.Tenant)}.Build(),
		}.Build(),
	}.Build(), nil
}

// keyed is the frame of a caller that is an **API key** rather than a person,
// and false for one that is not.
//
// # How it can tell before reading anything
//
// A `pdid` carries its domain, so an identifier says what kind of thing it
// names. There is no lookup here and no well-known value compared against: the
// credential named something, and what it named is either a key or it is not.
//
// # What the frame says
//
//   - the **actor is the key**, not the service it hangs off. So the trail
//     names which key asked, revoking it is a delete, and no person-row grants
//     anything -- which is the case `frame.Everything` warns about, where a
//     privilege held by being a particular row can be neither revoked nor
//     narrowed.
//   - **no tenant**, because there is not one. A key belongs to the deployment
//     and the deployment is every tenant in it.
//   - and **no scope**, because a resolver does not decide one. `gate.Decide`
//     overwrites whatever is here with what the policy answers -- see
//     [Policy], which is where a key's `frame.Everything` actually comes from.
//
// Setting it here looked right and was silently discarded: without a policy the
// gate answers `frame.Only(f.Tenant)`, and this frame's tenant is nil, so the
// scope became one tenant that does not exist and every read found nothing.
// `frame.Frame.Scope` says as much -- *worked out by whatever holds the rules
// about who may see whom, which runs after this*.
//
// What narrows a key is the **grant** in any case: the list of methods on its
// row, applied by the interceptor, which a resolver cannot widen.
func keyed(id auth.Identity) (*frame.Frame, bool) {
	if id.Id == "" {
		return nil, false
	}

	k, err := pdid.Parse(id.Id)
	if err != nil || k.Domain() != pd.ApiKeyDomain {
		return nil, false
	}

	// The grant is deliberately not set here. `auth.Interceptor` takes it from
	// the credential rather than from whatever a resolver felt like answering,
	// which is what stops a resolver widening a key by being generous.
	return frame.New(k, pdid.Nil, frame.Grant{}), true
}
