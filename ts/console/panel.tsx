/**
 * payday's devtools panel, with an editor if one turns up.
 *
 * A module of its own because it is imported dynamically -- `devtools.tsx`
 * next door is what decides whether to -- so that neither the panel nor the
 * four megabytes of Monaco behind it are in a production build at all. Nothing
 * here is reachable from one.
 *
 * @module
 */

import type { Transport } from '@connectrpc/connect'
import { Devtools } from '@lesomnus/payday/react/devtools'

import { entities } from '../gen/entities.js'
import { useMonaco } from './monaco.js'

/**
 * Panel is the sheet, over whichever store its `Provider` holds.
 *
 * `ungated` is the same rows with no wall, which only the sandbox has: the
 * server is inside the page there, so *past the wall* is the page looking at
 * itself. A served deployment hands over no such transport and the switch is
 * not offered.
 */
export default function Panel(props: { ungated?: Transport | undefined }): React.ReactNode {
	// Not waited on. The panel is whole without an editor -- a `Get` is the
	// same document, read-only -- and becomes editable when this lands.
	const monaco = useMonaco()

	return (
		<Devtools
			entities={entities}
			{...(props.ungated !== undefined ? { ungated: props.ungated } : {})}
			{...(monaco !== undefined ? { monaco } : {})}
		/>
	)
}
