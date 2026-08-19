package keys

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/roster/rstr"
)

// HeaderActing is where a delegation travels, and it is deliberately not
// `authorization`.
//
// # Why a delegation is not a bearer credential on its own
//
// D23 and D21 both put the same condition on it -- *bound to the caller it was
// issued to*, so that one product app cannot use what another was given -- and
// the first version of this could not keep it. A credential in `authorization`
// arrives alone: `auth.TokenStore.Lookup` is handed the token and nothing else,
// and the request carries no second thing saying who is presenting it. So the
// binding was checked in `TokenService/Introspect`, where an app asks *about* a
// token, and nowhere on the path where an app *uses* one -- which is the path
// the whole feature is for. Anything that came by the string could spend it.
//
// Here the app goes on authenticating as itself in `authorization`, and this
// header says **who it is acting for**. Both are on the request, so the
// comparison has something to compare, and a delegation that leaks is worth
// nothing without the key it was minted for.
//
// It is also the honest shape of what is happening. A delegation is not a
// second identity for the app; it is an attenuation of the app's own call down
// to one person. The app stays the caller `grpcx.Limit` counts and the
// connection roster trusts; what changes is who the request is about.
const HeaderActing = "roster-as"

// Acting resolves a request that carries a key **and** a delegation.
//
// It answers as the **person** the delegation names, narrowed by the
// delegation's methods -- the same identity a delegation used to produce on its
// own, with the difference that getting one now requires holding the key it was
// minted for.
//
// # Where it sits
//
// First in `auth.Seq`, ahead of `auth.Bearer`. A request with no [HeaderActing]
// is `ErrNoCredential` here, which is what `Seq` moves past; a request that has
// one and is wrong is refused outright, because a credential that is present
// and wrong must stop the search -- the same rule [Store] states about a token
// with an unknown prefix.
//
// # Only a deployment key may present one
//
// The issuer stamped on a delegation is a control-plane key row, because that
// is what `cmd.Resolver` makes the actor of a deployment key. A tenant key or a
// person presenting one has nothing that could match, and refusing them here
// rather than letting the comparison fail is the difference between a rule and
// an accident.
func Acting(deployment app.Server, tenant app.Server) auth.Handler {
	return auth.HandlerFunc(func(ctx context.Context) (auth.Identity, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return auth.Identity{}, auth.ErrNoCredential
		}

		as := ""
		for _, v := range md.Get(HeaderActing) {
			if v != "" {
				as = v
			}
		}
		if as == "" {
			// Not this handler's request. Everything else in the chain still
			// gets its turn.
			return auth.Identity{}, auth.ErrNoCredential
		}

		// Everything below is the same refusal, on purpose. Which half was
		// wrong -- an unknown key, a key that is not a deployment's, a
		// delegation that expired, one that belongs to somebody else -- is
		// exactly what somebody holding a string they found would like to know.
		no := func() (auth.Identity, error) {
			return auth.Identity{}, status.Error(codes.Unauthenticated, "no")
		}

		if deployment == nil || tenant == nil {
			return no()
		}

		token := ""
		for _, v := range md.Get(auth.Header) {
			if rest, ok := strings.CutPrefix(v, auth.BearerScheme+" "); ok && rest != "" {
				token = rest
			}
		}
		if token == "" || !strings.HasPrefix(token, PrefixDeployment) {
			return no()
		}
		if !strings.HasPrefix(as, PrefixDelegation) {
			return no()
		}

		k, err := findKey(ctx, deployment, token)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return no()
			}

			return auth.Identity{}, err
		}

		who, err := pdid.From(k.Id)
		if err != nil {
			return auth.Identity{}, err
		}

		v, err := findDelegation(ctx, tenant, as)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return no()
			}

			return auth.Identity{}, err
		}
		if err := issued(v, who); err != nil {
			return no()
		}

		h := v.Holder
		if h == nil || len(h.GetId()) == 0 {
			// The edge is required and immutable, so this is a row that should
			// not exist. Answering as nobody would be worse than answering
			// with nothing.
			return no()
		}

		id := auth.Identity{
			// The person, and only what the delegation was minted for. An
			// empty list allows nothing, which is `frame.Grant`'s zero.
			Grant: frame.Whole().To(v.Methods...),

			// Always set on a delegation -- [findDelegation] refuses a row
			// without one -- so a stream carrying this ends when it does.
			Expires: v.Expires,
		}

		p, err := pdid.From(h.GetId())
		if err != nil {
			return auth.Identity{}, err
		}
		id.Id = p.String()

		if t := h.GetTenant().GetId(); len(t) > 0 {
			q, err := pdid.From(t)
			if err != nil {
				return auth.Identity{}, err
			}

			id.TenantId = q.String()
		}

		return id, nil
	})
}
