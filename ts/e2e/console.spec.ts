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
	await expect(page.getByRole('cell', { name: /contoso/ })).toBeVisible()

	// Standing one up comes up from the bottom, opened from the bar: the form is
	// not on the screen until somebody asks for it, so the list has the room.
	await page.locator('button.add', { hasText: 'stand one up' }).click()
	const form = page.locator('.sheet .new-tenant form')
	await form.locator('input[name=alias]').fill('fabrikam')
	await form.locator('input[name=name]').fill('Fabrikam')
	await form.locator('input[name=who]').fill('admin')
	await form.locator('button[type=submit]').click()
	await expect(page.locator('.sheet')).toHaveCount(0)
	await expect(page.getByRole('cell', { name: /fabrikam/ })).toBeVisible()

	// The filter is over what was read, and says so: `TenantService` has no
	// `Search`, so the box narrows the page this screen already has.
	await page.getByLabel('filter tenants').fill('fabri')
	await expect(page.getByRole('cell', { name: /contoso/ })).toHaveCount(0)
	await expect(page.getByRole('cell', { name: /fabrikam/ })).toBeVisible()
	await page.getByLabel('filter tenants').fill('')

	// Selecting a tenant puts its screens in the **sidebar**, which is the shape
	// the user console has -- one console's worth of screens rather than a
	// disclosure under a table row.
	await page.locator('tr', { hasText: 'fabrikam' }).locator('button', { hasText: 'open' }).click()

	// Opening one lands on the tenant **itself** and not on its holders: the first
	// thing somebody who just chose a customer wants is to know they chose the
	// right one. The head of the sidebar is what says which, so that is what is
	// asserted -- `h1` says which deployment and never which tenant, because a name
	// in both places is a name somebody has to keep in step.
	const head = page.locator('nav button.head')
	await expect(page).toHaveURL(/\/tenants\/@fabrikam\/tenant$/)
	await expect(head).toHaveText('fabrikam')
	await expect(page.locator('nav h1')).toHaveText('roster')

	// And it is the tenant's own screen under it, with the alias it was reached by
	// and the form that changes what it says about itself.
	await expect(page.locator('section.within h3', { hasText: 'fabrikam' })).toBeVisible()
	await expect(page.locator('h4', { hasText: /^what it is$/ })).toBeVisible()

	// The place is in the address bar: back leaves the screen and stays in the app,
	// forward returns, and a reload keeps it.
	await page.goBack()
	await expect(page).toHaveURL(/\/tenants$/)
	await page.goForward()
	await expect(page).toHaveURL(/\/tenants\/@fabrikam\/tenant$/)
	await page.reload()
	await expect(head).toHaveText('fabrikam')

	// And the selection survives a screen that is not a tenant's at all, which
	// is the one thing a sidebar has to get right: `you` is the control plane's,
	// and coming back must not have lost fabrikam.
	await page.locator('nav button', { hasText: 'you' }).click()
	await expect(page.locator('nav h1')).toHaveText('roster')
	await page.locator('nav button', { hasText: 'holders' }).click()
	await expect(page).toHaveURL(/\/tenants\/@fabrikam\/holders$/)
	await expect(page.getByRole('cell', { name: 'admin', exact: true }).first()).toBeVisible()

	// How they arrive: a name added, then edited in place -- the note changes
	// and the name, which the row is, is not offered. From the sidebar now.
	await page.locator('nav button', { hasText: 'hosts' }).click()
	await expect(page).toHaveURL(/\/tenants\/@fabrikam\/hosts$/)
	const names = page.locator('h4', { hasText: /^hosts$/ }).locator('xpath=..')
	await names.locator('button.add', { hasText: 'add name' }).click()
	const sheet = page.locator('.sheet')
	await sheet.locator('input[name=name]').fill('fabrikam.test')
	await sheet.locator('input[name=desc]').fill('staging')
	await sheet.locator('button[type=submit]').click()
	await expect(sheet).toHaveCount(0)

	// The note is the second line of the name's own cell rather than a column of
	// its own: what a row is called and what it is for are not the same size.
	const row = names.locator('tr', { hasText: 'fabrikam.test' })
	await expect(row).toContainText('staging')

	await row.locator('button', { hasText: 'edit' }).click()
	await expect(row.locator('input[name=name]')).toHaveCount(0)
	await row.locator('input[name=desc]').fill('production')
	await row.locator('button', { hasText: 'save' }).click()
	await expect(names.locator('tr', { hasText: 'fabrikam.test' })).toContainText('production')
})
