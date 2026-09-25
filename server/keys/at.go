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
	"github.com/lesomnus/roster/server/front"
)

// HeaderAt is the name a request says it arrived at.
//
// # Said rather than sniffed
//
// A browser's `Host` is a fact about one hop, and a request that reached roster
// through a proxy, a mesh or a second service has whatever that hop wrote.
// A header the caller sets is a **declaration**: it says which tenant this call
// is about, and roster answers with a refusal when nothing claims that name
// rather than carrying on in whichever tenant it happened to reach.
//
// It is the rule `payday#15` settled one layer up, about routing: route on what
// the request declares, and refuse a request that declares nothing rather than
// falling through to a default. The failure it avoids is the same one -- a call
// that lands somewhere plausible and answers with somebody else's rows.
const HeaderAt = "roster-at"

// At resolves a **deployment key** down to the holder a `Host` nominates.
//
// # What it is for
//
// One Login App instance fronting many tenants holds one `rk_`, which resolves
// to a frame with no tenant: the policy hands it `frame.Everything`, and what
// keeps contoso's request out of fabrikam's rows is the app's own code. That is
// what `docs/login.md` refuses an `rk_` front door for.
//
// A `Host` names the holder its tenant nominated (`Host.acts_as`), so there is
// something to narrow **to**: a request carrying `roster-at` is answered as
// that holder, in that tenant, with their bindings and nothing wider.
//
// # It grants nothing, and that is why it needs no rule
//
// The caller already saw every tenant. What this does is take that away for one
// request, so there is no escalation to refuse and nothing for `mayGrant` to
// compare: a nomination is a tenant choosing how narrow the app in front of
// them is, not a permission it is given.
//
// # Where it sits
//
// Beside [Acting] and ahead of [auth.Bearer], for the reason `Seq` orders those
// two: a request with no [HeaderAt] is `ErrNoCredential` here and moves on, and
// one that has it and is wrong is refused outright rather than falling through
// to be answered as the key itself -- which would be the wide frame this exists
// to replace.
//
// # Only a deployment key
//
// An `rt_` resolves to a holder in a tenant already, so there is nothing to
// narrow and a request carrying both is a caller asking to be somebody else.
// Refused, in the same words as everything else here.
func At(deployment app.Server, tenant app.Server) auth.Handler {
	return auth.HandlerFunc(func(ctx context.Context) (auth.Identity, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return auth.Identity{}, auth.ErrNoCredential
		}

		at := ""
		for _, v := range md.Get(HeaderAt) {
			if v != "" {
				at = front.Hostname(v)
			}
		}
		if at == "" {
			return auth.Identity{}, auth.ErrNoCredential
		}

		// One refusal for every way this can be wrong -- an unknown key, a
		// tenant key, a name nothing claims, a name whose tenant nominated
		// nobody. Which one it was is what somebody probing would like to know,
		// and the deployment's own app is told by its logs rather than by this.
		no := func() (auth.Identity, error) {
			return auth.Identity{}, status.Error(codes.Unauthenticated, "no")
		}

		if tenant == nil || deployment == nil {
			return no()
		}

		token := ""
		for _, v := range md.Get(auth.Header) {
			if rest, ok := strings.CutPrefix(v, auth.BearerScheme+" "); ok && rest != "" {
				token = rest
			}
		}
		if !strings.HasPrefix(token, PrefixDeployment) {
			return no()
		}
		if _, err := findKey(ctx, deployment, token); err != nil {
			return no()
		}

		v, err := tenant.Host().Get(ctx, app.HostGetRequest_builder{
			Ref: app.HostRef_builder{Name: &at}.Build(),
			Select: app.HostSelect_builder{
				ActsAs: app.HolderSelect_builder{
					Tenant: app.TenantSelect_builder{}.Build(),
				}.Build(),
			}.Build(),
		}.Build())
		if err != nil {
			return no()
		}

		h := v.GetActsAs()
		if h == nil || len(h.GetId()) == 0 {
			// A name that is here and nominates nobody. Refused rather than
			// answered as the key, because answering would hand back the wide
			// frame the caller was trying to narrow -- silently.
			return no()
		}

		who, err := pdid.From(h.GetId())
		if err != nil {
			return auth.Identity{}, err
		}

		id := auth.Identity{
			// Whatever that holder may do, which their bindings decide on every
			// call. Narrowing here would be a second answer to a question the
			// policy already answers.
			Grant: frame.Whole(),
			Id:    who.String(),
		}

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
