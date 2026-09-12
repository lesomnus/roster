import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The account app's page: what `roster account serve --static ts/dist/account`
// serves. It speaks Connect to its own origin and the app hands the calls on
// to roster as the person, so in development it is proxied to a running
// `roster account serve` -- there is no sandbox for it yet.
export default defineConfig({
	root: 'account',
	// Its own optimizer cache, for the reason `vite.console.ts` gives: the
	// default is one directory for all three configs, and `scripts/e2e.sh`
	// runs two dev servers at once.
	cacheDir: '../node_modules/.vite-account',
	publicDir: false,
	build: { outDir: '../dist/account', emptyOutDir: true },
	plugins: [react()],
	server: {
		// Every interface, for `vite.console.ts`'s reason: a container.
		host: true,
		proxy: {
			'/session': 'http://localhost:8090',
			'/providers': 'http://localhost:8090',
			'/login': 'http://localhost:8090',
			'/callback': 'http://localhost:8090',
			'/ways': 'http://localhost:8090',
			'^/roster\\..*': 'http://localhost:8090',
		},
	},
})
