/**
 * The editor the devtools panel takes, fetched rather than imported.
 *
 * Monaco is four megabytes and the first screen of the panel is a table.
 * Imported at the top of a module it lands in the entry chunk, so the console
 * itself would not render until all of it had arrived -- and signing in, which
 * is what happens first, needs none of it. Asked for beside the page, the two
 * are independent: the console is on the screen and usable while the editor is
 * still coming, and the panel is whole without one. A `Get` opened before it
 * lands is the same document, read-only, and becomes editable when it lands.
 *
 * It is only ever fetched in a development build, because that is the only
 * build that mounts the panel; see `main.tsx`.
 *
 * @module
 */

import { useEffect, useState } from 'react'

import type { MonacoLike } from '@lesomnus/payday/react/devtools'

/** editing is monaco, fetched once, for as long as this page is open. */
let editing: Promise<MonacoLike> | undefined

/**
 * editor is the four things payday asks of monaco.
 *
 * `MonacoEnvironment` is set in here for the same reason it exists at all: the
 * URL of a bundled worker is something only the bundler that produced it knows,
 * and `?worker` is vite's way of saying so. It has to be set before monaco asks
 * for a worker, and beside the import that will do the asking is the only
 * ordering that guarantees it. Without the JSON one there is no completion and
 * no validation -- the language service is that worker -- and the editor still
 * edits, which is the confusing half of getting it wrong.
 *
 * Module state and not component state: StrictMode runs an effect twice on
 * purpose, and two of these is two copies of four megabytes.
 */
function editor(): Promise<MonacoLike> {
	editing ??= (async () => {
		// The worker paths are the package's own subpath map (`./*` ->
		// `./esm/vs/*.js`), not the directory they land in: spelling the
		// directory gets `esm/vs/esm/vs/...` and a module that is not there.
		const [monaco, json, plain] = await Promise.all([
			import('monaco-editor'),
			import('monaco-editor/language/json/json.worker?worker'),
			import('monaco-editor/editor/editor.worker?worker'),
		])

		;(self as unknown as { MonacoEnvironment: unknown }).MonacoEnvironment = {
			getWorker(_id: string, label: string) {
				return label === 'json' ? new json.default() : new plain.default()
			},
		}

		return { editor: monaco.editor, Uri: monaco.Uri, json: monaco.json.jsonDefaults }
	})()

	return editing
}

/**
 * useMonaco is the editor once it has arrived, and `undefined` until then.
 *
 * Nothing waits on it. Spread into the panel as an optional prop, an
 * `undefined` is the panel without an editor, which is a panel.
 */
export function useMonaco(): MonacoLike | undefined {
	const [v, setV] = useState<MonacoLike>()

	useEffect(() => {
		let alive = true
		editor().then(
			(m) => {
				if (alive) setV(m)
			},
			(e: unknown) => console.error('monaco:', e),
		)

		return () => {
			alive = false
		}
	}, [])

	return v
}
