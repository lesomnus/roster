package login

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"html/template"
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

func (a *App) page(w http.ResponseWriter, r *http.Request) { form(w) }

// form is the sign-in page, which the sandbox serves unchanged.
func form(w http.ResponseWriter) {
	w.Header().Set("content-type", "text/html; charset=utf-8")

	// A form for one flow, and the flow is in a cookie. Nothing here is
	// cacheable and a stale copy is a browser posting to a challenge that has
	// been spent.
	w.Header().Set("cache-control", "no-store")

	_, _ = w.Write(page)
}

// The consent screen, drawn only under [Ask].
//
// A template where the sign-in page is not, because this one says something
// about **this** flow -- which app is asking, and for what -- and a page that
// read that from its own URL would be a page a link could put words in.
//
//go:embed consent.html
var consentPage string

var consentTemplate = template.Must(template.New("consent").Parse(consentPage))

func (a *App) ask(w http.ResponseWriter, r *http.Request, v *consentRequest) {
	if err := ask(w, v); err != nil {
		a.broken(w, r, err)
	}
}

// ask draws the consent screen.
//
// A function rather than only a method, because the sandbox draws the **same**
// screen with nothing behind it -- a page that is real and a server that is
// not, which is what makes looking at it worth anything.
func ask(w http.ResponseWriter, v *consentRequest) error {
	name := v.Client.Name
	if name == "" {
		name = v.Client.Id
	}

	// Rendered to a buffer first: a template that fails half way has already
	// written half a page, and the answer to a broken deployment is one line
	// rather than a form with no buttons.
	b := &bytes.Buffer{}
	if err := consentTemplate.Execute(b, struct {
		Client    string
		Scope     []string
		Challenge string
	}{Client: name, Scope: v.Scope, Challenge: v.Challenge}); err != nil {
		return err
	}

	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	_, _ = w.Write(b.Bytes())

	return nil
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
