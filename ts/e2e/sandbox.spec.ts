import { expect, test } from '@playwright/test'

// The sandbox: the console with the whole server compiled into the page, two
// instances of it -- `app.wasm` for the control plane and `admin.wasm` for the
// customers screen. Nothing else in the repository opens it, so this is what
// keeps `npm run dev:sandbox` from quietly stopping being a thing that works.

const base = process.env['E2E_SANDBOX'] ?? 'http://localhost:18100/console/'

test('the sandbox signs in, and its second instance stands a customer up', async ({ page }) => {
	test.setTimeout(120_000)
	await page.goto(base)
	await page.locator('input[name=alias]').fill('ops')
	await page.locator('input[name=password]').fill('sandbox')
	// The instance compiles and seeds on first paint; the form is up before it
	// is, and a click before the entry point is published is queued rather
	// than lost. Give the compile the time it takes.
	await page.locator('button[type=submit]', { hasText: 'sign in' }).click()
	await expect(page.locator('nav .who')).toHaveText('ops', { timeout: 90_000 })

	await page.locator('nav button', { hasText: 'customers' }).click()
	await expect(page.locator('h2', { hasText: 'customers' })).toBeVisible()
	await expect(page.getByRole('cell', { name: 'contoso', exact: true })).toBeVisible({ timeout: 90_000 })

	const form = page.locator('.new-customer form')
	await form.locator('input[name=alias]').fill('fabrikam')
	await form.locator('input[name=who]').fill('admin')
	await form.locator('button[type=submit]').click()
	await expect(page.getByRole('cell', { name: 'fabrikam', exact: true })).toBeVisible()

	await page.locator('tr', { hasText: 'fabrikam' }).locator('button', { hasText: 'people' }).click()
	await expect(page.getByRole('cell', { name: 'admin', exact: true }).first()).toBeVisible()
})
