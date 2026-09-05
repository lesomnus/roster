import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The console: one of the two UIs this package builds, each from its own root
// over the same `lib/` and `gen/`. `vite.account.ts` is the other.
//
// What it takes to serve the sandbox, which is two things and neither is
// payday's to fix. Both fail confusingly, which is why they are written out
// rather than left to a README.
export default defineConfig({
	root: 'console',
	base: '/console/',
	publicDir: '../public',
	build: { outDir: '../dist/console', emptyOutDir: true },
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
