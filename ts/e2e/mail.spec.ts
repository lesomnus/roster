import { expect, test, type APIRequestContext, type Page, type Response } from '@playwright/test'

// What the account app mails, walked from the page to a mailbox and back: a
// link that confirms an address, one that recovers the account, and one that
// deletes it. The mailbox is Mailpit's (`scripts/e2e.sh`), read through its
// API the way a person reads theirs -- by the one link in the message.
//
// frank of tailspin, in order: his address confirmed, which the link that
// deletes his account needs; then his password recovered; then his account
// deleted, which is why he is nobody another spec signs in as.

const base = process.env['E2E_TAILSPIN'] ?? 'http://127.0.0.1:18090'
const mailpit = process.env['E2E_MAILPIT']
const address = 'frank@tailspin.example'
let password = process.env['E2E_FRANK_PASSWORD'] ?? ''

test.describe.configure({ mode: 'serial' })
test.skip(mailpit === undefined, 'no mailbox: the rig ran with E2E_MAIL=0')

type Listed = { messages: { ID: string; Subject: string; To: { Address: string }[] }[] }
type Read = { Text: string; From: { Name: string; Address: string } }

// linkIn is the one link in the message to `to` about `subject`, once it has
// arrived; and the message, read, is taken out of the mailbox.
async function linkIn(request: APIRequestContext, to: string, subject: string): Promise<string> {
	let id = ''
	await expect
		.poll(
			async () => {
				const res = await request.get(`${mailpit}/api/v1/messages`)
				const body = (await res.json()) as Listed
				id = body.messages.find((m) => m.Subject === subject && m.To.some((t) => t.Address === to))?.ID ?? ''
				return id
			},
			{ message: `no message to ${to} about "${subject}"`, timeout: 15_000 },
		)
		.not.toBe('')

	const m = (await (await request.get(`${mailpit}/api/v1/message/${id}`)).json()) as Read
	expect(m.From).toEqual({ Name: 'Tailspin', Address: 'no-reply@tailspin.example' })
	const links = m.Text.match(/https?:\/\/\S+/g) ?? []
	expect(links, 'the message has more to follow than its link, or nothing').toHaveLength(1)
	await request.delete(`${mailpit}/api/v1/messages`, { data: { IDs: [id] } })

	return links[0] ?? ''
}

// answered is a request the page made, held to the status it should have had,
// with its body said when it had another. Read only then: a 202 here has no
// body, and asking a browser for the body of one is an error of its own.
async function answered(res: Response, want: number): Promise<void> {
	if (res.status() !== want) {
		expect(res.status(), `${new URL(res.url()).pathname} answered ${res.status()}: ${await res.text()}`).toBe(want)
	}
}

async function signIn(page: Page): Promise<void> {
	await page.goto(base)
	await page.locator('input[name=alias]').fill('frank')
	await page.locator('input[name=password]').fill(password)
	await page.locator('form button[type=submit]', { hasText: 'sign in' }).click()
	await expect(page.getByRole('heading', { name: 'you', exact: true })).toBeVisible()
}

test('a link confirms an address of his', async ({ page, request }) => {
	await signIn(page)
	const row = page.locator('section', { has: page.getByRole('heading', { name: 'addresses' }) }).locator('tr', { hasText: address })
	const asked = page.waitForResponse((r) => r.url().endsWith('/verify'))
	await row.locator('button', { hasText: 'confirm' }).click()
	await answered(await asked, 202)
	await expect(page.locator('.note', { hasText: `a link is on its way to ${address}` })).toBeVisible()

	const link = await linkIn(request, address, 'Confirm your address')
	expect(link).toContain('/confirm?')
	await page.goto(link)
	await expect(page.getByRole('heading', { name: 'Address confirmed' })).toBeVisible()

	await page.goto(base)
	await expect(row).toContainText('confirmed')
})

test('a link recovers his account, with a password shown once', async ({ page, request }) => {
	await page.goto(base)
	await page.locator('button', { hasText: 'forgot?' }).click()
	await page.locator('input[name=address]').fill(address)
	const asked = page.waitForResponse((r) => r.url().endsWith('/recover'))
	await page.locator('button', { hasText: 'send a link' }).click()
	await answered(await asked, 202)
	await expect(page.locator('.note', { hasText: 'if that address is here, a link is on its way' })).toBeVisible()

	const link = await linkIn(request, address, 'Recover your account')
	expect(link).toContain('/redeem?')
	await page.goto(link)
	await expect(page.getByRole('heading', { name: 'Your new password' })).toBeVisible()
	password = (await page.locator('code').first().textContent()) ?? ''
	expect(password).not.toBe('')

	await signIn(page)
})

test('a link deletes his account, once he types who he is', async ({ page, request }) => {
	await signIn(page)
	// What the page posts for somebody whose way in cannot say when it asked
	// them (`account/account.go`, `leaveLink`); a password says, so the page
	// asks for it instead, and the link is asked for here as it would be.
	const res = await page.request.post(`${base}/leave/link`, { data: { address } })
	expect(res.status(), `/leave/link answered ${res.status()}: ${await res.text()}`).toBe(202)

	const link = await linkIn(request, address, 'Delete your account')
	expect(link).toContain('/leave?')
	await page.goto(link)
	await expect(page.getByRole('heading', { name: 'Delete your account' })).toBeVisible()
	await page.locator('input[name=confirm]').fill('frank')
	await page.locator('button', { hasText: 'delete my account' }).click()
	await expect(page.getByRole('heading', { name: 'Your account is deleted' })).toBeVisible()

	// From a browser that holds nothing of his, as anybody trying his password
	// would be.
	await page.context().clearCookies()
	await page.goto(base)
	await page.locator('input[name=alias]').fill('frank')
	await page.locator('input[name=password]').fill(password)
	await page.locator('form button[type=submit]', { hasText: 'sign in' }).click()
	await expect(page.locator('.bad')).toBeVisible()
})
