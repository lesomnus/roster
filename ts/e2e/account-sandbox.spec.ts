import { expect, test, type Page } from '@playwright/test'
import { Matrix4, MeshBasicMaterial, Quaternion, Euler, Vector3 } from 'three'
import { WovenRibbon } from '#lib/badge-ribbon.ts'
import { badgeArrival, BADGE_ARRIVAL_SECONDS } from '#lib/badge-arrival.ts'
import { mirrors } from './mirror.js'

// Only the evaluated callback reads the browser DOM; this spec also compiles
// under the Node-only configuration used by scripts/e2e.sh.
declare const document: { documentElement: { scrollWidth: number; clientWidth: number } }
declare function getComputedStyle(element: unknown): { animationName: string; transform: string; getPropertyValue(name: string): string }
declare function requestAnimationFrame(callback: (at: number) => void): number
declare const window: { scrollTo(x: number, y: number): void }

const base = process.env['E2E_ACCOUNT_SANDBOX']
test.skip(base === undefined, 'the account sandbox is not running')
// Each journey crosses several screens; allow for a busy development machine.
test.setTimeout(60_000)

const section = (page: Page, name: string) => page.locator('section').filter({ has: page.getByRole('heading', { name, exact: true }) })
const signIn = async (page: Page, alias = 'erin', password = 'correct horse battery staple') => {
	// The account still has a key-alias field while sign-out closes its store.
	// Wait for the actual sign-in form rather than filling that unrelated field.
	const form = page.locator('.sign-in')
	await form.locator('input[name=alias]').fill(alias)
	await form.locator('input[name=password]').fill(password)
	await form.getByRole('button', { name: 'sign in', exact: true }).click()
}

test('account sandbox edits, signs out and resets without an API server or disk mirror', async ({ page }) => {
	const apiRequests: string[] = []
	const errors: string[] = []
	page.on('pageerror', (e) => errors.push(e.message))
	// Let Vite serve code, styles and HMR. Fail any attempt to reach a backend.
	await page.route('**/*', async (route) => {
		const req = route.request()
		if (req.resourceType() === 'fetch' || req.resourceType() === 'xhr' || new URL(req.url()).origin !== base) {
			apiRequests.push(req.url())
			await route.abort()
		} else await route.continue()
	})
	await page.goto(`${base}/`)
	await expect(page.getByRole('heading', { name: 'you', exact: true })).toBeVisible()
	const profile = section(page, 'you')
	await expect(profile.locator('input[name=display_name]')).toHaveValue('Erin Chen')
	await profile.locator('input[name=display_name]').fill('Erin Preview')
	await profile.getByRole('button', { name: 'save', exact: true }).click()
	await expect(profile.getByText('saved', { exact: true })).toBeVisible()
	await expect(page.getByRole('article', { name: 'Employee ID for Erin Preview', exact: true, includeHidden: true })).toHaveCount(1)

	const addresses = section(page, 'addresses')
	await addresses.locator('input[name=address]').fill('preview@example.com')
	await addresses.getByRole('button', { name: 'add address', exact: true }).click()
	await expect(addresses.getByRole('cell', { name: 'preview@example.com', exact: true })).toBeVisible()
	await addresses.locator('tr', { hasText: 'preview@example.com' }).getByRole('button', { name: 'remove', exact: true }).click()
	await expect(addresses.getByRole('cell', { name: 'preview@example.com', exact: true })).toHaveCount(0)

	const keys = section(page, 'keys')
	await keys.locator('input[name=app]').fill('preview-nas')
	await keys.getByRole('button', { name: 'mint an app password' }).click()
	await expect(keys.getByRole('cell', { name: 'preview-nas', exact: true })).toBeVisible()
	await expect(keys.locator('.secret code')).toContainText('sandbox_only_')
	await keys.locator('tr', { hasText: 'preview-nas' }).getByRole('button', { name: 'revoke' }).click()
	await expect(keys.getByRole('cell', { name: 'preview-nas', exact: true })).toHaveCount(0)

	await page.getByRole('button', { name: 'sign out', exact: true }).click()
	await signIn(page, 'erin', 'wrong password')
	await expect(page.locator('.sign-in .bad')).toHaveText('no')
	await signIn(page)
	await expect(profile.locator('input[name=display_name]')).toHaveValue('Erin Preview')
	await page.getByRole('button', { name: 'reset preview' }).click()
	await expect(profile.locator('input[name=display_name]')).toHaveValue('Erin Chen')
	expect(await mirrors(page, 'account-sandbox')).toEqual([])
	expect(await mirrors(page)).toEqual([])
	expect(apiRequests).toEqual([])
	expect(errors).toEqual([])
})

test('account sandbox previews MFA, empty, directory and limited states', async ({ page }) => {
	await page.goto(`${base}/?sandbox=mfa`)
	await signIn(page, 'frank')
	await expect(page.locator('input[name=code]')).toBeVisible()
	await page.locator('input[name=code]').fill('000000')
	await page.getByRole('button', { name: 'continue', exact: true }).click()
	await expect(page.locator('input[name=password]')).toBeVisible()
	await signIn(page, 'frank')
	await page.locator('input[name=code]').fill('123456')
	await page.getByRole('button', { name: 'continue', exact: true }).click()
	await expect(page.getByRole('heading', { name: 'you', exact: true })).toBeVisible()

	await page.getByRole('combobox', { name: 'preview state' }).selectOption('empty')
	await expect(section(page, 'addresses').getByText('none — a link has nowhere to go')).toBeVisible()
	await expect(section(page, 'keys').getByText('none — nothing of yours calls this deployment')).toBeVisible()
	await page.getByRole('combobox', { name: 'preview state' }).selectOption('directory')
	await expect(page.locator('.directory')).toBeVisible()
	await expect(page.getByRole('heading', { name: 'delete your account', exact: true })).toHaveCount(0)
	await page.getByRole('combobox', { name: 'preview state' }).selectOption('limited')
	await expect(section(page, 'you').getByText('this needs a role naming /roster.HolderService/Update')).toBeVisible()
})

test('account sandbox simulates providers and security keys without leaving the browser', async ({ page }) => {
	await page.goto(`${base}/?sandbox=signed-out`)
	await page.getByRole('link', { name: 'sign in with github' }).click()
	await expect(page.getByRole('heading', { name: 'you', exact: true })).toBeVisible()
	await expect(section(page, 'signs in with').getByRole('row', { name: /github/ })).toContainText('unlink')
	const github = section(page, 'signs in with').getByRole('row', { name: /github/ })
	await github.getByRole('button', { name: 'unlink', exact: true }).click()
	await github.getByRole('button', { name: 'connect', exact: true }).click()
	await expect(github.getByRole('button', { name: 'unlink', exact: true })).toBeVisible()

	const factors = section(page, 'second factors')
	await factors.locator('select[name=kind]').selectOption('webauthn')
	await factors.locator('input[name=name]').fill('preview-key')
	await factors.getByRole('button', { name: 'enrol', exact: true }).click()
	await expect(factors.getByText('preview-key is enrolled and counts from now on: the ceremony that enrolled it proved it')).toBeVisible()
	await page.getByRole('button', { name: 'sign out', exact: true }).click()
	await signIn(page)
	await page.getByRole('button', { name: 'use your security key (preview-key)', exact: true }).click()
	await expect(page.getByRole('heading', { name: 'you', exact: true })).toBeVisible()
})

test('account Nox fields are labelled, themes switch, and the page fits a phone', async ({ page }, info) => {
	const errors: string[] = []
	page.on('pageerror', (error) => errors.push(error.message))
	await page.emulateMedia({ colorScheme: 'dark' })
	await page.goto(`${base}/`)
	await expect(page.getByRole('textbox', { name: 'Display name', exact: true })).toHaveValue('Erin Chen')
	await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'dark')
	await page.screenshot({ path: info.outputPath('account-dark.png') })
	await page.reload()
	await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'dark')
	await page.emulateMedia({ colorScheme: 'light' })
	await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'light')
	await page.screenshot({ path: info.outputPath('account-light.png') })
	await page.setViewportSize({ width: 390, height: 844 })
	await expect(page.getByRole('textbox', { name: 'Display name', exact: true })).toBeVisible()
	const width = await page.evaluate(() => ({ content: document.documentElement.scrollWidth, viewport: document.documentElement.clientWidth }))
	expect(width.content).toBeLessThanOrEqual(width.viewport)
	await page.screenshot({ path: info.outputPath('account-mobile.png') })
	expect(errors).toEqual([])
})

test('the HTML employee ID edits basic information and its photo without clearing other profile fields', async ({ page }, info) => {
	test.setTimeout(90_000)
	await page.goto(`${base}/`)
	const scene = page.getByRole('region', { name: 'Your employee ID', exact: true })
	await expect(scene).toHaveAttribute('data-renderer', 'webgl', { timeout: 30_000 })
	await expect(scene).toHaveAttribute('data-badge-arrived', 'true')
	const badge = page.locator('.employee-badge')
	// Move onto the floating card as a person does before pressing a field.
	// Locator.click waits for stability before it moves the pointer itself.
	await page.mouse.move(page.viewportSize()!.width / 2, page.viewportSize()!.height / 2)
	await expect.poll(async () => JSON.parse(await scene.getAttribute('data-badge-pose') ?? '[]') as number[]).toEqual([0, 0, 0, 0])
	await badge.getByRole('button', { name: 'Edit employee ID display name', exact: true }).click()
	await badge.getByRole('textbox', { name: 'Employee ID display name', exact: true }).fill('Not saved')
	await badge.getByRole('textbox', { name: 'Employee ID display name', exact: true }).press('Escape')
	await expect(badge).toContainText('Erin Chen')
	await badge.getByRole('button', { name: 'Edit employee ID display name', exact: true }).click()
	await badge.getByRole('textbox', { name: 'Employee ID display name', exact: true }).fill('Erin Badge')
	await expect(badge.locator('.badge-alias')).toHaveText('@erin')
	await expect(badge.getByRole('button', { name: /^(Save|Cancel)$/ })).toHaveCount(0)
	await page.mouse.move(15, 15)
	await expect.poll(async () => JSON.parse(await scene.getAttribute('data-badge-pose') ?? '[]') as number[]).toEqual([0, 0, 0, 0])
	await page.screenshot({ path: info.outputPath('employee-id-editing.png') })
	await badge.getByRole('textbox', { name: 'Employee ID display name', exact: true }).press('Enter')
	await expect(badge.getByRole('status')).toHaveText('Saved')
	await expect(badge).toHaveAttribute('aria-label', 'Employee ID for Erin Badge')
	await expect(page.locator('.profile input[name=display_name]')).toHaveValue('Erin Badge')
	await expect(page.locator('.profile input[name=locale]')).toHaveValue('en')
	await expect(page.locator('.profile input[name=employee_no]')).toHaveValue('E-1042')
	await page.locator('.profile input[name=locale]').fill('ko')
	await page.evaluate(() => window.scrollTo(0, 0))
	await expect.poll(async () => Number(await scene.getAttribute('data-badge-scroll'))).toBe(0)
	await page.mouse.move(page.viewportSize()!.width / 2, page.viewportSize()!.height / 2)
	await expect.poll(async () => JSON.parse(await scene.getAttribute('data-badge-pose') ?? '[]') as number[]).toEqual([0, 0, 0, 0])
	await badge.getByRole('button', { name: 'Edit employee ID employee number', exact: true }).click()
	await badge.getByRole('textbox', { name: 'Employee ID employee number', exact: true }).fill('1043')
	await page.getByRole('button', { name: 'sign out', exact: true }).focus()
	await expect(badge.getByRole('status')).toHaveText('Saved')
	await expect(page.locator('.profile input[name=employee_no]')).toHaveValue('1043')
	await expect(page.locator('.profile input[name=department]')).toHaveValue('Design')
	const pixel = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg==', 'base64')
	await badge.getByLabel('Employee ID photo', { exact: true }).setInputFiles({ name: 'portrait.png', mimeType: 'image/png', buffer: pixel })
	await expect(badge.locator('.badge-photo img')).toBeVisible()
	await expect(page.locator('.picture img')).toHaveAttribute('src', /^blob:/)
	await page.mouse.move(page.viewportSize()!.width / 2, page.viewportSize()!.height / 2)
	await badge.locator('.badge-portrait').hover()
	await badge.getByRole('button', { name: 'Remove employee ID photo', exact: true }).click()
	await expect(badge.locator('.badge-photo img')).toHaveCount(0)
	await expect(page.locator('.picture .none')).toHaveText('no picture')
	await badge.getByLabel('Employee ID photo', { exact: true }).setInputFiles({ name: 'large.png', mimeType: 'image/png', buffer: Buffer.alloc(2 * 1024 * 1024 + 1) })
	await expect(badge.getByRole('alert')).toContainText('larger than 2 MiB')
	await expect(page.locator('.profile input[name=locale]')).toHaveValue('ko')
})

test('the employee ID respects read-only access and explains directory-managed edits', async ({ page }) => {
	await page.goto(`${base}/?sandbox=limited`)
	const scene = page.getByRole('region', { name: 'Your employee ID', exact: true })
	await expect(scene).toHaveAttribute('data-badge-arrived', 'true', { timeout: 30_000 })
	const badge = page.locator('.employee-badge')
	await expect(badge.getByRole('button', { name: /^Edit employee ID/ })).toHaveCount(0)
	await expect(badge.getByRole('button', { name: 'Change employee ID photo', exact: true })).toBeDisabled()
	await page.goto(`${base}/?sandbox=directory`)
	await expect(scene).toHaveAttribute('data-badge-arrived', 'true', { timeout: 30_000 })
	await page.mouse.move(page.viewportSize()!.width / 2, page.viewportSize()!.height / 2)
	await expect.poll(async () => JSON.parse(await scene.getAttribute('data-badge-pose') ?? '[]') as number[]).toEqual([0, 0, 0, 0])
	await badge.getByRole('button', { name: 'Edit employee ID display name', exact: true }).click()
	await expect(badge.getByRole('textbox', { name: 'Employee ID display name', exact: true })).toHaveAttribute('aria-description', 'Your directory may overwrite this change.')
	await expect(badge.locator('.badge-alias')).toHaveText('@grace')
	await badge.getByRole('textbox', { name: 'Employee ID display name', exact: true }).press('Escape')
})

test('the WebGL employee badge follows the pointer and holds still on hover', async ({ page }, info) => {
	await page.goto(`${base}/`)
	const scene = page.getByRole('region', { name: 'Your employee ID', exact: true })
	const badge = page.getByRole('article', { name: 'Employee ID for Erin Chen', exact: true })
	await expect(scene).toHaveAttribute('data-renderer', 'webgl', { timeout: 30_000 })
	await expect(scene).toHaveAttribute('data-badge-arrived', 'true')
	await expect(badge).toBeVisible()
	await expect(badge).not.toContainText('Design')
	await expect(badge.locator('.badge-employee-number')).toHaveText('01042')
	const stage = await scene.boundingBox()
	if (stage === null) throw new Error('the badge scene has no size')
	const pose = async () => JSON.parse(await scene.getAttribute('data-badge-pose') ?? '[]') as number[]
	const initialPose = await pose()
	expect(Math.abs(initialPose[0] ?? 0)).toBeLessThan(2.4)
	expect(initialPose.slice(1)).toEqual([0, 0, 0])
	// Check the actual mesh's portrait projection, including the preview bar
	// above the canvas, rather than its enclosing card.
	const viewport = page.viewportSize()!
	const photo = JSON.parse(await scene.getAttribute('data-badge-portrait-center') ?? '[]') as number[]
	expect(photo[0]).toBeCloseTo(viewport.width / 2, 0)
	expect(photo[1]).toBeCloseTo(viewport.height / 2, 0)
	await expect.poll(async () => {
		const box = await badge.boundingBox()
		return box !== null && box.y > stage.y && box.y + box.height < stage.y + stage.height
	}).toBe(true)
	await page.mouse.move(viewport.width / 2, viewport.height / 2)
	await expect.poll(pose).toEqual([0, 0, 0, 0])
	const htmlPortrait = (await badge.locator('.badge-portrait').boundingBox())!
	expect(htmlPortrait.x + htmlPortrait.width / 2).toBeCloseTo(viewport.width / 2, 0)
	expect(htmlPortrait.y + htmlPortrait.height / 2).toBeCloseTo(viewport.height / 2, 0)
	const motion = () => scene.evaluate((el) => ({
		tip: JSON.parse(el.getAttribute('data-ribbon-world-tip') ?? '[]') as number[],
		matrix: JSON.parse(el.getAttribute('data-badge-matrix') ?? '[]') as number[],
	}))
	const beforeTurn = await motion()
	await page.mouse.move(stage.x + stage.width * .93, stage.y + stage.height * .45)
	await expect.poll(async () => (await pose())[1] ?? 0).toBeGreaterThan(15)
	const afterTurn = await motion()
	const rigidTip = new Vector3().fromArray(beforeTurn.tip).applyMatrix4(new Matrix4().fromArray(beforeTurn.matrix).invert()).applyMatrix4(new Matrix4().fromArray(afterTurn.matrix))
	expect(rigidTip.distanceTo(new Vector3().fromArray(afterTurn.tip))).toBeGreaterThan(.05)
	await page.mouse.move(stage.x + stage.width * .07, stage.y + stage.height * .45)
	await expect.poll(async () => (await pose())[1] ?? 0).toBeLessThan(-15)
	await page.screenshot({ path: info.outputPath('badge-looking-left.png') })
	const tilted = await badge.boundingBox()
	if (tilted === null) throw new Error('the badge has no size')
	await page.mouse.move(tilted.x + tilted.width / 2, tilted.y + tilted.height / 2)
	await expect.poll(pose).toEqual([0, 0, 0, 0])
	const steady = await badge.boundingBox()
	if (steady === null) throw new Error('the front-facing badge has no size')
	await page.mouse.move(steady.x + steady.width * .85, steady.y + steady.height * .2)
	const transforms = await page.locator('.badge-turn').evaluate(async (el) => {
		const frames: string[] = []
		for (let i = 0; i < 12; i++) {
			await new Promise<void>((done) => requestAnimationFrame(() => done()))
			const box = el.getBoundingClientRect()
			frames.push(JSON.stringify([box.x, box.y, box.width, box.height]))
		}
		return frames
	})
	expect(new Set(transforms).size).toBe(1)
	expect(await page.locator('.badge-canvas').evaluate((el) => (el as unknown as { getContext(name: string): unknown }).getContext('webgl2') !== null)).toBe(true)
	const before = await scene.getAttribute('data-ribbon-tip')
	await expect.poll(() => scene.getAttribute('data-ribbon-tip')).not.toBe(before)
	await page.screenshot({ path: info.outputPath('badge-front-and-loop.png') })
	await page.screenshot({ path: info.outputPath('ribbon-clip-detail.png'), clip: {
		x: steady.x - 50, y: Math.max(stage.y, steady.y - 100), width: steady.width + 100, height: 170,
	} })
	await page.mouse.move(stage.x + stage.width * .07, stage.y + stage.height * .45)
	await expect.poll(async () => (await pose())[1] ?? 0).toBeLessThan(-10)
	await expect(scene.getByRole('link')).toHaveCount(0)
	await page.getByRole('textbox', { name: 'Display name', exact: true }).scrollIntoViewIfNeeded()
	await expect(page.getByRole('textbox', { name: 'Display name', exact: true })).toBeInViewport()
})

test('scroll lifts the badge inside a fixed view and content pushes the toolbar away', async ({ page }, info) => {
	await page.goto(`${base}/`)
	const scene = page.getByRole('region', { name: 'Your employee ID', exact: true })
	await expect(scene).toHaveAttribute('data-renderer', 'webgl', { timeout: 30_000 })
	await expect(scene).toHaveAttribute('data-badge-arrived', 'true')
	const canvas = page.locator('.badge-canvas'), background = page.locator('.account-backdrop')
	const toolbar = page.locator('.account-header'), content = page.locator('.account-settings')
	const canvasBox = await canvas.boundingBox(), backgroundBox = await background.boundingBox()
	const contentBox = (await content.boundingBox())!, toolbarBox = (await toolbar.boundingBox())!
	const motion = () => scene.evaluate(el => ({
		tip: JSON.parse(el.getAttribute('data-ribbon-world-tip') ?? '[]') as number[],
		matrix: JSON.parse(el.getAttribute('data-badge-matrix') ?? '[]') as number[],
	}))
	const before = await motion()
	await page.evaluate(() => window.scrollTo(0, 240))
	await expect.poll(async () => Number(await scene.getAttribute('data-badge-scroll'))).toBeGreaterThan(2)
	expect(await canvas.boundingBox()).toEqual(canvasBox)
	expect(await background.boundingBox()).toEqual(backgroundBox)
	expect((await content.boundingBox())!.y).toBeCloseTo(contentBox.y - 240, 0)
	expect((await toolbar.boundingBox())!.y).toBeCloseTo(16, 0)
	const after = await motion()
	const rigidTip = new Vector3().fromArray(before.tip).applyMatrix4(new Matrix4().fromArray(before.matrix).invert()).applyMatrix4(new Matrix4().fromArray(after.matrix))
	expect(rigidTip.distanceTo(new Vector3().fromArray(after.tip))).toBeGreaterThan(.1)
	await page.screenshot({ path: info.outputPath('scroll-badge-and-content.png') })
	await page.evaluate(y => window.scrollTo(0, y), contentBox.y - 40)
	await expect.poll(async () => (await toolbar.boundingBox())!.y).toBeLessThan(0)
	const touching = (await toolbar.boundingBox())!
	expect(touching.y + touching.height).toBeCloseTo((await content.boundingBox())!.y, 0)
	await page.evaluate(y => window.scrollTo(0, y), contentBox.y + toolbarBox.height + 24)
	await expect(toolbar).not.toBeInViewport()
	expect(await canvas.boundingBox()).toEqual(canvasBox)
	expect(await background.boundingBox()).toEqual(backgroundBox)
	await page.screenshot({ path: info.outputPath('scroll-content-and-fixed-background.png') })
	await page.evaluate(() => window.scrollTo(0, 2000))
	let last = 0
	await expect.poll(async () => {
		const lift = Number(await scene.getAttribute('data-badge-scroll'))
		const settled = lift > 15 && Math.abs(lift - last) < .00001
		last = lift
		return settled
	}, { intervals: [200, 300] }).toBe(true)
	await page.evaluate(() => window.scrollTo(0, 0))
	await expect.poll(async () => Number(await scene.getAttribute('data-badge-scroll'))).toBe(0)
	expect((await toolbar.boundingBox())!.y).toBeCloseTo(toolbarBox.y, 0)
	const portrait = JSON.parse(await scene.getAttribute('data-badge-portrait-center') ?? '[]') as number[]
	expect(portrait[1]).toBeCloseTo(page.viewportSize()!.height / 2, 0)
})

test('pointer translation tapers with distance, saturates and settles on hover', async ({ page }) => {
	await page.goto(`${base}/`)
	const scene = page.getByRole('region', { name: 'Your employee ID', exact: true })
	await expect(scene).toHaveAttribute('data-renderer', 'webgl', { timeout: 30_000 })
	await expect(scene).toHaveAttribute('data-badge-arrived', 'true')
	const viewport = page.viewportSize()!
	const position = async () => JSON.parse(await scene.getAttribute('data-badge-position') ?? '[]') as number[]
	const offsets: number[] = []
	for (const distance of [140, 210, 280, 350, 420]) {
		await page.mouse.move(viewport.width / 2 + distance, viewport.height / 2)
		let last = 0
		await expect.poll(async () => {
			const x = (await position())[0] ?? 0
			const settled = x > .2 && Math.abs(x - last) < .0002
			last = x
			return settled
		}, { intervals: [100, 200, 300] }).toBe(true)
		offsets.push((await position())[0]!)
	}
	// Equal pointer steps give progressively smaller increments, then none.
	expect(offsets[1]! - offsets[0]!).toBeGreaterThan(offsets[2]! - offsets[1]!)
	expect(offsets[2]! - offsets[1]!).toBeGreaterThan(offsets[3]! - offsets[2]!)
	expect(offsets[4]).toBeCloseTo(offsets[3]!, 3)
	await page.mouse.move(viewport.width / 2, viewport.height / 2)
	await expect.poll(position).toEqual([0, 0, 0])
	// The viewport-wide tracker also works above the hero, over the toolbar.
	await page.mouse.move(viewport.width / 2, 10)
	await expect.poll(async () => (await position())[1] ?? 0).toBeGreaterThan(.4)
})

test('the floating ribbon keeps its length behind the badge and moves the clasp', async ({ page }, info) => {
	const errors: string[] = []
	page.on('pageerror', (error) => errors.push(error.message))
	await page.goto(`${base}/`)
	const scene = page.getByRole('region', { name: 'Your employee ID', exact: true })
	await expect(scene).toHaveAttribute('data-renderer', 'webgl', { timeout: 30_000 })
	await expect(scene).toHaveAttribute('data-badge-arrived', 'true')
	const restLength = Number(await scene.getAttribute('data-ribbon-rest-length'))
	for (const viewport of [{ width: 1280, height: 720 }, { width: 390, height: 844 }]) {
		await page.setViewportSize(viewport)
		await page.mouse.move(viewport.width * .08, viewport.height / 2)
		const samples = []
		for (let i = 0; i < 8; i++) {
			// Sample a moving, tilted mesh rather than only its initial rest pose.
			await page.waitForTimeout(200)
			samples.push(await scene.evaluate((el) => ({
				length: Number(el.getAttribute('data-ribbon-length')),
				restLength: Number(el.getAttribute('data-ribbon-rest-length')),
				stretch: Number(el.getAttribute('data-ribbon-max-stretch')),
				frontZ: Number(el.getAttribute('data-ribbon-front-z')),
				anchorError: Number(el.getAttribute('data-ribbon-anchor-error')),
				clasp: el.getAttribute('data-clasp-pose'),
				tip: el.getAttribute('data-ribbon-tip'),
				pitch: (JSON.parse(el.getAttribute('data-badge-pose') ?? '[]') as number[])[0] ?? 0,
			})))
		}
		for (const sample of samples) {
			expect(sample.restLength).toBe(restLength)
			expect(Math.abs(sample.length - restLength)).toBeLessThan(restLength * .0003)
			expect(sample.stretch).toBeLessThan(.001)
			// Vertices over the card's footprint stay behind its back (-.264).
			// The independent free sections can otherwise retain their inertia.
			expect(sample.frontZ).toBeLessThan(-.30)
			expect(sample.anchorError).toBeLessThan(.00001)
		}
		expect(new Set(samples.map((sample) => sample.clasp)).size).toBeGreaterThan(1)
		expect(new Set(samples.map((sample) => sample.tip)).size).toBeGreaterThan(1)
		expect(Math.max(...samples.map((sample) => sample.pitch)) - Math.min(...samples.map((sample) => sample.pitch))).toBeGreaterThan(.01)
		await page.screenshot({ path: info.outputPath(`loop-floating-${viewport.width}.png`) })
	}
	expect(errors).toEqual([])
})

test('the employee badge rises inside a fixed canvas without overshooting or bouncing back down', async ({ page }) => {
	await page.goto(`${base}/`)
	const scene = page.getByRole('region', { name: 'Your employee ID', exact: true })
	await expect(scene).toHaveAttribute('data-renderer', 'webgl', { timeout: 30_000 })
	const canvas = page.locator('.badge-canvas')
	const before = await canvas.boundingBox()
	const offsets = await scene.evaluate((el) => new Promise<number[]>((resolve) => {
		const values: number[] = []
		const sample = () => {
			values.push(Number(el.getAttribute('data-badge-arrival')))
			if (el.getAttribute('data-badge-arrived') === 'true') resolve(values)
			else requestAnimationFrame(sample)
		}
		sample()
	}))
	for (let i = 0; i < offsets.length; i++) {
		expect(offsets[i]!).toBeLessThanOrEqual(0)
		if (i > 0) expect(offsets[i]!).toBeGreaterThanOrEqual(offsets[i - 1]!)
	}
	expect(offsets.at(-1)).toBe(0)
	expect(await canvas.boundingBox()).toEqual(before)
	expect(await page.locator('.badge-arrival').evaluate((el) => getComputedStyle(el).transform)).toBe('none')
	await expect(page.getByRole('article', { name: 'Employee ID for Erin Chen', exact: true })).toBeVisible()
})

test('the rising badge pulls a trailing ribbon that coasts upward after the card stops', () => {
	const material = new MeshBasicMaterial(), matrix = new Matrix4()
	const a = new Vector3(), b = new Vector3(), join = new Vector3()
	for (const seed of [1, 2, 17]) {
		const ribbon = new WovenRibbon(material, material, seed, { trailing: true, liftSeconds: BADGE_ARRIVAL_SECONDS })
		const heights: number[] = []
		for (let frame = 0; frame <= 180; frame++) {
			const time = frame / 30, offset = badgeArrival(time, 8.3)
			matrix.makeScale(.84, .84, .84).setPosition(0, offset, 0)
			a.set(0, 1.53, -.362).applyMatrix4(matrix); b.set(0, 1.53, -.397).applyMatrix4(matrix)
			join.set(0, 1.34, -.382).applyMatrix4(matrix)
			const state = ribbon.update(time, a, b, matrix, false, join)
			expect(Math.abs(state.length - 21.30)).toBeLessThan(.0001)
			expect(state.anchorError).toBeLessThan(1e-7)
			expect(state.frontZ).toBeLessThan(-.30)
			if (frame === 0) {
				expect(state.tip.y).toBeLessThan(a.y - 3)
				const positions = ribbon.geometry.getAttribute('position')
				for (let i = 0; i < positions.count; i++) expect(positions.getY(i)).toBeLessThan(-4)
			}
			if (frame >= 72) expect(offset).toBe(0)
			heights.push(state.tip.y)
		}
		// The mount has stopped, but the same free material point continues
		// upward and gradually slows; there is no second rise of the card.
		expect(heights[102]! - heights[72]!).toBeGreaterThan(1)
		expect(heights[162]! - heights[132]!).toBeLessThan(heights[102]! - heights[72]!)
		ribbon.geometry.dispose()
	}
	material.dispose()
})

test('the employee badge stays still when reduced motion is requested', async ({ page }, info) => {
	await page.emulateMedia({ reducedMotion: 'reduce' })
	await page.goto(`${base}/`)
	const scene = page.getByRole('region', { name: 'Your employee ID', exact: true })
	await expect(scene).toHaveAttribute('data-renderer', 'webgl', { timeout: 30_000 })
	await expect(page.getByRole('article', { name: 'Employee ID for Erin Chen', exact: true })).toBeVisible()
	const stage = await page.locator('.badge-scene').boundingBox()
	if (stage === null) throw new Error('the badge scene has no size')
	const look = async () => scene.getAttribute('data-badge-pose')
	const initial = await look()
	await page.mouse.move(stage.x + stage.width * .95, stage.y + stage.height * .5)
	expect(await look()).toBe(initial)
	expect(await page.locator('.badge-arrival').evaluate((el) => getComputedStyle(el).animationName)).toBe('none')
	await page.screenshot({ path: info.outputPath('badge-still.png') })
})

test('the ribbon is a closed smooth band, holds a heading and keeps its length through clasp turns', () => {
	const material = new MeshBasicMaterial()
	const scale = new Vector3(.84, .84, .84)
	const matrix = new Matrix4().makeScale(.84, .84, .84)
	const rotation = new Quaternion()
	const a = new Vector3(), b = new Vector3()
	const join = new Vector3()
	const closedBand = (ribbon: WovenRibbon) => {
		const positions = ribbon.geometry.getAttribute('position')
		const welded = new Map<string, number>()
		const ids = Array.from({ length: positions.count }, (_, i) => {
			const key = `${positions.getX(i)},${positions.getY(i)},${positions.getZ(i)}`
			if (!welded.has(key)) welded.set(key, welded.size)
			return welded.get(key)!
		})
		const indices = ribbon.geometry.getIndex()!
		const edges = new Map<string, number>()
		for (let i = 0; i < indices.count; i += 3) for (let k = 0; k < 3; k++) {
			const x = ids[indices.getX(i + k)]!, y = ids[indices.getX(i + (k + 1) % 3)]!
			const key = x < y ? `${x}:${y}` : `${y}:${x}`
			edges.set(key, (edges.get(key) ?? 0) + 1)
		}
		// Every edge belongs to two faces, including the return through the
		// clasp and the UV seam: there are no open strip ends to hide.
		expect([...edges.values()].every((count) => count === 2)).toBe(true)
	}
	for (const seed of [1, 3, 17, 51]) {
		const ribbon = new WovenRibbon(material, material, seed)
		matrix.makeScale(.84, .84, .84)
		a.set(0, 1.53, -.362).applyMatrix4(matrix); b.set(0, 1.53, -.397).applyMatrix4(matrix)
		join.set(0, 1.34, -.382).applyMatrix4(matrix)
		ribbon.update(0, a, b, matrix, false, join)
		closedBand(ribbon)
		const positions = ribbon.geometry.getAttribute('position')
		const points = Array.from({ length: ribbon.freeSegments + 1 }, (_, i) => {
			const point = new Vector3()
			for (let k = 0; k < 8; k++) point.add(new Vector3().fromBufferAttribute(positions, i * 8 + k))
			return point.multiplyScalar(1 / 8)
		})
		for (let i = 8; i < ribbon.freeSegments - 8; i++) {
			const incoming = points[i]!.clone().sub(points[i - 1]!)
			const outgoing = points[i + 1]!.clone().sub(points[i]!)
			expect(incoming.angleTo(outgoing)).toBeLessThan(.14)
		}
		const uv = Array.from(ribbon.geometry.getAttribute('uv').array)
		let heading: Vector3 | undefined
		for (let i = 1; i <= 420; i++) {
			const t = i / 30
			rotation.setFromEuler(new Euler(Math.sin(t) * .1, i > 50 && i < 100 ? Math.sin(t * 3) * .35 : 0, 0))
			matrix.compose(new Vector3(), rotation, scale)
			a.set(0, 1.53, -.362).applyMatrix4(matrix); b.set(0, 1.53, -.397).applyMatrix4(matrix)
			join.set(0, 1.34, -.382).applyMatrix4(matrix)
			const state = ribbon.update(t, a, b, matrix, false, join)
			if (heading === undefined) heading = state.wind.clone().normalize()
			if (i <= 360) expect(heading.distanceTo(state.wind.clone().normalize())).toBeLessThan(.00001)
			expect(state.restLength).toBe(21.30)
			expect(state.maxStretch).toBeLessThan(.001)
			expect(Math.abs(state.length - state.restLength)).toBeLessThan(state.restLength * .0003)
			expect(state.anchorError).toBeLessThan(1e-7)
			expect(state.frontZ).toBeLessThan(-.30)
		}
		closedBand(ribbon)
		expect(Array.from(ribbon.geometry.getAttribute('uv').array)).toEqual(uv)
		ribbon.geometry.dispose()
	}
	material.dispose()
})

test('repeated gaze and scroll motion does not wind the ribbon into a corkscrew', () => {
	test.setTimeout(120_000)
	const material = new MeshBasicMaterial()
	const matrix = new Matrix4(), rotation = new Quaternion(), scale = new Vector3(.84, .84, .84)
	const a = new Vector3(), b = new Vector3(), join = new Vector3(), translation = new Vector3()
	const tangent = new Vector3(), lastTangent = new Vector3(), across = new Vector3(), previous = new Vector3(), edge = new Vector3(), cross = new Vector3()
	const transport = new Quaternion()
	for (const seed of [2, 51]) {
		const ribbon = new WovenRibbon(material, material, seed, { trailing: true, liftSeconds: BADGE_ARRIVAL_SECONDS })
		const positions = ribbon.geometry.getAttribute('position')
		const centers = Array.from({ length: ribbon.freeSegments + 1 }, () => new Vector3())
		for (let frame = 0; frame <= 2700; frame++) {
			const t = frame / 30
			rotation.setFromEuler(new Euler(.15 * Math.sin(.73 * t), .44 * Math.sin(1.13 * t), 0))
			translation.set(.45 * Math.sin(.43 * t), t < 2.4 ? -8.3 * (1 - t / 2.4) ** 3 : 7 * (1 - Math.cos((t - 2.4) * .17)), 0)
			matrix.compose(translation, rotation, scale)
			a.set(0, 1.53, -.362).applyMatrix4(matrix); b.set(0, 1.53, -.397).applyMatrix4(matrix); join.set(0, 1.34, -.382).applyMatrix4(matrix)
			ribbon.update(t, a, b, matrix, false, join)
			if (frame % 15 !== 0) continue
			for (let i = 0; i < centers.length; i++) {
				let x = 0, y = 0, z = 0
				for (let k = 0; k < 8; k++) { x += positions.getX(i * 8 + k); y += positions.getY(i * 8 + k); z += positions.getZ(i * 8 + k) }
				centers[i]!.set(x / 8, y / 8, z / 8)
			}
			let winding = 0
			for (let i = 2; i < ribbon.freeSegments - 1; i++) {
				tangent.subVectors(centers[i + 1]!, centers[i - 1]!).normalize()
				across.fromBufferAttribute(positions, i * 8).sub(edge.fromBufferAttribute(positions, i * 8 + 1)).normalize()
				if (i > 2) {
					// Transport the actual mesh edge through the centreline bend:
					// curvature is allowed, accumulated material corkscrews are not.
					transport.setFromUnitVectors(lastTangent, tangent)
					previous.applyQuaternion(transport)
					winding += Math.abs(Math.atan2(tangent.dot(cross.crossVectors(previous, across)), previous.dot(across)))
				}
				previous.copy(across); lastTangent.copy(tangent)
			}
			expect(winding / (2 * Math.PI), `seed ${seed}, t=${t}: material turns`).toBeLessThan(.5)
		}
		ribbon.geometry.dispose()
	}
	material.dispose()
})

test('a clipped band floats without rapidly spinning or turning edge-on', () => {
	const material = new MeshBasicMaterial()
	const matrix = new Matrix4().makeScale(.84, .84, .84)
	const a = new Vector3(0, 1.53, -.362).applyMatrix4(matrix)
	const b = new Vector3(0, 1.53, -.397).applyMatrix4(matrix)
	const join = new Vector3(0, 1.34, -.382).applyMatrix4(matrix)
	for (const seed of [1, 2, 17, 51]) {
		const ribbon = new WovenRibbon(material, material, seed)
		const positions = ribbon.geometry.getAttribute('position')
		const normal = new Vector3(), previous = new Vector3(), cross = new Vector3()
		const centers = Array.from({ length: positions.count / 8 }, () => new Vector3())
		for (let frame = 0; frame <= 600; frame++) {
			ribbon.update(frame / 30, a, b, matrix, false, join)
			for (let i = 0; i < centers.length; i++) {
				let x = 0, y = 0, z = 0
				for (let k = 0; k < 8; k++) { x += positions.getX(i * 8 + k); y += positions.getY(i * 8 + k); z += positions.getZ(i * 8 + k) }
				centers[i]!.set(x / 8, y / 8, z / 8)
			}
			normal.set(0, 0, 0)
			for (let i = 1; i < centers.length; i++) normal.add(cross.crossVectors(centers[i - 1]!, centers[i]!))
			normal.normalize()
			if (frame > 150) {
				// Measure the actual rendered band's plane, not a damping setting.
				expect(normal.angleTo(previous) * 30).toBeLessThan(.15)
				expect(Math.abs(normal.z)).toBeGreaterThan(.8)
			}
			previous.copy(normal)
		}
		ribbon.geometry.dispose()
	}
	material.dispose()
})

test('tight bends keep continuous faces without folded or inverted quads', () => {
	const material = new MeshBasicMaterial()
	const matrix = new Matrix4(), rotation = new Quaternion(), scale = new Vector3(.84, .84, .84)
	const a = new Vector3(), b = new Vector3(), join = new Vector3()
	const p = new Vector3(), p1 = new Vector3(), p2 = new Vector3(), p3 = new Vector3()
	const edge = new Vector3(), width = new Vector3(), face = new Vector3(), normal = new Vector3(), nextNormal = new Vector3()
	for (const seed of [1, 2, 17]) {
		const ribbon = new WovenRibbon(material, material, seed)
		const positions = ribbon.geometry.getAttribute('position'), normals = ribbon.geometry.getAttribute('normal')
		for (let frame = 0; frame <= 300; frame++) {
			const t = frame / 30
			rotation.setFromEuler(new Euler(Math.sin(t) * .1, frame > 50 && frame < 100 ? Math.sin(t * 3) * .35 : 0, 0))
			matrix.compose(new Vector3(), rotation, scale)
			a.set(0, 1.53, -.362).applyMatrix4(matrix); b.set(0, 1.53, -.397).applyMatrix4(matrix); join.set(0, 1.34, -.382).applyMatrix4(matrix)
			const state = ribbon.update(t, a, b, matrix, false, join)
			expect(Math.abs(state.length - 21.30)).toBeLessThan(.0001)
			if (frame % 15 !== 0) continue
			let leastAgreement = 1, largestNormalStep = 0
			for (let i = 2; i < ribbon.freeSegments - 2; i++) {
				p.fromBufferAttribute(positions, i * 8); p1.fromBufferAttribute(positions, (i + 1) * 8)
				p2.fromBufferAttribute(positions, i * 8 + 1); p3.fromBufferAttribute(positions, (i + 1) * 8 + 1)
				normal.fromBufferAttribute(normals, i * 8); nextNormal.fromBufferAttribute(normals, (i + 1) * 8)
				face.crossVectors(edge.subVectors(p1, p), width.subVectors(p2, p)).normalize()
				leastAgreement = Math.min(leastAgreement, face.dot(normal))
				face.crossVectors(edge.subVectors(p1, p2), width.subVectors(p3, p2)).normalize()
				leastAgreement = Math.min(leastAgreement, face.dot(nextNormal))
				largestNormalStep = Math.max(largestNormalStep, normal.angleTo(nextNormal))
			}
			// Check the actual triangle winding and adjacent surface directions,
			// including the necks beside the clip, rather than just the curve API.
			expect(leastAgreement).toBeGreaterThan(.5)
			expect(largestNormalStep).toBeLessThan(.20)
		}
		ribbon.geometry.dispose()
	}
	material.dispose()
})
