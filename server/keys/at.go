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
// `server/front`'s, because a consumer has to be able to write it and may import
// no server package but that one; the reasoning is there.
const HeaderAt = front.HeaderAt

// Nominated is the holder `borrower`'s keys are answered as in the tenant `at`
// chooses -- a name it answers at, or the tenant itself written `@contoso` -- or
// nil where nothing is that and no [app.Nomination] says so.
//
// Two lookups, and each answers a different question. The `Host` says **which
// tenant** the name is; the `Nomination` says **who this app is** there, found
// by the control-plane holder the presented key hangs off. They used to be one
// field on the `Host`, which made a name able to nominate one app -- and the
// Login App and a product arrive at the same name (`nomination.proto`).
//
// Factored out of [At] because [Acting] needs the same answer: a delegation is
// bound to the caller it was issued to, and when a deployment key is narrowed
// the caller **is** this holder -- so a delegation minted under `roster-at` is
// stamped with them and has to be recognised as theirs on the way back. Two
// readers of one fact, and a second copy of this query is a second answer to
// which holder a name borrows.
//
// The tenant comes back on the holder, selected, because both callers need it.
func Nominated(ctx context.Context, tenant app.Server, at string, borrower []byte) (*app.Holder, error) {
	if tenant == nil || at == "" || len(borrower) == 0 {
		return nil, nil
	}

	where, err := tenantAt(ctx, tenant, at)
	if err != nil {
		return nil, err
	}

	n, err := tenant.Nomination().Get(ctx, app.NominationGetRequest_builder{
		Ref: app.NominationRef_builder{
			Borrower: app.NominationRefByBorrower_builder{
				Tenant:     app.TenantRef_builder{Id: where}.Build(),
				BorrowerId: borrower,
			}.Build(),
		}.Build(),
		Select: app.NominationSelect_builder{
			ActsAs: app.HolderSelect_builder{
				Tenant: app.TenantSelect_builder{}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	if status.Code(err) == codes.NotFound {
		// The tenant answers at this name and has not said who this app is
		// there. Nobody, rather than an error: the caller refuses either way,
		// and the two are told apart in its log rather than on the wire.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	h := n.GetActsAs()
	if h == nil || len(h.GetId()) == 0 {
		return nil, nil
	}

	return h, nil
}

// tenantAt is the tenant a [HeaderAt] value chooses: the tenant itself when it
// is written `@alias` or `@<identifier>`, and otherwise the tenant whose `Host`
// row has that name. Either way it chooses and does not consent -- the
// nomination is what decides whether the key acts there.
func tenantAt(ctx context.Context, tenant app.Server, at string) ([]byte, error) {
	if ref, ok := strings.CutPrefix(at, front.TenantMark); ok {
		if ref == "" {
			return nil, status.Error(codes.InvalidArgument, "roster-at: a tenant with no name")
		}

		r := app.TenantRef_builder{Alias: &ref}
		if k, err := pdid.Parse(ref); err == nil {
			r = app.TenantRef_builder{Id: k.Bytes()}
		}

		v, err := tenant.Tenant().Get(ctx, app.TenantGetRequest_builder{
			Ref:    r.Build(),
			Select: app.TenantSelect_builder{}.Build(),
		}.Build())
		if err != nil {
			return nil, err
		}

		return v.GetId(), nil
	}

	v, err := tenant.Host().Get(ctx, app.HostGetRequest_builder{
		Ref:    app.HostRef_builder{Name: &at}.Build(),
		Select: app.HostSelect_builder{Tenant: app.TenantSelect_builder{}.Build()}.Build(),
	}.Build())
	if err != nil {
		return nil, err
	}

	return v.GetTenant().GetId(), nil
}

// ArrivedAt is the name a request declares, normalised, or empty.
//
// One reader was enough while [At] was the only one; [Acting] is the second, and
// a header read in two places is a header spelled differently in one of them.
func ArrivedAt(md metadata.MD) string {
	out := ""
	for _, v := range md.Get(HeaderAt) {
		if v != "" {
			out = front.Hostname(v)
		}
	}

	return out
}

// At resolves a **deployment key** down to the holder a tenant nominated for it.
//
// # What it is for
//
// One Login App instance fronting many tenants holds one `rk_`, which resolves
// to a frame with no tenant: the policy hands it `frame.Everything`, and what
// keeps contoso's request out of fabrikam's rows is the app's own code. That is
// what `docs/login.md` refuses an `rk_` front door for.
//
// The name a request declares (`roster-at`) is a `Host`, which says the tenant;
// a [app.Nomination] in that tenant says who **this key's holder** is there.
// So there is something to narrow **to**: that holder, in that tenant, with
// their bindings and nothing wider.
//
// # Less on one axis, and not on the other
//
// The caller already saw every tenant, so this takes the tenant axis away for
// one request. It does **not** take methods away: the frame is the nominated
// holder's bindings, and the key's own methods are not consulted, so a key
// minted for three reads comes out holding whatever that holder was granted.
// That is why the nomination is found by the key -- a key nobody nominated is
// refused here rather than borrowing whatever the name pointed at, which is
// what `Host.acts_as` allowed (#73) -- and why writing a nomination is held to
// the rule a way into an account is (`server/core`, `mayWriteAWayIn`).
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

		at := ArrivedAt(md)
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
		k, err := findKey(ctx, deployment, token)
		if err != nil || k.Holder == nil {
			return no()
		}

		h, err := Nominated(ctx, tenant, at, k.Holder.GetId())
		if err != nil || h == nil {
			// A name nothing claims, or a tenant that nominated nobody for this
			// key. Refused rather than answered as the key, because answering
			// would hand back the wide frame the caller was trying to narrow --
			// silently.
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
