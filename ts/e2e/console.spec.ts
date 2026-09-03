import { expect, test } from '@playwright/test'

// The console, as an operator uses it: sign in with the password `roster init`
// took, stand a customer up from the customers screen -- which is the page
// reaching the admin listener from another origin, with the session cookie
// the control listener set -- and see their first person.

const base = process.env['E2E_CONSOLE'] ?? 'http://127.0.0.1:18062/console/'
const operator = process.env['E2E_OPS_USER'] ?? 'ops'
const password = process.env['E2E_OPS_PASSWORD'] ?? ''

test('an operator signs in and stands a customer up', async ({ page }) => {
	await page.goto(base)
	await page.locator('input[name=alias]').fill(operator)
	await page.locator('input[name=password]').fill(password)
	await page.locator('button[type=submit]', { hasText: 'sign in' }).click()
	await expect(page.locator('nav .who')).toHaveText(operator)

	await page.locator('nav button', { hasText: 'customers' }).click()
	await expect(page.locator('h2', { hasText: 'customers' })).toBeVisible()
	await expect(page.getByRole('cell', { name: 'contoso', exact: true })).toBeVisible()

	const form = page.locator('.new-customer form')
	await form.locator('input[name=alias]').fill('fabrikam')
	await form.locator('input[name=name]').fill('Fabrikam')
	await form.locator('input[name=who]').fill('admin')
	await form.locator('button[type=submit]').click()
	await expect(page.getByRole('cell', { name: 'fabrikam', exact: true })).toBeVisible()

	await page.locator('tr', { hasText: 'fabrikam' }).locator('button', { hasText: 'people' }).click()
	await expect(page.getByRole('cell', { name: 'admin', exact: true }).first()).toBeVisible()

	// How they arrive: a name added, then edited in place -- the note changes
	// and the name, which the row is, is not offered.
	await page.locator('tr', { hasText: 'fabrikam' }).locator('button', { hasText: 'arrives through' }).click()
	const names = page.locator('h4', { hasText: 'names' }).locator('xpath=..')
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
