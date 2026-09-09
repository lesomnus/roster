import { expect, test, type Page } from '@playwright/test'

// The Login App's page, in a browser, against nothing.
//
// # Why this exists, and why it needs no deployment
//
// Every other gate on this page is blind to it. `scripts/test.sh` type-checks
// and bundles; `scripts/hydra.sh` walks a whole OAuth flow through a real Hydra
// with **curl**, so no React runs at all. Between them, `ts/login/main.tsx`
// could throw on load and every one of them would stay green -- which is the
// sentence at the top of `scripts/e2e.sh`, about the three defects it found on
// its first run.
//
// And it needs nothing stood up, because `ts/vite.login.ts` is the app made up:
// two people, one of them with a second factor, and the same three status codes
// `frontdoor` answers. What that fake cannot check is anything about roster or
// Hydra, and nothing here tries to -- `login/login_test.go` and
// `scripts/hydra.sh` are where that lives. This checks the **page**: that the
// form posts what the app reads, that a second factor is asked for and drawn
// from what the server said rather than from a label, and that the consent
// screen is a screen.

const base = process.env['E2E_LOGIN'] ?? 'http://127.0.0.1:18101'
const password = 'correct horse battery staple'

/** A flow always starts where Hydra would have sent the browser. */
async function begin(page: Page): Promise<void> {
	await page.goto(`${base}/login?login_challenge=sandbox`)
	await expect(page.locator('input[name=password]')).toBeVisible()
}

async function first(page: Page, who: string, secret = password): Promise<void> {
	await page.locator('input[name=alias]').fill(who)
	await page.locator('input[name=password]').fill(secret)
	await page.locator('form button[type=submit]', { hasText: 'sign in' }).click()
}

test('the page draws, and a wrong password is refused', async ({ page }) => {
	await begin(page)

	// The brand comes from `/flow`, which is the one thing this page asks the
	// app for: it may not ask Hydra and the app may.
	await expect(page.getByRole('heading', { name: 'Contoso' })).toBeVisible()

	await first(page, 'erin', 'not it')
	await expect(page.locator('.bad')).toBeVisible()
	await expect(page.locator('input[name=password]')).toBeVisible()
})

test('a password alone finishes it for somebody with no second factor', async ({ page }) => {
	await begin(page)
	await first(page, 'erin')

	// The consent screen, which is where `POST /accept` sent her -- so getting
	// here at all is the last hop having worked.
	await expect(page.getByRole('button', { name: 'allow' })).toBeVisible()
	await expect(page.getByText('openid')).toBeVisible()
})

test('and the second form is asked for somebody who has one', async ({ page }) => {
	await begin(page)
	await first(page, 'frank')

	// Drawn from what the server said is left to prove, and not from a label
	// it sent: the page is told `totp` and decides what to call it.
	const code = page.locator('input[name=code]')
	await expect(code).toBeVisible()
	await expect(page.getByRole('button', { name: 'allow' })).toHaveCount(0)

	// One attempt per first form: a wrong code sends her back to the password,
	// where the lockout counts.
	await code.fill('000000')
	await page.locator('form button[type=submit]', { hasText: 'continue' }).click()
	await expect(page.locator('input[name=password]')).toBeVisible()
	await expect(page.locator('.bad')).toBeVisible()

	await first(page, 'frank')
	await page.locator('input[name=code]').fill('123456')
	await page.locator('form button[type=submit]', { hasText: 'continue' }).click()

	await expect(page.getByRole('button', { name: 'allow' })).toBeVisible()
})

test('the consent screen is a screen, and a no is an answer', async ({ page }) => {
	await begin(page)
	await first(page, 'erin')

	// Which app is asking, and for what. Neither is the page's to invent.
	await expect(page.getByRole('heading', { name: 'the demo product' })).toBeVisible()
	for (const scope of ['openid', 'profile', 'email']) {
		await expect(page.getByText(scope, { exact: true })).toBeVisible()
	}

	// No checkboxes. A screen that lets somebody grant less than a client asked
	// for reads like a choice and is not one.
	await expect(page.locator('input[type=checkbox]')).toHaveCount(0)

	await page.getByRole('button', { name: 'no' }).click()
	await expect(page).toHaveURL(/denied/)
})
