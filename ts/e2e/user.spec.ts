import { expect, test } from '@playwright/test'

// The user console, as somebody in a tenant uses it: sign in at the name their
// tenant answers at, and see their own organisation.
//
// Three things here are true of no other page and are why this spec exists.
// The caller is a **data plane** holder, so what comes back is narrowed by the
// wall rather than by an operator's port. The form asks for no tenant, because
// the one in the address bar is what the sign-in resolved against -- so a page
// served anywhere else would sign somebody in to somebody else's organisation
// or to nobody. And the page, the sign-in and the rows are one listener, which
// is what a `__Host-` cookie requires and what nothing but a browser checks.

const base = process.env['E2E_USER'] ?? 'http://localhost:18052/'

// contoso's own administrator -- the holder `Tenant.Add` wrote -- and not erin,
// because the account spec walks her through changing her password and then
// enrolling a second factor. The specs run in order on one worker, so signing
// in as her here would be signing in with a password she no longer has.
const who = 'admin'
const password = process.env['E2E_TENANT_ADMIN_PASSWORD'] ?? ''

test('a tenant administrator signs in and sees their own tenant', async ({ page }) => {
	await page.goto(base)
	await page.locator('input[name=alias]').fill(who)
	await page.locator('input[name=password]').fill(password)
	await page.locator('button[type=submit]', { hasText: 'sign in' }).click()

	// Who, and **which organisation** -- read from `Tenant.Get` on the
	// identifier `Me.Get` answered with, which the wall can only ever answer
	// about theirs.
	await expect(page.locator('nav .who')).toHaveText(who)
	await expect(page.locator('nav h1')).toHaveText('contoso')

	// The people of contoso, and nobody else's: the seed makes `admin`, `erin`
	// and `account` here, and fabrikam's people do not appear because they do
	// not exist to this caller.
	await expect(page.getByRole('cell', { name: 'erin', exact: true })).toBeVisible()
	await expect(page.getByRole('cell', { name: 'account', exact: true })).toBeVisible()

	// One of them opened, which is the screen the admin console draws from the
	// other side of the table -- the same component, a different caller.
	await page.locator('tr', { hasText: who }).locator('button', { hasText: 'signs in with' }).click()
	await expect(page).toHaveURL(new RegExp(`/people/${who}$`))
	await expect(page.locator('h4', { hasText: who })).toBeVisible()

	// The place is in the address bar, so a reload keeps it.
	await page.reload()
	await expect(page.locator('h4', { hasText: who })).toBeVisible()

	// And somebody added, which is the thing a tenant administrator could not
	// do without a roster operator before #34: this is their own tenant,
	// through the wall, with their own binding.
	await page.locator('nav button', { hasText: 'people' }).click()
	const form = page.locator('.new-holder form')
	await form.locator('input[name=alias]').fill('newcomer')
	await form.locator('button[type=submit]').click()
	await expect(page.getByRole('cell', { name: 'newcomer', exact: true })).toBeVisible()

	// How they arrive is theirs to say too, which is the `Host` row a tenant
	// registers for its own front door.
	await page.locator('nav button', { hasText: 'arrives through' }).click()
	await expect(page.getByRole('cell', { name: 'localhost', exact: true })).toBeVisible()
})
