import { expect, test } from '@playwright/test'

// The console, as an operator uses it: sign in with the password `roster init`
// took, stand a customer up from the customers screen -- which is the page
// reaching the admin listener from another origin, with the session cookie
// the control listener set -- and see their first person.

const base = process.env['E2E_CONSOLE'] ?? 'http://127.0.0.1:18062/console/'
const password = process.env['E2E_OPS_PASSWORD'] ?? ''

test('an operator signs in and stands a customer up', async ({ page }) => {
	await page.goto(base)
	await page.locator('input[name=alias]').fill('ops')
	await page.locator('input[name=password]').fill(password)
	await page.locator('button[type=submit]', { hasText: 'sign in' }).click()
	await expect(page.locator('nav .who')).toHaveText('ops')

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
})
