import { expect, test } from '@playwright/test'

// The sandbox: the admin console with the whole server compiled into the page, one
// instance serving two servers -- the control plane, and the admin one the
// tenants screen dials by name. Nothing else in the repository opens it, so this is what
// keeps `npm run dev:sandbox` from quietly stopping being a thing that works.

const base = process.env['E2E_SANDBOX'] ?? 'http://localhost:18100/'

test('the sandbox signs in, and its second server stands a customer up', async ({ page }) => {
	test.setTimeout(120_000)
	await page.goto(base)
	await page.locator('input[name=alias]').fill('admin')
	await page.locator('input[name=password]').fill('admin')
	// The instance compiles and seeds on first paint; the form is up before it
	// is, and a click before the entry point is published is queued rather
	// than lost. Give the compile the time it takes.
	await page.locator('button[type=submit]', { hasText: 'sign in' }).click()
	await expect(page.locator('nav .who')).toHaveText('admin', { timeout: 90_000 })

	await page.locator('nav button', { hasText: 'tenants' }).click()
	await expect(page.locator('h2', { hasText: 'tenants' })).toBeVisible()
	await expect(page.getByRole('cell', { name: 'contoso', exact: true })).toBeVisible({ timeout: 90_000 })

	const form = page.locator('.new-tenant form')
	await form.locator('input[name=alias]').fill('fabrikam')
	await form.locator('input[name=who]').fill('admin')
	await form.locator('button[type=submit]').click()
	await expect(page.getByRole('cell', { name: 'fabrikam', exact: true })).toBeVisible()

	// Opening one lands on the tenant itself, named by the head of the sidebar, and
	// its holders are one click below -- the administrator `Tenant.Add` wrote, read
	// back through the second server this page is running.
	await page.locator('tr', { hasText: 'fabrikam' }).locator('button', { hasText: 'open' }).click()
	await expect(page.locator('nav button.head')).toHaveText('fabrikam')
	await page.locator('nav button', { hasText: 'holders' }).click()
	await expect(page.getByRole('cell', { name: 'admin', exact: true }).first()).toBeVisible()
})
