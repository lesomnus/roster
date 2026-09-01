package sso

import (
	_ "embed"
	"net/http"
)

// The screen somebody sees about themselves.
//
// D24 §4: *self-service: my record, add and remove an Sso method, sign out
// everywhere.*
//
// # Why it is one file of Html and not a build
//
// Because the app it belongs to is a Go example, and because D24 §6 puts
// extracting components **last** for a reason it states: *extracting first
// means guessing what to extract, and what 4 and 5 turn out to need is the
// specification.* A page with a build step, a framework and a component library
// would be a guess with three more moving parts, and the thing it is meant to
// specify -- which calls a self-service screen makes, and what each of them
// needs from roster -- is legible here in a way it would not be there.
//
// So: one form, one table, three buttons, and the calls written out. Whoever
// extracts the components has this to read.
//
// # What it shows about the boundary
//
// Every call this page makes goes to **this app**, which then calls roster with
// the delegation. The browser never holds one and never sees roster: it holds a
// cookie for the app in front of it, which is D21's split drawn as a page.
//
// And the three Rpcs behind it take no subject. That is not a coincidence -- a
// screen a person draws about themselves is exactly where a method that took
// one would be a permission over everybody in their tenant.

//go:embed account.html
var account []byte

// Account serves the page.
//
// Its own route rather than `/`, because an app that mounts this example has a
// home page of its own and this is one screen inside it.
func (a *App) Account(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/html; charset=utf-8")

	// No store, because the page it draws is about somebody: a copy in a proxy
	// is a copy of one person's record served to the next.
	w.Header().Set("cache-control", "no-store")

	_, _ = w.Write(account)
}
