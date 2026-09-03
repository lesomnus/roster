import { expect, test, type Page } from '@playwright/test'
import { totp } from './totp.js'

// The account app, as erin uses it: what `account/account_test.go` walks
// through with the calls the page makes written by hand, walked here by the
// page. One person, in order, because each step changes what signs her in.

const base = process.env['E2E_ACCOUNT'] ?? 'http://localhost:18090'
let password = process.env['E2E_ERIN_PASSWORD'] ?? 'correct horse battery staple'

test.describe.configure({ mode: 'serial' })

async function signIn(page: Page, secret = password): Promise<void> {
	await page.goto(base)
	await page.getByRole('textbox', { name: /alias|who/i }).or(page.locator('input[name=alias]')).first().fill('erin')
	await page.locator('input[name=password]').fill(secret)
	await page.locator('form button[type=submit]', { hasText: 'sign in' }).click()
}

async function signedIn(page: Page): Promise<void> {
	await expect(page.getByRole('heading', { name: 'you', exact: true })).toBeVisible()
	await expect(page.locator('code', { hasText: 'erin' }).first()).toBeVisible()
}

async function signOut(page: Page): Promise<void> {
	await page.locator('button', { hasText: 'sign out' }).first().click()
	await expect(page.locator('input[name=password]')).toBeVisible()
}

test('a password signs her in, and a wrong one does not', async ({ page }) => {
	await signIn(page, 'not it')
	await expect(page.locator('.bad')).toBeVisible()
	await expect(page.getByRole('heading', { name: 'you', exact: true })).toHaveCount(0)

	await signIn(page)
	await signedIn(page)
	await signOut(page)
})

test('she changes her password, which asks for the current one', async ({ page }) => {
	await signIn(page)
	await signedIn(page)

	const next = `${password}-2`
	const form = page.locator('form', { has: page.locator('input[name=next]') })
	await form.locator('input[name=current]').fill('not it')
	await form.locator('input[name=next]').fill(next)
	await form.locator('button[type=submit]').click()
	await expect(page.locator('.bad')).toBeVisible()

	await form.locator('input[name=current]').fill(password)
	await form.locator('input[name=next]').fill(next)
	await form.locator('button[type=submit]').click()
	await expect(page.locator('.note', { hasText: 'changed' })).toBeVisible()
	password = next

	await signOut(page)
	await signIn(page)
	await signedIn(page)
})

test('an authenticator app counts once it is proved, and is asked for at the next sign-in', async ({ page }) => {
	await signIn(page)
	await signedIn(page)

	const enrol = page.locator('form', { has: page.locator('select[name=kind]') })
	await enrol.locator('select[name=kind]').selectOption('totp')
	await enrol.locator('input[name=name]').fill('phone')
	await enrol.locator('button[type=submit]').click()

	const uri = await page.locator('code', { hasText: 'otpauth://' }).textContent()
	expect(uri).toContain('otpauth://totp/')

	// Proved with this step's code; the sign-in below uses the next one,
	// because a step that was spent is not accepted twice.
	await page.locator('input[placeholder="the code it shows"]').fill(totp(uri ?? ''))
	await page.locator('button', { hasText: 'prove' }).click()
	await expect(page.locator('.note', { hasText: /proved|counts/ })).toBeVisible()
	await expect(page.locator('td', { hasText: 'authenticator app' })).toBeVisible()

	await signOut(page)
	await signIn(page)
	await expect(page.locator('input[name=code]')).toBeVisible()
	await page.locator('input[name=code]').fill('000000')
	await page.locator('button', { hasText: 'continue' }).click()
	// A wrong code costs the first form again: one attempt per password.
	await expect(page.locator('.bad')).toBeVisible()
	await expect(page.locator('input[name=password]')).toBeVisible()

	await signIn(page)
	await page.locator('input[name=code]').fill(totp(uri ?? '', new Date(), 1))
	await page.locator('button', { hasText: 'continue' }).click()
	await signedIn(page)

	// And off again, so the key below is asked for on its own.
	await page.locator('tr', { hasText: 'authenticator app' }).locator('button', { hasText: 'remove' }).click()
	await expect(page.locator('td', { hasText: 'authenticator app' })).toHaveCount(0)
	await signOut(page)
	await signIn(page)
	await signedIn(page)
})

test('a security key is enrolled by the ceremony, and the page asks for that key', async ({ page, context }) => {
	// A virtual authenticator, so the ceremony is the browser's real one with
	// nobody touching a key.
	const cdp = await context.newCDPSession(page)
	await cdp.send('WebAuthn.enable')
	await cdp.send('WebAuthn.addVirtualAuthenticator', {
		options: {
			protocol: 'ctap2',
			transport: 'internal',
			hasResidentKey: true,
			hasUserVerification: true,
			isUserVerified: true,
			automaticPresenceSimulation: true,
		},
	})

	await signIn(page)
	await signedIn(page)

	const enrol = page.locator('form', { has: page.locator('select[name=kind]') })
	await enrol.locator('select[name=kind]').selectOption('webauthn')
	await enrol.locator('input[name=name]').fill('yubi')
	await enrol.locator('button[type=submit]').click()
	await expect(page.locator('.note', { hasText: 'yubi is enrolled' })).toBeVisible()
	await expect(page.locator('td', { hasText: 'security key' })).toBeVisible()

	await signOut(page)
	await signIn(page)
	const key = page.locator('button', { hasText: 'use your security key (yubi)' })
	await expect(key).toBeVisible()
	await expect(page.locator('input[name=code]')).toHaveCount(0)
	await key.click()
	await signedIn(page)
})
