import { expect, test, type Page } from '@playwright/test'

import { kept, mirrors, movedOn } from './mirror.js'

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
	// The head of the sidebar, which is where the tenant's name is on this page:
	// there is no heading above it, because the page is reached at the tenant's own
	// host and the tenant is the whole of what it is.
	await expect(page.locator('nav button.head')).toHaveText('contoso')

	// The people of contoso, and nobody else's: the seed makes `admin`, `erin`,
	// `grace` and `account` here, and fabrikam's people do not appear because
	// they do not exist to this caller.
	await expect(page.getByRole('cell', { name: 'erin', exact: true })).toBeVisible()
	await expect(page.getByRole('cell', { name: 'account', exact: true })).toBeVisible()

	// One of them opened, which is the screen the admin console draws from the
	// other side of the table -- the same component, a different caller.
	await page.locator('tr', { hasText: who }).locator('button', { hasText: 'signs in with' }).click()
	await expect(page).toHaveURL(new RegExp(`/holders/${who}$`))
	await expect(page.locator('h4', { hasText: who })).toBeVisible()

	// The place is in the address bar, so a reload keeps it.
	await page.reload()
	await expect(page.locator('h4', { hasText: who })).toBeVisible()

	// And somebody added, which is the thing a tenant administrator could not
	// do without a roster operator before #34: this is their own tenant,
	// through the wall, with their own binding.
	await page.locator('nav button', { hasText: 'holders' }).click()
	await page.locator('button.add', { hasText: 'add somebody' }).click()
	const form = page.locator('.sheet .new-holder form')
	await form.locator('input[name=alias]').fill('newcomer')
	await form.locator('button[type=submit]').click()
	await expect(page.locator('.sheet')).toHaveCount(0)
	await expect(page.getByRole('cell', { name: /newcomer/ })).toBeVisible()

	// Which apps act in their tenant, and as whom -- and ending one, which is
	// theirs to do without a roster operator: the app's key is refused here from
	// its next request (#73, #75). The rig installed `kamino`.
	await page.locator('nav button', { hasText: 'apps' }).click()
	const app = page.locator('tr', { hasText: 'kamino' })
	await expect(app).toBeVisible()
	await app.getByRole('button', { name: /more for kamino/ }).click()
	await page.getByRole('button', { name: 'stop it acting here' }).click()
	// The account app is nominated here too -- it provisions itself into every
	// tenant with a name -- so the list is not empty afterwards; kamino's row is
	// what goes.
	await expect(page.locator('tr', { hasText: 'kamino' })).toHaveCount(0)

	// How they arrive is theirs to say too, which is the `Host` row a tenant
	// registers for its own front door.
	await page.locator('nav button', { hasText: 'hosts' }).click()
	// Anchored: the acts cell beside it is named for the row too, because the
	// menu says which row it is the menu for.
	await expect(page.getByRole('cell', { name: /^localhost/ })).toBeVisible()

	// And which road wrote it. The rig's names come from `roster host add` in a
	// shell, which is the roster operator's road and asks for no proof -- so the
	// row says `written` rather than `proved` (#42).
	await expect(page.getByText('written', { exact: true }).first()).toBeVisible()

	// Claiming one of their own, which is the half a tenant could not do before
	// #42: `Host.name` is unique across the deployment, so registering a name
	// used to be a permission a deployment withheld.
	const claiming = page.locator('h4', { hasText: /^host proofs$/ }).locator('xpath=..')
	await claiming.locator('button.add', { hasText: 'claim a name' }).click()
	const sheet = page.locator('.sheet')
	await sheet.locator('input[name=name]').fill('proved.example.com')
	await sheet.locator('button[type=submit]').click()
	await expect(sheet).toHaveCount(0)

	// What roster asked for, laid out the way a DNS provider's form asks for it.
	await expect(claiming.getByText('_roster-challenge.proved.example.com')).toBeVisible()
	await expect(claiming.getByText(/^roster-verify=/)).toBeVisible()

	// And taking it is refused here, naming the setting: this rig cannot publish
	// a record, so it is configured `host.resolver: none` and roster says a
	// roster operator writes the row. The success path is a Go test, over a zone
	// written down rather than a DNS server nobody can run in CI.
	await claiming.locator('button', { hasText: 'take the name' }).click()
	await expect(claiming.locator('.bad')).toContainText('host.resolver')

	// Nothing was written, which is the claim holding nothing.
	await expect(page.getByRole('cell', { name: /^proved\.example\.com/ })).toHaveCount(0)
})

// Where the account app answers, which is what a tenant writes down as its front
// door: the sign-in page sends people there for a provider, and the front door
// sends them back with a link (#62). The rig has no directory to walk the round
// trip against -- `Connection` rows and no provider -- so what is pinned here is
// the half a browser decides: the setting is the tenant's to write, and the page
// draws one button per provider pointing where it was told.
const account = process.env['E2E_ACCOUNT'] ?? 'http://localhost:18090'

test('a tenant names its front door, and the sign-in page sends people there', async ({ page }) => {
	await page.goto(base)
	await page.locator('input[name=alias]').fill(who)
	await page.locator('input[name=password]').fill(password)
	await page.locator('button[type=submit]', { hasText: 'sign in' }).click()
	await expect(page.locator('nav .who')).toHaveText(who)

	// The tenant itself is the head of the sidebar, and its settings are there.
	await page.locator('nav button.head').click()
	const form = page.locator('form.profile')
	await form.locator('input[name=front_door]').fill(account)
	await form.locator('button[type=submit]').click()
	await expect(page.locator('p.note', { hasText: 'saved' })).toBeVisible()

	// Signed out, the page asks again what this name lets somebody in with.
	await page.getByRole('button', { name: 'sign out' }).click()
	await expect(page.locator('input[name=password]')).toBeVisible()

	// contoso has passwords **and** two directories, so both are drawn, under a
	// rule -- and each button goes to the front door, told which provider and
	// where to come back to, which is this page's own origin and nothing configured.
	const origin = new URL(base).origin
	await expect(page.locator('.providers a.button')).toHaveCount(2)
	await expect(page.locator('.providers a.button', { hasText: 'entra' })).toHaveAttribute(
		'href',
		`${account}/login?connection=entra&next=${encodeURIComponent(origin)}`,
	)
	await expect(page.locator('p.or')).toHaveText('or')
})

// A browser one person signs out of and another signs in at keeps nothing of
// the first. The page's store is its caller's, named by the server for their
// session (`AuthService.Which`): signing out forgets what it held, and the next
// person's sign-in deletes every mirror but their own -- whether the one
// before signed out, or only went away and left a session to end by itself.
const grace = process.env['E2E_GRACE_PASSWORD'] ?? ''

async function signIn(page: Page, alias: string, secret: string): Promise<void> {
	await page.locator('input[name=alias]').fill(alias)
	await page.locator('input[name=password]').fill(secret)
	await page.locator('button[type=submit]', { hasText: 'sign in' }).click()
	await expect(page.locator('nav .who')).toHaveText(alias)
}

test('the next person to sign in finds nothing of the one before', async ({ page, context }) => {
	await page.goto(base)
	await signIn(page, who, password)
	await expect.poll(() => mirrors(page)).toHaveLength(1)
	const [theirs = ''] = await mirrors(page)
	await expect.poll(() => kept(page, theirs)).toBeGreaterThan(0)

	// Forgotten, and not only let go of: nothing of theirs is left on the disk,
	// before anybody else has signed in here and whether or not anybody does.
	await page.getByRole('button', { name: 'sign out' }).click()
	await expect(page.locator('input[name=password]')).toBeVisible()
	await expect.poll(() => kept(page, theirs)).toBe(0)

	await signIn(page, 'grace', grace)
	await expect.poll(() => mirrors(page)).not.toContain(theirs)
	await expect.poll(() => mirrors(page)).toHaveLength(1)

	// And somebody who went away without signing out. Their mirror is kept
	// while nobody else has signed in -- a session that ended by itself is the
	// same person when they come back, with what they wrote meanwhile -- and
	// goes the moment somebody else does.
	const [hers] = await mirrors(page)
	await context.clearCookies()
	await page.reload()
	await expect(page.locator('input[name=password]')).toBeVisible()
	expect(await mirrors(page)).toContain(hers)
	await signIn(page, who, password)
	await expect.poll(() => mirrors(page)).not.toContain(hers)
})

// And a sign-out that could not clear the browser's copy says so, rather than
// leave somebody believing nothing of theirs is here: the mirror was moved on
// by another page, as a later deploy's would, so the forget cannot reach it.
test('a sign-out that leaves what was seen on this browser says so', async ({ page }) => {
	await page.goto(base)
	await signIn(page, who, password)
	await expect.poll(() => mirrors(page)).toHaveLength(1)
	const [theirs = ''] = await mirrors(page)
	await expect.poll(() => kept(page, theirs)).toBeGreaterThan(0)

	await movedOn(page, theirs)
	await page.getByRole('button', { name: 'sign out' }).click()

	await expect(page.getByRole('alert')).toContainText('this browser still holds what you saw here')
	await expect(page.locator('input[name=password]')).toBeVisible()
	expect(await kept(page, theirs), 'the page said so of a mirror that is empty').toBeGreaterThan(0)
})

test('a sign-out in one tab is a sign-out in every tab', async ({ context }) => {
	const one = await context.newPage()
	await one.goto(base)
	await signIn(one, who, password)
	const two = await context.newPage()
	await two.goto(base)
	await expect(two.locator('nav .who')).toHaveText(who)

	await one.getByRole('button', { name: 'sign out' }).click()
	await expect(two.locator('input[name=password]')).toBeVisible()
})

test('a page left holding one person\'s store lets go of it when somebody else signs in', async ({ context }) => {
	const one = await context.newPage()
	await one.goto(base)
	await signIn(one, who, password)

	// Somebody else signs in, in another tab, without anybody signing out: the
	// cookie is the browser's, so it is hers now in every tab.
	const two = await context.newPage()
	await two.goto(base)
	const signed = await two.evaluate(
		async (secret) =>
			(
				await fetch('/roster.AuthService/SignIn', {
					method: 'POST',
					headers: { 'content-type': 'application/json', 'connect-protocol-version': '1' },
					body: JSON.stringify({ alias: 'grace', password: secret }),
					credentials: 'include',
				})
			).status,
		grace,
	)
	expect(signed).toBe(200)

	// The first tab's next call is made for the first person's store, and is
	// refused rather than answered as her: it asks who is signed in now, lets
	// go of the store, and draws hers.
	await one.locator('nav button', { hasText: 'apps' }).click()
	await expect(one.locator('nav .who')).toHaveText('grace')
})
