/**
 * Whether this build has a devtools panel at all.
 *
 * **The panel itself is payday's** -- `@lesomnus/payday/react/devtools`, a
 * bottom sheet that reads any entity's rows beside what the store holds. It is
 * reached through `panel.tsx`, which is the forty lines that hand it this app's
 * entities, the unwalled transport where there is one, and an editor. Nothing
 * about the panel is written here, and the name of this file is the one thing
 * that suggests otherwise: what a call site imports as `Devtools` is this
 * decision, not that component.
 *
 * The panel is a development tool and Monaco behind it is four megabytes, so
 * neither belongs in what a deployment serves. Gating the **element** is not
 * enough for that: an element that is never rendered is still a module that was
 * imported, and the bundler emits the chunk either way.
 *
 * So the import is inside the branch. `import.meta.env.DEV` is a constant the
 * bundler substitutes, which makes this `null` in a production build and the
 * `import()` beside it unreachable -- and unreachable is the only thing that
 * actually removes a chunk.
 *
 * What it is worth, measured on `npm run build` with the branch taken out --
 * `ts/dist/console/`, the module the sandbox serves excluded:
 *
 *	gated      551,331
 *	ungated 14,808,690
 *
 * The **entry** is 478.5 kB either way, which is the part worth being clear
 * about: the import is already dynamic, so the first paint was never at risk.
 * What the branch removes is fourteen megabytes of lazy chunks -- Monaco, its
 * language workers, `ts.worker` alone seven of them -- that ship in the image
 * and that nothing ever fetches. `npm run build` is where to check it: a
 * `monaco` chunk in `ts/dist/console/` means this stopped working.
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
