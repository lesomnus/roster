import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

// What the account app mails, walked from the page to a mailbox and back: a
// link that confirms an address, one that recovers the account, and one that
// deletes it. The mailbox is Mailpit's (`scripts/e2e.sh`), read through its
// API the way a person reads theirs -- by the one link in the message.
//
// frank of fabrikam, in order: his address confirmed, which the link that
// deletes his account needs; then his password recovered; then his account
// deleted, which is why he is nobody another spec signs in as.

const base = process.env['E2E_FABRIKAM'] ?? 'http://127.0.0.1:18090'
const mailpit = process.env['E2E_MAILPIT']
const address = 'frank@fabrikam.example'
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
	expect(m.From).toEqual({ Name: 'Fabrikam', Address: 'no-reply@fabrikam.example' })
	const links = m.Text.match(/https?:\/\/\S+/g) ?? []
	expect(links, 'the message has more to follow than its link, or nothing').toHaveLength(1)
	await request.delete(`${mailpit}/api/v1/messages`, { data: { IDs: [id] } })

	return links[0] ?? ''
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
	await row.locator('button', { hasText: 'confirm' }).click()
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
	await page.locator('button', { hasText: 'send a link' }).click()
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
	expect(res.status()).toBe(202)

	const link = await linkIn(request, address, 'Delete your account')
	expect(link).toContain('/leave?')
	await page.goto(link)
	await expect(page.getByRole('heading', { name: 'Delete your account' })).toBeVisible()
	await page.locator('input[name=confirm]').fill('frank')
	await page.locator('button', { hasText: 'delete my account' }).click()
	await expect(page.getByRole('heading', { name: 'Your account is deleted' })).toBeVisible()

	await page.goto(base)
	await page.locator('input[name=alias]').fill('frank')
	await page.locator('input[name=password]').fill(password)
	await page.locator('form button[type=submit]', { hasText: 'sign in' }).click()
	await expect(page.locator('.bad')).toBeVisible()
})
