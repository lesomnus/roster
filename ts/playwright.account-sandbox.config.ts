import { defineConfig } from '@playwright/test'

// A UI preview must stay runnable with no Go, Docker or backend on the desk.
const base = 'http://localhost:18103'
process.env['E2E_ACCOUNT_SANDBOX'] = base

export default defineConfig({
	testDir: 'e2e',
	testMatch: 'account-sandbox.spec.ts',
	workers: 1,
	expect: { timeout: 10_000 },
	use: { browserName: 'chromium', trace: 'retain-on-failure' },
	outputDir: 'dist/account-sandbox-tests',
	webServer: {
		command: 'npm run dev:account:sandbox -- --port 18103 --strictPort',
		url: base,
		reuseExistingServer: false,
	},
})
