/**
 * The user console: what a **roster user** sees of their own tenant.
 *
 * It is the walled **data plane**, which is a different set of rows from the
 * admin console: here a `Holder` is one of this organisation's own people, and
 * the caller is one of them too. Nothing on this page reaches another tenant,
 * and not because the page declines to ask -- the wall narrows every read to
 * the caller's tenant, in the query, so a page that asked for everything would
 * be answered with theirs.
 *
 * # Why it is not the admin console with a filter
 *
 * Because that would be the wrong caller. The admin listener stands from the
 * **port**: no wall, and the two escalation rules waived
 * (`docs/operating.md` § *What the admin port waives*). A tenant administrator
 * reaching their own rows through it would be the roster operator acting on
 * their behalf. Here they are themselves, and roster narrows them.
 *
 * # Why the screens are the same components
 *
 * `ts/lib/tenant/` is one tenant's rows drawn once. What differs between the two
 * pages is who is calling and which listener answers -- and neither of those is
 * something a table of holders knows about. A second copy of these screens
 * would be a second set of answers to *what may be shown*, and the first thing
 * to drift.
 *
 * # Which tenant
 *
 * The caller's, from `Me.Get`, which takes no subject and so cannot be pointed
 * at anybody else. There is no tenant picker and nothing to pick: the name this
 * page was reached at is what decided which tenant the sign-in was about
 * (`cmd.Hosted`), and by the time anything here renders that is settled.
 *
 * @module
 */

import { useQuery } from '@lesomnus/payday/react'

import { covers } from '../lib/covers.js'
import { go, useRoute } from '../lib/route.js'
import type { Writes } from '../lib/client.js'

import { MeService } from '../gen/app/me_pb.js'
import { TenantService } from '../gen/roster/payday/tenant_svc_pb.js'

import { People } from '../lib/tenant/people.js'
import { Arrives } from '../lib/tenant/arrives.js'
import { Organisation } from '../lib/tenant/organisation.js'
import { Access } from '../lib/tenant/access.js'
import { Trail } from '../lib/tenant/trail.js'

type Screen = 'people' | 'arrives' | 'organisation' | 'access' | 'trail' | 'you'
const screenNames: readonly Screen[] = ['people', 'arrives', 'organisation', 'access', 'trail', 'you']

function screenOf(v: string | undefined): Screen {
	return (screenNames as readonly string[]).includes(v ?? '') ? (v as Screen) : 'people'
}

export function Page(props: { onSignOut: () => void; writes: Writes }): React.ReactNode {
	const me = useQuery(MeService.method.get, {})

	// Which screen is the address bar's to say (`ts/lib/route.ts`): the first
	// segment under the page's base, and the first screen when there is none or
	// it names nothing. `/people/<alias>` and not `/<tenant>/people/<alias>` --
	// there is one tenant here and putting it in the path would be saying it
	// twice, and saying it somewhere a caller could change it.
	const route = useRoute()
	const at = screenOf(route[0])

	if (me.state === 'pending') return <main className="loading">…</main>
	if (me.state === 'error') {
		return (
			<main className="error">
				<p>{me.error instanceof Error ? me.error.message : 'no'}</p>
				<button onClick={props.onSignOut}>sign out</button>
			</main>
		)
	}

	const held = me.data?.methods ?? []
	const may = (method: string): boolean => held.some((v) => covers(v, method))

	return (
		<Screens
			id={me.data?.tenant}
			may={may}
			at={at}
			who={me.data?.alias ?? ''}
			methods={held}
			writes={props.writes}
			onSignOut={props.onSignOut}
			people={route[1] ?? null}
		/>
	)
}

/**
 * Screens is the page once we know which tenant, which is one read away.
 *
 * `Me.Get` answers the identifier and not the alias, which is right -- it
 * answers about the caller, and the tenant's own name is the tenant's row. So
 * it is read, through the wall, which can only ever answer with this one.
 *
 * A caller whose role does not cover `Tenant.Get` still gets the page: the
 * heading falls back to `roster` and every screen below is unaffected, because
 * what they need is the identifier. A page that refused to draw without a name
 * would be a page that needs a permission to show a heading.
 */
function Screens(props: {
	id: Uint8Array | undefined
	may: (method: string) => boolean
	at: Screen
	who: string
	methods: string[]
	writes: Writes
	onSignOut: () => void
	people: string | null
}): React.ReactNode {
	const id = props.id
	const v = useQuery(TenantService.method.get, {
		ref: id === undefined ? undefined : { key: { case: 'id', value: id } },
	})

	const tenant = id === undefined ? undefined : { id, alias: v.data?.alias ?? '' }
	const name = v.data?.alias ?? 'roster'

	// What is worth drawing, and never what is allowed. The server refuses
	// either way, and a client that treated this as the decision would be one an
	// altered client could talk out of.
	const screens: { at: Screen; name: string; ok: boolean }[] = [
		{ at: 'people', name: 'people', ok: props.may('/roster.HolderService/List') },
		{ at: 'arrives', name: 'arrives through', ok: props.may('/roster.HostService/List') },
		{ at: 'organisation', name: 'organisation', ok: props.may('/roster.SiteService/List') },
		{ at: 'access', name: 'access', ok: props.may('/roster.RoleService/List') },
		{ at: 'trail', name: 'trail', ok: props.may('/roster.AuditService/List') },
		{ at: 'you', name: 'you', ok: true },
	]

	return (
		<div className="console">
			<nav>
				<h1>{name}</h1>
				{screens.map((s) => (
					<button
						key={s.at}
						disabled={!s.ok}
						className={s.at === props.at ? 'at' : ''}
						onClick={() => go([s.at])}
					>
						{s.name}
					</button>
				))}
				<span className="who">{props.who}</span>
				<button onClick={props.onSignOut}>sign out</button>
			</nav>

			<main>
				{props.at === 'people' && (
					<People
						tenant={tenant}
						writes={props.writes}
						may={props.may}
						at={props.people}
						onOpen={(who) => go(who === null ? ['people'] : ['people', who])}
					/>
				)}
				{props.at === 'arrives' && <Arrives tenant={tenant} may={props.may} />}
				{props.at === 'organisation' && <Organisation tenant={tenant} may={props.may} />}
				{props.at === 'access' && <Access tenant={tenant} may={props.may} />}
				{props.at === 'trail' && <Trail tenant={tenant} />}
				{props.at === 'you' && <You methods={props.methods} />}
			</main>
		</div>
	)
}

/** You: what this person may call, which is the union the server enforces. */
function You(props: { methods: string[] }): React.ReactNode {
	return (
		<section>
			<h2>you</h2>
			<ul className="methods">
				{props.methods.map((m) => (
					<li key={m}>
						<code>{m}</code>
					</li>
				))}
			</ul>
			<p className="note">
				Patterns, not every RPC written out. <code>/roster.*/*</code> is
				everything roster serves, now and after an upgrade — which is why it is
				a pattern and not a list somebody has to keep in step.
			</p>
			<p className="note">
				Narrowed to what this <em>credential</em> may do, not only to what you
				may: a key or a delegation can call less than its holder.
			</p>
		</section>
	)
}
