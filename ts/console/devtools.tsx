/**
 * Whether this build has a devtools panel at all.
 *
 * The panel is a development tool and Monaco behind it is four megabytes, so
 * neither belongs in what a deployment serves. Gating the **element** is not
 * enough for that: an element that is never rendered is still a module that was
 * imported, and the bundler emits the chunk either way.
 *
 * So the import is inside the branch. `import.meta.env.DEV` is a constant the
 * bundler substitutes, which makes this `null` in a production build and the
 * `import()` beside it unreachable -- and unreachable is the only thing that
 * actually removes a chunk. `npm run build` is where to check that: a `monaco`
 * chunk in `ts/dist/console/` means this stopped working.
 *
 * @module
 */

import { Suspense, lazy } from 'react'

import type { Transport } from '@connectrpc/connect'

const Panel = import.meta.env.DEV ? lazy(() => import('./panel.js')) : null

/** Devtools is the panel where there is one, and nothing where there is not. */
export function Devtools(props: { ungated?: Transport | undefined }): React.ReactNode {
	if (Panel === null) return null

	return (
		<Suspense fallback={null}>
			<Panel {...props} />
		</Suspense>
	)
}
