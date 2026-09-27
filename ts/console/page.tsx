/**
 * The admin console: what an operator runs a deployment with.
 *
 * It is the **control plane**, which is a different set of rows from every
 * other port this app serves. Here a `Holder` is not a person a product app
 * signs in — those are customers' people, in the other database — it is
 * somebody who runs this deployment, or something that calls it.
 *
 * And a `Holder` is **not** a person or a machine. Nothing in the schema says
 * which, and nothing should: it is somebody or something registered here that
 * may exercise a permission, and how it proves itself is a separate row beside
 * it. A person with a `rt_` key holds both, which is exactly what that key is
 * for.
 *
 * # Why it is shaped like the user console
 *
 * Because a customer's screens are the same screens. `ts/lib/tenant/` has held
 * them since #34 and both pages drew them; what differed was **where they
 * appeared** — the user console put them in the sidebar, and this page nested
 * them under a row of the tenants table, six buttons wide, one open at a time.
 *
 * Which was the wrong shape: they are a whole console's worth of screens, and
 * they were being drawn as a disclosure. Somebody who wanted `access` after
 * `people` had to come back out to the table and find the row again. So the
 * sidebar is the sidebar in both pages now, and the one thing this page has that
 * the other cannot is the **selection**: pick a tenant, and the screens under it
 * are that one's.
 *
 * `ts/lib/console.tsx` is the frame, shared, which is the half of this that
 * matters beyond the layout: a change to the chrome is a change to both pages,
 * and there is no version of *I fixed it in one of them* left.
 *
 * # Why the selection is not a filter
 *
 * The screens read `admin.http`, the third listener, where there is **no wall**
 * — so what scopes a read to one tenant is the `tenant` filter the component
 * puts in the query. That is `tenants.tsx`'s own argument and it is unchanged:
 * an operator has no tenant in that database, so there is nothing for a wall to
 * narrow them to and the selection is the whole of the narrowing.
 *
 * Which is why the alias is in the **address** — `/tenants/@contoso/people` —
 * rather than held only here. A person reads it, a reload keeps it, and the back
 * button leaves the tenant.
 *
 * @module
 */

import { useState } from 'react'
import type { Transport } from '@connectrpc/connect'

import { useQuery } from '@lesomnus/payday/react'
import type { App } from '@lesomnus/payday/react'

import { covers } from '../lib/covers.js'
import { go, useRoute } from '../lib/route.js'
import { Broken, Console, Loading, You, type Tab } from '../lib/console.js'

import { MeService } from '../gen/app/me_pb.js'

import type { Writes } from '../lib/client.js'
import { Tenants, Chosen, type Screen as Of } from './tenants.js'

/**
 * Screen is a tab in the sidebar: a customer's, or one of the deployment's own.
 *
 * `Of` is `tenants.tsx`'s list of the ones that are a tenant's, imported
 * rather than written again -- so adding a screen there and forgetting it here
 * does not compile.
 */
type Screen = Of | 'tenants' | 'you'

/** under is the screens that need a tenant to be about. */
const under: readonly Of[] = [
	'holders',
	'hosts',
	'connections',
	'maildomains',
	'sites',
	'groups',
	'roles',
	'trail',
	'settings',
]

function isUnder(v: Screen): v is Of {
	return (under as readonly string[]).includes(v)
}

/**
 * where reads the sidebar's state off the path.
 *
 * Three shapes, because two of the tabs are not about a customer:
 *
 *	/tenants                        the picker
 *	/you                            the deployment's own
 *	/tenants/@<alias>/<screen>      one tenant's, and `who` under `people`
 *
 * A path naming a tenant and no screen is the picker with that one selected,
 * which is what somebody who clicked a row and then the back button lands on. A screen name this build does not have is somebody's stale bookmark,
 * and the picker is the honest answer to one.
 */
function where(route: string[]): { at: Screen; alias: string | null; who: string | null } {
	const none = { at: 'tenants' as Screen, alias: null, who: null }

	if (route[0] === 'you') return { at: 'you', alias: null, who: null }
	if (route[0] !== 'tenants') return none

	const named = route[1] ?? ''
	if (!named.startsWith('@')) return none

	const alias = named.slice(1)
	if (alias === '') return none

	const on = (route[2] ?? '') as Screen
	if (!isUnder(on)) return { at: 'tenants', alias, who: null }

	return { at: on, alias, who: route[3] ?? null }
}

export function Page(props: {
	onSignOut: () => void

	// The tenants screen's store, on the admin listener. Null where there is no
	// such listener -- the sandbox -- and the screen is not offered.
	tenants: App | null

	// And the clients for the writes that screen makes, which do not go through
	// the store: a reset answers with a secret rather than with a row.
	writes: Writes | null

	// The data plane with no wall, for that screen's devtools panel; only the
	// sandbox has one to hand (`main.tsx`, `ungatedTransports`).
	ungated?: Transport | undefined
}): React.ReactNode {
	const me = useQuery(MeService.method.get, {})
	const route = useRoute()
	const at = where(route)

	// The tenant last selected, so the six tabs stay reachable from `you` and from
	// the picker.
	//
	// A cache and not state, in `login/at.go`'s sense: every value in it can be
	// recomputed from the address bar, and dropping it costs a click. What it
	// buys is that clicking `you` and coming back does not lose the tenant, which
	// is the one thing a sidebar has to get right.
	const [last, setLast] = useState<string | null>(null)
	const alias = at.alias ?? last
	if (at.alias !== null && at.alias !== last) setLast(at.alias)

	if (me.state === 'pending') return <Loading />
	if (me.state === 'error') return <Broken at={me.error} onSignOut={props.onSignOut} />

	const held = me.data?.methods ?? []
	const may = (method: string): boolean => held.some((v) => covers(v, method))

	// What is worth drawing, and never what is allowed. The server refuses
	// either way, and a client that treated this as the decision would be one an
	// altered client could talk out of.
	//
	// The nine in the middle also need a tenant to be about, so they are disabled
	// until one is picked -- which is the same `ok: false` a missing
	// permission gets, and reads the same way: the screen exists and you cannot
	// open it yet.
	const picked = alias !== null
	const tabs: Tab<Screen>[] = [
		// The data plane with no wall, which is this listener: the page, the
		// sign-in and these rows are one host (#27, #32).
		{ at: 'tenants', name: 'tenants', ok: may('/roster.TenantService/List') },

		// Each named for the rows it lists, which is why there are nine of them
		// rather than five: *arrives through* was four entities in one screen and
		// *organisation* was two. `docs/glossary.md` § *A word for prose is not a
		// name*.
		{ at: 'holders', name: 'holders', ok: picked && may('/roster.HolderService/List'), group: true },
		{ at: 'hosts', name: 'hosts', ok: picked && may('/roster.HostService/List') },
		{ at: 'connections', name: 'connections', ok: picked && may('/roster.ConnectionService/List') },
		{ at: 'maildomains', name: 'mail domains', ok: picked && may('/roster.MailDomainService/List') },
		{ at: 'sites', name: 'sites', ok: picked && may('/roster.SiteService/List') },
		{ at: 'groups', name: 'groups', ok: picked && may('/roster.GroupService/List') },
		{ at: 'roles', name: 'roles', ok: picked && may('/roster.RoleService/List') },
		{ at: 'trail', name: 'trail', ok: picked && may('/roster.AuditService/List') },
		{ at: 'settings', name: 'settings', ok: picked && may('/roster.TenantService/Update') },

		{ at: 'you', name: 'you', ok: true, group: true },
	]

	// The heading says which tenant is being looked at, because nine of the eleven
	// tabs are about one and a page that said `roster` over them would be a page
	// somebody operates on the wrong one from.
	const title = isUnder(at.at) && alias !== null ? alias : 'roster'

	return (
		<Console
			title={title}
			tabs={tabs}
			at={at.at}
			onGo={(to) => {
				if (to === 'you' || to === 'tenants') return go([to])
				if (alias === null) return go(['tenants'])

				return go(['tenants', '@' + alias, to])
			}}
			who={me.data?.alias ?? ''}
			onSignOut={props.onSignOut}
		>
			{at.at === 'tenants' && (
				<Tenants
					app={props.tenants}
					writes={props.writes}
					may={may}
					at={alias}
					onOpen={(who) => go(who === null ? ['tenants'] : ['tenants', '@' + who, 'holders'])}
					{...(props.ungated !== undefined ? { ungated: props.ungated } : {})}
				/>
			)}

			{isUnder(at.at) && alias !== null && (
				<Chosen
					app={props.tenants}
					writes={props.writes}
					may={may}
					alias={alias}
					at={at.at}
					who={at.who}
					onOpen={(w) =>
						go(
							w === null
								? ['tenants', '@' + alias, 'holders']
								: ['tenants', '@' + alias, 'holders', w],
						)
					}
					{...(props.ungated !== undefined ? { ungated: props.ungated } : {})}
				/>
			)}

			{at.at === 'you' && <You methods={held} />}
		</Console>
	)
}
