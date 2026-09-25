import { expect, test } from '@playwright/test'

// The user console's sandbox: the same `app.wasm`, dialed under a different
// name.
//
// It is here for the two fakes, which are the only things in the page that a
// real deployment does not have. A message port carries no cookie, so the
// instance remembers who signed in; and it carries no `Host`, so the instance
// writes the name the page would have arrived at and `cmd.Hosted` resolves it
// through the `Host` row the seed puts there. Neither is visible from up here,
// which is the point -- if either had to be visible, the page would have a
// branch that runs nowhere else.
//
// `wasm/sandbox/sandbox_test.go` is the half that runs in `scripts/test.sh`;
// this is the half that proves the entry point is published and the page dials
// it.

const base = process.env['E2E_USER_SANDBOX'] ?? 'http://localhost:18102/'

test('the sandbox signs a roster user in to the tenant the page is at', async ({ page }) => {
	test.setTimeout(120_000)
	await page.goto(base)
	await page.locator('input[name=alias]').fill('admin')
	await page.locator('input[name=password]').fill('admin')
	// The instance compiles and seeds on first paint; the form is up before it
	// is, and a click before the entry point is published is queued rather than
	// lost. Give the compile the time it takes.
	await page.locator('button[type=submit]', { hasText: 'sign in' }).click()
	await expect(page.locator('nav .who')).toHaveText('admin', { timeout: 90_000 })

	// The tenant `seed` stands up, resolved from the name the instance says the
	// page arrived at -- not from anything the page sent.
	await expect(page.locator('nav h1')).toHaveText('contoso', { timeout: 90_000 })

	// The administrator `Tenant.Add` wrote, who is the person just signed in.
	await expect(page.getByRole('cell', { name: 'admin', exact: true })).toBeVisible()

	// And the row that made the sign-in resolvable at all, drawn as a row.
	await page.locator('nav button', { hasText: 'arrives through' }).click()
	await expect(page.getByRole('cell', { name: 'contoso.roster.example', exact: true })).toBeVisible()
})
