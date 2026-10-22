/**
 * A page's mirrors on the browser's disk, read from inside the page, since
 * IndexedDB is the page's origin's and nobody else's.
 *
 * What these hand `page.evaluate` runs in the page, and this file is compiled
 * for node, which has no IndexedDB: the declaration below is as much of it as
 * they use.
 *
 * @module
 */

import type { Page } from '@playwright/test'

interface Req<T> {
	readonly result: T
	onsuccess: (() => void) | null
	onerror: (() => void) | null
}

declare const indexedDB: {
	databases(): Promise<{ name?: string }[]>
	open(
		name: string,
		version?: number,
	): Req<{
		readonly objectStoreNames: ArrayLike<string>
		transaction(names: string[], mode: 'readonly'): { objectStore(name: string): { count(): Req<number> } }
		close(): void
	}>
}

/** mirrors is the mirrors of one of the pages' stores on this browser: `roster`, unless said. */
export async function mirrors(page: Page, store = 'roster'): Promise<string[]> {
	return page.evaluate(
		async (prefix) => (await indexedDB.databases()).map((d) => d.name ?? '').filter((n) => n.startsWith(prefix)),
		`payday/${store}:`,
	)
}

/**
 * kept is how much of its caller's one mirror holds: every table of it but
 * `meta`, the stamp payday keeps of what the rows were written against. Asked
 * only of a mirror that is there, since asking would make one.
 */
export async function kept(page: Page, mirror: string): Promise<number> {
	return page.evaluate(async (name) => {
		const done = <T>(req: Req<T>): Promise<T> =>
			new Promise((ok, no) => {
				req.onsuccess = () => ok(req.result)
				req.onerror = () => no(new Error(`cannot read ${name}`))
			})

		const db = await done(indexedDB.open(name))
		try {
			const tables = Array.from(db.objectStoreNames).filter((t) => t !== 'meta')
			const tx = db.transaction(tables, 'readonly')
			const counts = await Promise.all(tables.map((t) => done(tx.objectStore(t).count())))

			return counts.reduce((a, b) => a + b, 0)
		} finally {
			db.close()
		}
	}, mirror)
}

/**
 * movedOn opens a mirror at a later version, as a later deploy's page would.
 * A page holding it lets go of it rather than hold that deploy up, so nothing
 * it is told to forget afterwards reaches the disk -- which is how a sign-out
 * comes to leave somebody's data on a browser, made to happen.
 */
export async function movedOn(page: Page, mirror: string): Promise<void> {
	await page.evaluate(
		(name) =>
			new Promise<void>((ok, no) => {
				const r = indexedDB.open(name, 99)
				r.onsuccess = () => {
					r.result.close()
					ok()
				}
				r.onerror = () => no(new Error(`cannot open ${name}`))
			}),
		mirror,
	)
}
