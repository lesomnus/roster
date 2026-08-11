// Package keys is how a caller proves it may call this deployment.
//
// # Two kinds, and the difference is who the caller turns out to be
//
// A key is a row on a `Holder`, and the same table exists on both planes
// because they are one schema run twice. Which plane a row is in is the whole
// of what separates the two kinds, and [PrefixDeployment] and [PrefixTenant]
// are how a token says which before anything is queried -- there is no query
// from one plane to the other, so somebody has to say.
//
//   - A **deployment** key lives in the control plane: a second roster, in this
//     process, on its own database, whose one tenant is the owner and whose
//     holders are that owner's services. See PLAN.md, D15 and D16. It resolves
//     to the key itself, and the policy hands it every tenant there is.
//   - A **tenant** key lives here, on an ordinary person. It resolves to that
//     person, so nothing about the wall, the bindings or the sites is decided
//     twice -- it is somebody calling, narrowed by the key's own methods.
//
// So this package is small on purpose. Finding a key is a `Get` against a
// generated server, checking one is a hash, and what a key allows is a
// [frame.Grant] built from a column. What it is *not* is a second
// authentication scheme: payday already reads `authorization: Bearer` and
// exchanges it for a name, and this is the store it exchanges against.
//
// [Service] is the same rows over a wire, for a product app that was handed a
// token and cannot read one; see `payday.TokenService`.
//
// # The key itself
//
// Thirty-two bytes from `crypto/rand`, behind a prefix so that a leaked one is
// recognisable -- to a secret scanner, and to whoever finds it in a log they
// should not have written it to.
//
// It is shown once, when it is made. What is stored is a hash, for the reason
// every password store has one, and the deployment cannot tell somebody what
// their key was any more than it can tell them their password.
package keys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// The two prefixes, which are two **kinds** of key and not decoration.
//
// A prefix is not a secret and grants nothing. What it buys is first that a key
// is recognisable -- a secret scanner can be told the pattern, a reviewer seeing
// one in a configuration file knows what they are looking at, and a log line
// carrying one can be found after the fact.
//
// What it buys second is the thing this file turns on: **which database the row
// is in**, known before any query. The two planes are separate databases with
// no query from one to the other, so a token has to say which one to ask, and
// the alternative is asking both and letting whichever answers decide what the
// caller may see.
//
//   - [PrefixDeployment] is the control plane: the operator's own services,
//     minted by `roster key add`. It resolves to the **key**, holds no tenant,
//     and the policy hands it every tenant there is.
//   - [PrefixTenant] is this plane: a key belonging to somebody inside a
//     tenant. It resolves to that **holder**, so the wall, the bindings and the
//     second axis all apply exactly as they do when that person calls, and the
//     key's methods narrow the answer further.
//
// Which is the whole of the difference, and it is why they cannot share a
// prefix: the first is the deployment and the second is a customer, and telling
// them apart by looking at the row would mean already having decided which
// database to look in.
const (
	PrefixDeployment = "rk_"
	PrefixTenant     = "rt_"
)

// Method is what this calls itself in [auth.Identity.Method].
const Method = "api-key"

// Touched is how stale [ApiKey.date_used] may be before a verify writes it.
//
// It is not written every time. This runs on every request a service makes, and
// a write there is a row version, an audit entry and a watch event for a fact
// that changes by the minute. What the field is for is finding the key nobody
// needs any more, and an hour is precise enough for that.
const Touched = time.Hour

// Mint makes a key of the given kind: the one to hand over, and the verifier to
// store.
//
// The kind is a parameter and not a default, because the two are handed to
// different people and one of them is the deployment. A caller that did not say
// which is a caller who has not decided, and defaulting either way makes the
// wrong one silently.
func Mint(prefix string) (string, []byte, error) {
	switch prefix {
	case PrefixDeployment, PrefixTenant:
	default:
		return "", nil, fmt.Errorf("keys: %q is not a kind of key", prefix)
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("keys: %w", err)
	}

	token := prefix + base64.RawURLEncoding.EncodeToString(b)

	return token, Sum(token), nil
}

// Sum is the verifier for a token, and the way its row is found.
//
// SHA-256, unsalted, which is right here and wrong for a password. A key is 256
// bits from `crypto/rand`: there is no dictionary to run against it and no
// rainbow table that covers it, so the work a slow salted hash buys is work
// against an attack that does not exist. What it would cost is real -- every
// call from every service, and a table scan to find the row, since a per-row
// salt cannot be applied before the row is known.
//
// `Credential` is the opposite case in each of those respects, and is salted
// and slow for exactly that reason.
func Sum(token string) []byte {
	v := sha256.Sum256([]byte(token))

	return v[:]
}

// Store answers what a key stands for, over both planes.
//
// `deployment` is the control plane and `tenant` is this one, both with no wall
// on them, for the same reason `cmd.Resolver` reads an unwalled server: working
// out who is calling cannot require knowing who is calling. Either may be nil,
// and then keys of that kind do not exist -- a deployment with no control plane
// has no `rk_`, and one that never issues them to customers may pass no
// `tenant`.
//
// **One store rather than two behind `auth.Seq`**, and that is not tidiness.
// `Seq` moves on only for a handler that found *nothing*, and a token with the
// wrong prefix has been found -- it is a credential that is present and belongs
// to the other plane. A store that answered `ErrNoCredential` so the next one
// could try would also answer it for a token that is simply wrong, and then a
// bad key falls through to whatever was wired after it.
//
// # The two answers
//
// They differ in **who the caller is**, and everything else follows from that.
//
// A deployment key answers with the key itself. The trail then names which key
// asked, revoking is a delete, and no person-row grants anything -- which is
// what a credential belonging to the deployment rather than to anybody in it
// has to mean.
//
// A tenant key answers with the **holder**. So the wall narrows it, the
// bindings decide what it may call, and the second axis applies, all without a
// second copy of any of that: the key is somebody calling, attenuated by its
// methods. Writing it the other way -- the key as the actor -- would mean the
// policy resolving key to holder to bindings on every request, which is the
// permission evaluation written twice.
//
// What that costs is the trail. A tenant key's writes are recorded as the
// person's, so `Audit` says who and not which of their keys. Revoking still
// works, because the row is what the token resolves through; what is lost is
// telling two of somebody's keys apart after the fact. It is the price of the
// grant being an attenuation of a real actor rather than an actor of its own,
// and `frame.Grant` is built for exactly that shape.
func Store(deployment app.Server, tenant app.Server) auth.TokenStore {
	return auth.TokenStoreFunc(func(ctx context.Context, token string) (auth.Identity, error) {
		var (
			s     app.Server
			whose bool // the holder is the caller, rather than the key
		)
		switch {
		case strings.HasPrefix(token, PrefixDeployment):
			s = deployment
		case strings.HasPrefix(token, PrefixTenant):
			s, whose = tenant, true
		}
		if s == nil {
			// Not one of ours, or one of a kind this deployment does not issue.
			// Refused rather than passed over, for the reason above.
			return auth.Identity{}, status.Error(codes.Unauthenticated, "no")
		}

		v, err := lookup(ctx, s, token)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return auth.Identity{}, status.Error(codes.Unauthenticated, "no")
			}

			// The store could not be reached, which is not the caller's fault:
			// told unauthenticated, a service throws away a perfectly good key
			// and its operator goes looking for a problem that is not there.
			return auth.Identity{}, fmt.Errorf("%w: %w", auth.ErrUnavailable, err)
		}

		id := auth.Identity{
			// What it may call, and nothing wider. An empty `methods` is
			// `Grant`'s zero for actions, which allows nothing -- a key
			// somebody forgot to fill in opens no door.
			Grant: frame.Whole().To(v.GetMethods()...),

			// Carried so that a stream ends when the key does; see
			// `auth.InterceptorStream`.
			Expires: expiryOf(v),
		}

		who := v.GetId()
		if whose {
			h := v.GetHolder()
			if h == nil || len(h.GetId()) == 0 {
				// The edge is required and immutable, so this is a row that
				// should not exist. Serving it as the key would hand a customer
				// the scope of a deployment key, which is the one mistake this
				// switch exists to prevent.
				return auth.Identity{}, status.Error(codes.Unauthenticated, "no")
			}

			who = h.GetId()

			// The tenant roster believes holds them, which the resolver reads
			// its own row to disagree with; see `auth.Identity.TenantId`.
			if t := h.GetTenant().GetId(); len(t) > 0 {
				k, err := pdid.From(t)
				if err != nil {
					return auth.Identity{}, err
				}

				id.TenantId = k.String()
			}
		}

		k, err := pdid.From(who)
		if err != nil {
			return auth.Identity{}, err
		}

		id.Id = k.String()

		return id, nil
	})
}

// lookup finds the row a token stands for, or says there is none.
//
// It is separate from [Store] because there are two answers to give about one
// row and only one way to find it. What differs is who the caller is told the
// token stands for -- the key itself here, the holder it hangs off in
// [Service] -- and none of the checking differs at all. Written twice, the
// second copy is the one that forgets the constant-time comparison.
//
// Every refusal is `NotFound`, including a row that was found and did not
// match: expired, revoked and never-existed told apart are an oracle for "this
// string was a real key once".
func lookup(ctx context.Context, s app.Server, token string) (*app.ApiKey, error) {
	v, err := s.ApiKey().Get(ctx, app.ApiKeyGetRequest_builder{
		Ref: app.ApiKeyRef_builder{Secret: Sum(token)}.Build(),
		Select: app.ApiKeySelect_builder{
			Secret:      z.Ptr(true),
			Methods:     z.Ptr(true),
			DateUsed:    z.Ptr(true),
			DateExpires: z.Ptr(true),
			DateUpdated: z.Ptr(true),

			// The holder and its tenant, which [Service] answers with and
			// [Store] does not use. One select rather than two because the
			// edge is a join the query does either way once it is asked for,
			// and a second shape here is a second thing to keep in step.
			Holder: app.HolderSelect_builder{
				Tenant: app.TenantSelect_builder{}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	// Compared again, in constant time, even though the row was found by this
	// exact value. The index did the finding; this is what makes the answer
	// independent of how a mismatched hash sorts, and it costs one comparison
	// of thirty-two bytes.
	if subtle.ConstantTimeCompare(v.GetSecret(), Sum(token)) != 1 {
		return nil, status.Error(codes.NotFound, "no such key")
	}

	if u := v.GetDateExpires(); u != nil && !time.Now().Before(u.AsTime()) {
		return nil, status.Error(codes.NotFound, "no such key")
	}

	touch(ctx, s, v)

	return v, nil
}

// touch records that the key was used, rarely.
//
// Failure is ignored on purpose. A deployment whose control plane refuses a
// write should still be able to serve the request that was authorised -- what
// is lost is a timestamp used to find keys nobody needs.
func touch(ctx context.Context, s app.Server, v *app.ApiKey) {
	if u := v.GetDateUsed(); u != nil && time.Since(u.AsTime()) < Touched {
		return
	}

	_, _ = s.ApiKey().Patch(ctx, app.ApiKeyPatchRequest_builder{
		Ref:         app.ApiKeyRef_builder{Id: v.GetId()}.Build(),
		DateUsed:    timestamppb.Now(),
		DateUpdated: v.GetDateUpdated(),
	}.Build())
}

func expiryOf(v *app.ApiKey) time.Time {
	if u := v.GetDateExpires(); u != nil {
		return u.AsTime()
	}

	return time.Time{}
}
