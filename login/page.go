package login

import (
	"context"
	"encoding/json"
	"net/http"
)

// The page, built rather than served.
//
// `ts/login/` over the same `ts/lib/` the console and the account page are
// over, which is where the sign-in they both draw lives. It was one file of
// plain HTML for a day, on a reason that was about something else: `frontdoor`
// ships its browser half with **no build on purpose**, so that somebody else's
// Go product app can mount one route and write plain markup, and
// `examples/sso/account.html` is that page. roster's own pages were never the
// subject -- and roster already had one drawing this exact flow, with a
// security key and the rule about one attempt per first form, which the plain
// copy did not.
//
// So `login.page.dir` names a build, the way `account.page.dir` and
// `control.console.dir` do. A deployment that serves the page from somewhere
// else leaves it out; what it must keep is the last hop, `POST /accept`,
// because that is the half no other front door has.

// page serves the built page: the two screens at their own paths, and the
// assets under them.
//
// One handler for both, because they are one page -- it reads which screen it
// is from the challenge in its own URL. A file server would answer 404 for
// `/login`, which is a path in the app and not a file in the build.
func (a *App) page() http.Handler {
	if a.c.Page == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "this deployment serves no sign-in page", http.StatusNotFound)
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login", "/consent":
			// A screen, and the build has one document. Rewritten rather than
			// redirected: the challenge is in the query and a redirect that
			// dropped it would be a page with nothing to ask about.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}

		// A form for one flow. Nothing here is cacheable and a stale copy is a
		// browser posting to a challenge that has been spent.
		w.Header().Set("cache-control", "no-store")
		a.c.Page.ServeHTTP(w, r)
	})
}

func writeJson(w http.ResponseWriter, v any) {
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")

	_ = json.NewEncoder(w).Encode(v)
}

// What a request carries between `inFlow` and everything under it: the
// operator whose flow this is, and -- for the outgoing calls -- that
// operator's key.
type (
	keyKey      struct{}
	operatorKey struct{}
)

func withKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, keyKey{}, key)
}

func keyOf(ctx context.Context) (string, bool) {
	k, ok := ctx.Value(keyKey{}).(string)

	return k, ok && k != ""
}

func withOperator(ctx context.Context, o *operator) context.Context {
	return context.WithValue(ctx, operatorKey{}, o)
}

func operatorOf(ctx context.Context) (*operator, bool) {
	o, ok := ctx.Value(operatorKey{}).(*operator)

	return o, ok && o != nil
}
