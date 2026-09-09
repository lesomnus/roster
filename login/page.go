package login

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
)

// The page, served rather than built.
//
// One file, no toolchain, and it imports `frontdoor.js` from this same app --
// which is what `frontdoor.Script` is mounted for. A deployment that wants its
// own serves it with [Config.Page] instead; what it must keep is the last hop,
// `POST /accept`, because that is the half no other front door has.
//
//go:embed login.html
var page []byte

func (a *App) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/html; charset=utf-8")

	// A form for one flow, and the flow is in a cookie. Nothing here is
	// cacheable and a stale copy is a browser posting to a challenge that has
	// been spent.
	w.Header().Set("cache-control", "no-store")

	_, _ = w.Write(page)
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
