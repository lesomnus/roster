package frontdoor

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"

	"github.com/lesomnus/roster/server/keys"
)

// Proxy is the browser's road to roster, as the person.
//
// # Why the page does not call roster itself
//
// A page could: roster speaks Connect over HTTP and `ts/gen` has a client for
// every service. What it would need is a credential, and the only one that acts
// as the person is the delegation -- which is a bearer token, and a bearer
// token in a page is a bearer token in every extension, log line and
// screenshot that page ever meets. So the delegation stays in this process,
// beside the session that earned it, and the browser holds the app's own cookie
// and nothing else.
//
// # Why a proxy and not a handler per feature
//
// `examples/sso` wrote one route, one handler and one `fetch` for each thing a
// person could do -- `GET /me`, `POST /me/keys`, `DELETE /me/ways/{id}` -- and
// none of `ts/gen` was any use to it, because the browser was talking to a
// hand-made JSON shape rather than to roster. This is the alternative: the page
// speaks Connect to the app's own origin exactly as the admin console speaks it to
// roster, and this hands the call on with two headers changed. The transport is
// the only thing that differs between the two UIs, which `ts/lib/client.ts`
// says is the whole idea.
//
// # What it checks, in the order it checks
//
// The method must be one the app asked for (`Config.Methods`), refused before
// the hop rather than by roster after it, so the allow-list is enforced in one
// place and a path that is not an RPC never leaves this process. The request
// must carry `Connect-Protocol-Version`, which a cross-site form cannot set and
// `@connectrpc/connect-web` always does -- so a cookie-authenticated proxy is
// not a CSRF sink. And the browser must be signed in, all the way: a half
// session has no delegation and is refused like no session, for
// [Door.Acting]'s reason.
//
// What goes out is the app's own credential -- `bearer`, asked per request,
// because which credential, and which tenant it is narrowed to, is a fact about
// the host the browser arrived at -- and the person's delegation in
// `roster-as`. An app fronting several operators with one deployment key
// answers the key and the tenant to name in `roster-at`; one holding a tenant
// key per tenant answers that tenant's key and no name. What came in as
// `Cookie` and `Authorization` is dropped. What comes back is roster's answer,
// untouched.
func (d *Door) Proxy(roster *url.URL, bearer func(ctx context.Context, host string) (Bearer, error)) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(roster)
			pr.Out.Header.Del("Cookie")
			a := actingOf(pr.In.Context())
			pr.Out.Header.Set("Authorization", "Bearer "+a.bearer.Token)
			pr.Out.Header.Set(keys.HeaderActing, a.token)
			pr.Out.Header.Del(keys.HeaderAt)
			if a.bearer.At != "" {
				pr.Out.Header.Set(keys.HeaderAt, a.bearer.At)
			}
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "no", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Connect-Protocol-Version") == "" {
			// The header is the CSRF token: a form cannot set it, a Connect
			// client cannot leave it out.
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		if !slices.Contains(d.c.Methods, r.URL.Path) {
			// Not something this app draws. Said here rather than by roster's
			// delegation, so an app that asked for less than a page calls is
			// told at the page and not in a log.
			http.Error(w, "no", http.StatusForbidden)
			return
		}

		_, token, ok := d.whole(r.Context(), r)
		if !ok {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}

		b, err := bearer(r.Context(), r.Host)
		if err != nil {
			// The app serves nobody under this name, or cannot say whose key
			// to use: either way there is no roster to hand this to.
			http.Error(w, "no", http.StatusNotFound)
			return
		}

		rp.ServeHTTP(w, r.WithContext(withActing(r.Context(), acting{bearer: b, token: token})))
	})
}

// Bearer is the app's credential for one proxied call: its key, and -- when the
// key is a deployment key -- the tenant it is narrowed to, as `roster-at` says
// one. A delegation minted under a narrowed key is stamped with the holder it
// was narrowed to, so it is recognised on the way back only under the same
// narrowing (`server/keys/acting.go`).
type Bearer struct {
	Token string
	At    string
}

// acting is what one proxied call goes out with: the app's credential for this
// tenant, and the person's delegation.
type acting struct {
	bearer Bearer
	token  string
}

type actingKey struct{}

func withActing(ctx context.Context, v acting) context.Context {
	return context.WithValue(ctx, actingKey{}, v)
}

func actingOf(ctx context.Context) acting {
	v, _ := ctx.Value(actingKey{}).(acting)

	return v
}
