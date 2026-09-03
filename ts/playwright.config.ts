import { defineConfig } from '@playwright/test'

// `scripts/e2e.sh` stands the deployment up and says where it is; the specs
// read the same variables. Run through the script rather than `npx playwright
// test` alone, which has nothing to talk to.
export default defineConfig({
	testDir: 'e2e',
	// The account spec walks one person through changing what signs them in,
	// and the steps are in order on purpose.
	fullyParallel: false,
	workers: 1,
	retries: 0,
	reporter: 'list',
	// A password compare is slow on purpose, and the run before this one is a
	// build: the first sign-in on a busy machine has taken four seconds.
	expect: { timeout: 10_000 },
	use: {
		browserName: 'chromium',
		trace: 'retain-on-failure',
	},
	outputDir: 'dist/e2e',
})
