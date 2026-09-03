import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The account app's page: what `roster account serve --static ts/dist/account`
// serves. It speaks Connect to its own origin and the app hands the calls on
// to roster as the person, so in development it is proxied to a running
// `roster account serve` -- there is no sandbox for it yet (ts/plan.md, P4).
export default defineConfig({
	root: 'account',
	publicDir: false,
	build: { outDir: '../dist/account', emptyOutDir: true },
	plugins: [react()],
	server: {
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
