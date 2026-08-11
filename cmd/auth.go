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
// `keys` is the control plane, or nil for a deployment that has none. It is
// what makes an api-key identifier a **row that exists** rather than a shape a
// caller can write down; see [keyed].
func Resolver(s app.Server, keys app.Server) auth.Resolver {
	return auth.ResolverFunc(func(ctx context.Context, id auth.Identity) (*frame.Frame, error) {
		f, ok, err := keyed(ctx, keys, id)
		if err != nil {
			return nil, err
		}
		if ok {
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
// # It reads the row, and it used to not
//
// A `pdid` carries its domain, so an identifier says what kind of thing it
// names -- and for a while that was the whole of the test. It was wrong, and
// the way it was wrong is worth leaving written down.
//
// What the policy does with a key is hand it `frame.Everything`: a key belongs
// to the deployment and the deployment is every tenant in it. So "this is a
// key" was a claim worth more than any other a credential could make, and it
// was the one claim nothing checked -- an identifier is sixteen bytes, its
// domain is one of them, and under `auth.Plain` a caller writes their own. A
// forged one named no row in any database, was minted by nobody, could be
// revoked by nobody, and read every tenant. Found by writing it; nothing
// refused, nothing logged.
//
// It is the case `ApiKey`'s own comment warns about -- *a privilege held by
// being a particular row can be neither revoked nor narrowed* -- one step
// worse, because there was no row.
//
// So a key is a row that exists or it is not a key. `keys` is the control
// plane, and a deployment without one has no keys at all: there is nothing an
// api-key identifier could name, so it names nobody rather than naming the
// deployment.
//
// # What it costs
//
// A second read of a row `keys.Store` looked up moments earlier, on the key
// path only. It is paid rather than avoided because the alternatives are worse:
// a field on `auth.Identity` saying "the store vouched for this" is a field
// `auth.Plain` can also fill in, and there is no channel between a handler and
// a resolver that a caller cannot write to. Reading the row is the only answer
// that does not depend on which handler happened to be wired up.
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
func keyed(ctx context.Context, keys app.Server, id auth.Identity) (*frame.Frame, bool, error) {
	if id.Id == "" {
		return nil, false, nil
	}

	k, err := pdid.Parse(id.Id)
	if err != nil || k.Domain() != pd.ApiKeyDomain {
		return nil, false, nil
	}

	if keys == nil {
		// No control plane, so no keys. Refused rather than passed over: the
		// caller named something, and letting it fall through would resolve an
		// api-key identifier against the holders, where it finds nobody and
		// says so in a way that reads as a missing person.
		return nil, false, fmt.Errorf("%w: this deployment has no keys", auth.ErrNoCredential)
	}

	if _, err := keys.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
		Ref: app.ApiKeyRef_builder{Id: k.Bytes()}.Build(),

		// Nothing is read off it. The question is whether the row is there --
		// the secret was already checked by whatever produced this identity,
		// and checking it again here would need the token, which a resolver is
		// deliberately never given.
		Select: app.ApiKeySelect_builder{}.Build(),
	}.Build()); err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, false, fmt.Errorf("%w: no such key", auth.ErrNoCredential)
		}

		return nil, false, err
	}

	// The grant is deliberately not set here. `auth.Interceptor` takes it from
	// the credential rather than from whatever a resolver felt like answering,
	// which is what stops a resolver widening a key by being generous.
	return frame.New(k, pdid.Nil, frame.Grant{}), true, nil
}
