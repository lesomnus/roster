import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The user console: what `server.http` serves at `/` when `user_console.dir`
// names a build. The third of the UIs this package builds from its own root
// over the same `lib/` and `gen/`; `vite.console.ts` is the admin console's and
// `vite.account.ts` is the account app's.
//
// It is `vite.console.ts` line for line, and the four things it repeats are the
// four that bite whoever serves a sandbox -- each of them fails confusingly,
// and none of them is payday's to fix. Written out rather than shared, because
// a shared base config would be one more indirection between the symptom and
// the sentence that explains it, and the whole value of these comments is that
// they are where somebody lands.
export default defineConfig({
	root: 'user',
	// Its own dependency-optimizer cache, and not the default.
	//
	// The default is `<the nearest package.json>/node_modules/.vite`, which is
	// `ts/` -- one directory for every config. `scripts/e2e.sh` runs several dev
	// servers at once, and a page's modules are invalidated the moment
	// **another** server optimizes a set this one did not: the browser is then
	// answered `504 Outdated Optimize Dep` for modules it already has, and the
	// page never finishes.
	cacheDir: '../node_modules/.vite-user',
	base: '/',
	// The same `public/` as the admin console, which is where `npm run wasm`
	// puts `app.wasm` and `wasm_exec.js`. One module publishes every entry
	// point (`wasm/main.go`), so the two sandboxes are one build and one cache
	// entry, and the page each is comes down to the name it dials.
	publicDir: '../public',
	build: { outDir: '../dist/user', emptyOutDir: true },
	plugins: [react()],

	// The worker `@lesomnus/grpc-dgram` starts is
	// `new URL("./wasm/worker.mjs", import.meta.url)`, and dependency
	// pre-bundling rewrites the module into `.vite/deps/` -- where that relative
	// URL resolves to nothing. The failure is "the worker itself failed", which
	// does not mention bundling.
	optimizeDeps: { exclude: ['@lesomnus/grpc-dgram'] },

	server: {
		// Every interface, not the loopback: this is developed in a container,
		// and a dev server bound to 127.0.0.1 there is one the host's browser
		// cannot reach. `scripts/e2e.sh` still checks and dials 127.0.0.1, which
		// a listener on 0.0.0.0 answers.
		host: true,

		// SQLite in a Worker cancels work with a `SharedArrayBuffer`, which does
		// not exist without cross-origin isolation. The symptom is "it works on
		// the other dev server".
		headers: {
			'Cross-Origin-Opener-Policy': 'same-origin',
			'Cross-Origin-Embedder-Policy': 'require-corp',
		},
	},
})
