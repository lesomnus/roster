import { expect, test } from '@playwright/test'

// The admin console, as an operator uses it: sign in with the password `roster init`
// took, stand a tenant up from the tenants screen -- which is the page
// reaching the admin listener from another origin, with the session cookie
// the control listener set -- and see their first person.

const base = process.env['E2E_CONSOLE'] ?? 'http://127.0.0.1:18062/'
const operator = process.env['E2E_OPS_USER'] ?? 'admin'
const password = process.env['E2E_OPS_PASSWORD'] ?? ''

test('an operator signs in and stands a customer up', async ({ page }) => {
	await page.goto(base)
	await page.locator('input[name=alias]').fill(operator)
	await page.locator('input[name=password]').fill(password)
	await page.locator('button[type=submit]', { hasText: 'sign in' }).click()
	await expect(page.locator('nav .who')).toHaveText(operator)

	await page.locator('nav button', { hasText: 'tenants' }).click()
	await expect(page.locator('h2', { hasText: 'tenants' })).toBeVisible()
	await expect(page.getByRole('cell', { name: 'contoso', exact: true })).toBeVisible()

	const form = page.locator('.new-tenant form')
	await form.locator('input[name=alias]').fill('fabrikam')
	await form.locator('input[name=name]').fill('Fabrikam')
	await form.locator('input[name=who]').fill('admin')
	await form.locator('button[type=submit]').click()
	await expect(page.getByRole('cell', { name: 'fabrikam', exact: true })).toBeVisible()

	// The filter is over what was read, and says so: `TenantService` has no
	// `Search`, so the box narrows the page this screen already has.
	await page.getByLabel('filter tenants').fill('fabri')
	await expect(page.getByRole('cell', { name: 'contoso', exact: true })).toHaveCount(0)
	await expect(page.getByRole('cell', { name: 'fabrikam', exact: true })).toBeVisible()
	await page.getByLabel('filter tenants').fill('')

	// Selecting a tenant puts its screens in the **sidebar**, which is the shape
	// the user console has -- one console's worth of screens rather than a
	// disclosure under a table row.
	await page.locator('tr', { hasText: 'fabrikam' }).locator('button', { hasText: 'open' }).click()

	// The place is in the address bar: back leaves the customer and stays in the
	// app, forward returns, and a reload keeps it.
	await expect(page).toHaveURL(/\/tenants\/@fabrikam\/people$/)
	await expect(page.locator('nav h1')).toHaveText('fabrikam')
	await page.goBack()
	await expect(page).toHaveURL(/\/tenants$/)
	await expect(page.locator('nav h1')).toHaveText('roster')
	await page.goForward()
	await expect(page.locator('nav h1')).toHaveText('fabrikam')
	await page.reload()
	await expect(page.locator('nav h1')).toHaveText('fabrikam')
	await expect(page.getByRole('cell', { name: 'admin', exact: true }).first()).toBeVisible()

	// And the selection survives a screen that is not a tenant's at all, which
	// is the one thing a sidebar has to get right: `you` is the control plane's,
	// and coming back must not have lost fabrikam.
	await page.locator('nav button', { hasText: 'you' }).click()
	await expect(page.locator('nav h1')).toHaveText('roster')
	await page.locator('nav button', { hasText: 'people' }).click()
	await expect(page).toHaveURL(/\/tenants\/@fabrikam\/people$/)

	// How they arrive: a name added, then edited in place -- the note changes
	// and the name, which the row is, is not offered. From the sidebar now.
	await page.locator('nav button', { hasText: 'arrives through' }).click()
	await expect(page).toHaveURL(/\/tenants\/@fabrikam\/arrives$/)
	const names = page.locator('h4', { hasText: /^names$/ }).locator('xpath=..')
	await names.locator('input[name=name]').fill('fabrikam.test')
	await names.locator('input[name=desc]').fill('staging')
	await names.locator('button', { hasText: 'add name' }).click()
	const row = names.locator('tr', { hasText: 'fabrikam.test' })
	await expect(row.getByRole('cell', { name: 'staging', exact: true })).toBeVisible()

	await row.locator('button', { hasText: 'edit' }).click()
	await expect(row.locator('input[name=name]')).toHaveCount(0)
	await row.locator('input[name=desc]').fill('production')
	await row.locator('button', { hasText: 'save' }).click()
	await expect(names.locator('tr', { hasText: 'fabrikam.test' }).getByRole('cell', { name: 'production', exact: true })).toBeVisible()
})
