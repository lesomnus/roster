// Package keys is how a service proves it may call this deployment.
//
// # What a key is
//
// A row in the **control plane** -- a second roster, in this process, on its own
// database, whose one tenant is the owner and whose holders are that owner's
// services. See PLAN.md, D15 and D16.
//
// So this package is small on purpose. Finding a key is a `Get` against a
// generated server, checking one is a hash, and what a key allows is a
// [frame.Grant] built from a column. What it is *not* is a second
// authentication scheme: payday already reads `authorization: Bearer` and
// exchanges it for a name, and this is the store it exchanges against.
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

// Prefix is what every key begins with.
//
// It is not a secret and it is not checked for authority -- what it buys is
// that a key is **recognisable**. A secret scanner can be told this pattern, a
// reviewer seeing it in a configuration file knows what they are looking at,
// and a log line carrying one can be found after the fact.
const Prefix = "rk_"

// Method is what this calls itself in [auth.Identity.Method].
const Method = "api-key"

// Touched is how stale [ApiKey.date_used] may be before a verify writes it.
//
// It is not written every time. This runs on every request a service makes, and
// a write there is a row version, an audit entry and a watch event for a fact
// that changes by the minute. What the field is for is finding the key nobody
// needs any more, and an hour is precise enough for that.
const Touched = time.Hour

// Mint makes a key: the one to hand over, and the verifier to store.
func Mint() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("keys: %w", err)
	}

	token := Prefix + base64.RawURLEncoding.EncodeToString(b)

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

// Store answers what a key stands for.
//
// `s` is the control plane with no wall on it, for the same reason
// `cmd.Resolver` reads an unwalled server: working out who is calling cannot
// require knowing who is calling.
func Store(s app.Server) auth.TokenStore {
	return auth.TokenStoreFunc(func(ctx context.Context, token string) (auth.Identity, error) {
		v, err := lookup(ctx, s, token)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				// No such key, which includes a string that is not one of ours
				// at all. It is still a credential that was presented, so it is
				// refused rather than passed over -- `auth.TokenStore` is
				// explicit that a token which is not known must stop the
				// search.
				return auth.Identity{}, status.Error(codes.Unauthenticated, "no")
			}

			// The control plane could not be reached, which is not the
			// caller's fault: told unauthenticated, a service throws away a
			// perfectly good key and its operator goes looking for a problem
			// that is not there.
			return auth.Identity{}, fmt.Errorf("%w: %w", auth.ErrUnavailable, err)
		}

		k, err := pdid.From(v.GetId())
		if err != nil {
			return auth.Identity{}, err
		}

		return auth.Identity{
			// The **key** is who is calling, not the service it hangs off.
			// So the trail names which key asked, revoking is a delete, and no
			// person-row grants anything.
			Id: k.String(),

			// What it may call, and nothing wider. An empty `methods` is
			// `Grant`'s zero for actions, which allows nothing -- a key
			// somebody forgot to fill in opens no door.
			Grant: frame.Whole().To(v.GetMethods()...),

			// Carried so that a stream ends when the key does; see
			// `auth.InterceptorStream`.
			Expires: expiryOf(v),
		}, nil
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
	if !strings.HasPrefix(token, Prefix) {
		return nil, status.Error(codes.NotFound, "no such key")
	}

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
