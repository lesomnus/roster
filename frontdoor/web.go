package frontdoor

import (
	_ "embed"
	"net/http"
)

// The browser half, served rather than built.
//
// `web/frontdoor.js` says what it is and why it is one module instead of the
// component library D22 guessed at. This is how a Go app gets it to the page
// without a toolchain: one route, one file, and an ordinary `import` in the
// Html.
//
// An app with a bundler ignores this and imports the file from the source tree
// instead; the `.d.ts` beside it is there for exactly that.

//go:embed web/frontdoor.js
var script []byte

// Script serves the module.
//
// Mounted by the app, at whatever path its pages import -- not by
// [Door.Handler], because that handler is the sign-in **protocol** and this is
// an asset. An app that bundles its own copy should be able to leave this out
// entirely, and it can only do that if it was never mounted for it.
func Script(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/javascript; charset=utf-8")

	// It changes with the binary and not on its own, so it is cacheable -- but
	// the version somebody has is the version the server they are talking to
	// expects, and a long cache outliving a deployment is a page reading a
	// status code the new server no longer sends.
	w.Header().Set("cache-control", "no-cache")

	_, _ = w.Write(script)
}
