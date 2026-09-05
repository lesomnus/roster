import { useSyncExternalStore } from 'react'

/**
 * The address bar is where a page keeps which screen it is on.
 *
 * The console kept it in component state -- which screen, which customer,
 * which panel, which person -- so the history had one entry for the whole
 * visit: the back button left the app, a reload lost the place, and a link
 * to a customer's people could not be sent. This is the least that fixes it:
 * the path under the page's base, as segments, and one function that changes
 * it through `history.pushState` so the back button walks it back.
 *
 * No library, because there is nothing to match: the tree is fixed
 * (`/console/<screen>/<tenant>/<panel>/<person>`) and each screen reads the
 * segment that is its own. The server already answers `index.html` for every
 * path under `/console/` (`cmd/serve.go`), as vite does, so a deep link opens.
 */

const base = import.meta.env.BASE_URL.replace(/\/$/, '')

function segments(): string[] {
	const path = location.pathname.startsWith(base) ? location.pathname.slice(base.length) : location.pathname

	return path.split('/').filter((s) => s !== '')
}

const listeners = new Set<() => void>()

function notify(): void {
	for (const l of listeners) l()
}

let current = segments().join('/')

function read(): string {
	return current
}

function subscribe(l: () => void): () => void {
	listeners.add(l)
	if (listeners.size === 1) window.addEventListener('popstate', onPop)

	return () => {
		listeners.delete(l)
		if (listeners.size === 0) window.removeEventListener('popstate', onPop)
	}
}

function onPop(): void {
	current = segments().join('/')
	notify()
}

/**
 * go changes the path. A push, so the back button undoes it; `replace` for
 * the moves that are not the person's own -- a redirect to the first screen,
 * a sign-out -- which should not be a step back either.
 */
export function go(to: readonly string[], replace = false): void {
	const path = base + '/' + to.map(encodeURIComponent).join('/')
	if (path === location.pathname) return
	if (replace) history.replaceState(null, '', path)
	else history.pushState(null, '', path)
	current = to.join('/')
	notify()
}

/** useRoute is the path as segments, redrawn when it changes. */
export function useRoute(): string[] {
	const v = useSyncExternalStore(subscribe, read, read)

	return v === '' ? [] : v.split('/').map(decodeURIComponent)
}
