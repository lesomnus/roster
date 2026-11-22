import { expect, test } from '@playwright/test'

declare const document: { documentElement: { scrollWidth: number; clientWidth: number } }

for (const app of [
	{ name: 'admin', base: process.env['E2E_CONSOLE'], who: process.env['E2E_OPS_USER'] ?? 'admin', password: process.env['E2E_OPS_PASSWORD'] ?? '', add: 'stand one up' },
	{ name: 'tenant', base: process.env['E2E_USER'], who: 'admin', password: process.env['E2E_TENANT_ADMIN_PASSWORD'] ?? '', add: 'add somebody' },
]) {
	test(`${app.name} workspace fits a phone, follows the system theme and restores drawer focus`, async ({ page }, info) => {
		test.skip(app.base === undefined, 'the deployment is not running')
		const errors: string[] = []
		page.on('pageerror', error => errors.push(error.message))
		await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
		await page.goto(app.base!)
		await page.locator('input[name=alias]').fill(app.who)
		await page.locator('input[name=password]').fill(app.password)
		await page.getByRole('button', { name: 'sign in', exact: true }).click()
		await expect(page.locator('nav .who')).toHaveText(app.who)
		await expect(page.getByRole('button', { name: 'sign out', exact: true })).toBeInViewport()
		await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'light')
		await page.screenshot({ animations: 'disabled', path: info.outputPath(`${app.name}-desktop.png`) })
		await page.setViewportSize({ width: 390, height: 844 })
		const add = page.getByRole('button', { name: app.add, exact: true })
		await add.click()
		const dialog = page.getByRole('dialog')
		await expect(dialog).toBeVisible()
		await expect(dialog.locator('input[name=alias]')).toBeFocused()
		await page.screenshot({ animations: 'disabled', path: info.outputPath(`${app.name}-phone-drawer.png`) })
		await page.keyboard.press('Escape')
		await expect(dialog).toHaveCount(0)
		await expect(add).toBeFocused()
		expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
		await page.emulateMedia({ colorScheme: 'dark' })
		await expect(page.locator('html')).toHaveAttribute('data-ui-theme', 'dark')
		await page.screenshot({ animations: 'disabled', path: info.outputPath(`${app.name}-phone-dark.png`) })
		expect(errors).toEqual([])
	})
}
