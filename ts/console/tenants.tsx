/**
 * The tenants screen -- the ones this deployment serves, and one of them
 * selected.
 *
 * Two exports, and the split is the sidebar's: [Tenants] is the picker, and
 * [Chosen] is every screen that is one tenant's. They used to be one thing,
 * with the screens nested under a row of the table behind six buttons and one
 * open at a time. `ts/console/page.tsx` says why that moved.
 *
 * # Why `tenants` and not `customers`
 *
 * The screen was called *customers* and the glossary recorded that rather than
 * arguing it. **A thing that names rows uses the row's name**: the entity is
 * `Tenant`, `roster tenant add` makes one, and the wall narrows to one -- so a
 * screen labelled otherwise was the single place in this product where a list of
 * rows did not say what it was a list of.
 *
 * `customer` is still the right word in **prose**, and the glossary keeps it: a
 * tenant, from the roster operator's side of the table. What it is not is a name
 * for a screen, a route, or a component.
 *
 * What did **not** move is the port, the store, or the narrowing -- see below.
 *
 * # It reads a different port
 *
 * The rest of the admin console is the **control plane** -- who runs the deployment
 * and which services call it. These rows are the **data plane**, which is
 * another database, and an operator has no tenant there: their row is in the
 * control plane, so the wall on `server.http` narrows them to a tenant that does
 * not exist in that database and they see nothing.
 *
 * So this reads `admin.http`, the third listener, which exists for exactly this
 * and says so in `cmd/admin.go`:
 *
 *   Who is calling and what they hold are **control plane** questions.
 *   What they are operating on is the **data plane**.
 *
 * It reads the same session cookie, so there is nothing further to sign in to.
 * What it needs from the deployment is `origins:` under `admin.http`, the same
 * line `control.http` needs, because `npm run dev` is a third origin.
 *
 * # Two stores, and why that is not a workaround
 *
 * A `Store` holds rows keyed by entity, and `roster.Holder` means one thing on
 * the control plane and another on the data plane -- an operator and a
 * customer's person. One store would have them overwrite each other by
 * identifier, which is the shape of a bug rather than a cache.
 *
 * `Provider` is React context, so this screen renders under its own and
 * everything below it reads the data plane without knowing there is another.
 *
 * @module
 */

import { useState } from 'react'
import type { Transport } from '@connectrpc/connect'

import { Provider, useCall, useQuery } from '@lesomnus/payday/react'
import type { App } from '@lesomnus/payday/react'

import type { Tenant } from '#gen/roster/payday/tenant_pb.js'
import { TenantService } from '#gen/roster/payday/tenant_svc_pb.js'

import type { Writes } from '#lib/client.js'
// payday's panel, where this build has one; see `devtools.tsx`.
import { Devtools } from '#lib/devtools.js'
import { entities } from '#gen/entities.js'
import { Holders } from '#lib/tenant/holders.js'
import { Hosts } from '#lib/tenant/hosts.js'
import { Connections } from '#lib/tenant/connections.js'
import { MailDomains } from '#lib/tenant/maildomains.js'
import { Sites } from '#lib/tenant/sites.js'
import { Groups } from '#lib/tenant/groups.js'
import { Roles } from '#lib/tenant/roles.js'
import { Trail } from '#lib/tenant/trail.js'
// Renamed here and nowhere else: this file needs the generated `Tenant` message as
// well as the screen named for it, and the message is the one of the two that is
// this file's own business.
import { Tenant as TenantScreen } from '#lib/tenant/tenant.js'

/**
 * Screen is one of a customer's, as the sidebar names them.
 *
 * The same nine the user console has, every one a `ts/lib/tenant/` component drawn
 * unchanged. `tenant` is the row the other eight are inside, and it is the one the
 * sidebar draws as its head -- so the button that says which tenant is the button
 * that opens it. It was `settings` until it was a screen rather than a form.
 */
export type Screen =
	| 'tenant'
	| 'holders'
	| 'hosts'
	| 'connections'
	| 'maildomains'
	| 'sites'
	| 'groups'
	| 'roles'
	| 'trail'

/** uuid is the bytes an identifier arrives as, written the way a person reads one. */
function uuid(v: Uint8Array | undefined): string {
	if (v === undefined || v.length !== 16) return ''

	const h = [...v].map((b) => b.toString(16).padStart(2, '0')).join('')

	return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

/**
 * bytesOf is the other direction: what somebody typed, as the sixteen bytes the
 * wire carries, or nothing if it is not an identifier at all.
 *
 * It exists for one field -- the identifier a new customer may have to be given
 * -- and the check is the whole of its value: a tenant created with a mangled
 * one is the failure that field exists to prevent, made a different way.
 */
function bytesOf(v: string): Uint8Array | undefined {
	const h = v.replaceAll('-', '')
	if (!/^[0-9a-fA-F]{32}$/.test(h)) return undefined

	const b = new Uint8Array(16)
	for (let i = 0; i < 16; i++) b[i] = Number.parseInt(h.slice(i * 2, i * 2 + 2), 16)

	return b
}

function when(v: { seconds: bigint } | undefined): string {
	if (v === undefined) return ''

	return new Date(Number(v.seconds) * 1000).toISOString().slice(0, 10)
}

/**
 * Tenants is the picker: which tenants this deployment serves, and standing one
 * up.
 *
 * `app` is null while the store is opening, which is one render rather than a
 * state machine: the disk mirror is read before the first query runs, so a
 * spinner here is the only honest thing to draw.
 */
export function Tenants(props: {
	app: App | null
	writes: Writes | null
	may: (method: string) => boolean

	/** Which one is selected, so the table can say so. */
	at: string | null

	/** Selecting one, by alias, or letting go of the selection. */
	onOpen: (alias: string | null) => void

	// The data plane with no wall, for the panel; the sandbox's alone.
	ungated?: Transport | undefined
}): React.ReactNode {
	if (props.app === null || props.writes === null) return <p className="loading">…</p>

	return (
		<Provider app={props.app}>
			<Table writes={props.writes} may={props.may} at={props.at} onOpen={props.onOpen} />
			{/* The same window, on the data plane's store -- the one this screen
			    reads, and the only one mounted while it is showing. */}
			<Devtools {...(props.ungated !== undefined ? { ungated: props.ungated } : {})} />
		</Provider>
	)
}

/**
 * Chosen is one tenant's screens, under the same store the picker reads.
 *
 * The store is why this is a component here rather than six in `page.tsx`:
 * `roster.Holder` means an operator on the control plane and a customer's person
 * on the data plane, so one store would have them overwrite each other by
 * identifier. `Provider` is React context, so everything under this reads the
 * data plane without knowing there is another -- which is the paragraph at the
 * top of this file, and the reason the screens could not simply be mounted beside
 * `you`.
 *
 * It resolves the alias to a row rather than taking one, because the address bar
 * carries the alias and a page reached by reload has no row yet.
 */
export function Chosen(props: {
	app: App | null
	writes: Writes | null
	may: (method: string) => boolean
	alias: string
	at: Screen
	who: string | null
	onOpen: (who: string | null) => void
	ungated?: Transport | undefined
}): React.ReactNode {
	if (props.app === null || props.writes === null) return <p className="loading">…</p>

	return (
		<Provider app={props.app}>
			<Screens
				writes={props.writes}
				may={props.may}
				alias={props.alias}
				at={props.at}
				who={props.who}
				onOpen={props.onOpen}
			/>
			<Devtools {...(props.ungated !== undefined ? { ungated: props.ungated } : {})} />
		</Provider>
	)
}

/** Screens is [Chosen] once it is under the store, so it may read. */
function Screens(props: {
	writes: Writes
	may: (method: string) => boolean
	alias: string
	at: Screen
	who: string | null
	onOpen: (who: string | null) => void
}): React.ReactNode {
	// By alias, which is what the address carries and what `TenantRef` takes -- a
	// tenant is not inside anything, so its reference is the alias itself and not
	// a slug. Through the admin listener like every other read here, so there is
	// no wall and this answers for whichever customer was named.
	const v = useQuery(TenantService.method.get, {
		ref: { key: { case: 'alias', value: props.alias } },
	})

	if (v.state === 'pending') return <p className="loading">…</p>
	if (v.state === 'error') return <Failed at={v.error} />

	// A row rather than an identifier, because that is what `ts/lib/tenant/`
	// takes: it filters by the tenant it was handed, which on this listener is the
	// whole of the narrowing.
	const tenant = v.data

	return (
		<>
			{props.at === 'tenant' && <TenantScreen tenant={tenant} may={props.may} />}
			{props.at === 'holders' && (
				<Holders
					tenant={tenant}
					writes={props.writes}
					may={props.may}
					// Which holder is open is the page's to say, in the page's tree:
					// `/tenants/@<tenant>/holders/<alias>`. The user console's is
					// `/holders/<alias>`, which is why the component takes it rather
					// than reading the route.
					at={props.who}
					onOpen={props.onOpen}
				/>
			)}
			{props.at === 'hosts' && <Hosts tenant={tenant} may={props.may} />}
			{props.at === 'connections' && <Connections tenant={tenant} may={props.may} />}
			{props.at === 'maildomains' && <MailDomains tenant={tenant} may={props.may} />}
			{props.at === 'sites' && <Sites tenant={tenant} may={props.may} />}
			{props.at === 'groups' && <Groups tenant={tenant} may={props.may} />}
			{props.at === 'roles' && <Roles tenant={tenant} may={props.may} />}
			{props.at === 'trail' && <Trail tenant={tenant} />}
		</>
	)
}

/**
 * Table is every tenant, filtered by what somebody typed, and the one that is
 * selected.
 *
 * # The filter is over what was read
 *
 * `TenantService` has no `Search` -- `HolderService` does, and that asymmetry is
 * the schema's rather than this page's -- so what this narrows is the page it
 * already has. A deployment with more tenants than one page holds pages through
 * them with `after`, and the box says *of these* rather than pretending to be a
 * search of the table.
 *
 * Said in the placeholder rather than left to be discovered, because a filter
 * that silently covers the first twenty rows is the shape `login/doctor.go`
 * refused for the same reason.
 */
function Table(props: {
	writes: Writes
	may: (method: string) => boolean
	at: string | null
	onOpen: (alias: string | null) => void
}): React.ReactNode {
	const vs = useQuery(TenantService.method.list, {})
	const [typed, setTyped] = useState('')

	// What this screen made since it read, which is the shape `Keys` already
	// uses one file over: a list query is not revalidated by a write this page
	// made, and a customer that vanished until a reload would read as one that
	// was not created. Merged by identifier, so a store that does pick the row
	// up does not draw it twice.
	const [made, setMade] = useState<Tenant[]>([])

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	const read = vs.data?.items ?? []
	const all = [...read, ...made.filter((v) => !read.some((w) => uuid(w.id) === uuid(v.id)))]

	// Alias and name both, because an operator looking for a tenant knows one or
	// the other and not reliably which. Lowered on both sides: what somebody
	// types into a box is not canonical anything.
	const needle = typed.trim().toLowerCase()
	const items =
		needle === ''
			? all
			: all.filter((v) => v.alias.toLowerCase().includes(needle) || v.name.toLowerCase().includes(needle))

	return (
		<section>
			<h2>tenants</h2>

			<NewTenant
				writes={props.writes}
				may={props.may}
				onMade={(v) => setMade((was) => [...was, v])}
			/>

			{all.length === 0 && <p className="none">none yet</p>}

			{all.length > 0 && (
				<input
					className="wide"
					type="search"
					value={typed}
					onChange={(e) => setTyped(e.target.value)}
					placeholder={`filter these ${all.length} by alias or name`}
					aria-label="filter tenants"
				/>
			)}

			{all.length > 0 && items.length === 0 && <p className="none">none of these match</p>}

			{items.length > 0 && (
				<table>
					<thead>
						<tr>
							<th>tenant</th>
							<th>name</th>
							<th>since</th>
							<th />
						</tr>
					</thead>
					<tbody>
						{items.map((v) => {
							const open = props.at === v.alias

							return (
								<tr key={uuid(v.id)} className={open ? 'at' : ''}>
									<td>{v.alias}</td>
									<td>{v.name}</td>
									<td>{when(v.dateCreated)}</td>
									<td className="acts">
										{/* One button, and the sidebar is where the screens
										    are. Six buttons per row was six ways to open one
										    thing, and somebody who wanted `access` after
										    `people` had to come back out to the table. */}
										<button onClick={() => props.onOpen(open ? null : v.alias)}>
											{open ? 'selected' : 'open'}
										</button>
									</td>
								</tr>
							)
						})}
					</tbody>
				</table>
			)}
		</section>
	)
}

// every is what a customer's first person is given, and it is a **pattern**.
//
// The same one `init` wrote for the first customer and for the same reason: a
// list enumerated the day a customer is created is what existed that day, and
// the next release adds an RPC their administrator cannot call and cannot grant
// themselves either, because granting is refused for anything the granter does
// not already hold.
//
// `/roster.*/*` and not `/*.*/*`, which would take in payday's own --
// `BatchService` is a way of calling the methods this already covers. See
// `cmd/init.go`, which writes the same string.
//
// A line comment and not a doc block, which is not a style choice: `*/` inside
// one ends it, and this string contains one.
const every = '/roster.*/*'

/** The two calls below, so the button can be drawn as what it does. */
const needs = ['/roster.TenantService/Add', '/roster.CredentialService/Issue']

/**
 * stand puts a customer up: the tenant with its administrator, and a password.
 *
 * Two calls, where this was four. `Tenant.Add` writes the tenant, the holder
 * that administers it, the role and the binding in one transaction
 * (`server/core/tenant.go`), and `Credential.Issue` is the ordinary verb that
 * answers a password once, pointed at the person the first call made.
 *
 * The four were argued for once: each is held to the same rules every other
 * write on this port is, and a composite would be a fifth thing to hold them
 * to. That is about a **caller** composing them; the three writes inside `Add`
 * go back through the layer they would have arrived at on their own, so a role
 * wider than the caller holds is refused there exactly as `Role.Add` refuses
 * it.
 *
 * What the four left is what changed the answer: a tenant with nobody who could
 * do anything in it, finishable only by an operator reaching inside through
 * this port -- the one that waives two rules.
 *
 * # The identifier
 *
 * Given when an app served by this roster already has this organisation written
 * down as a constant. The two have to agree from the start, and disagreeing is
 * the failure that fails **silently**: both sides come up, somebody signs in,
 * and the app makes a second tenant because the identifier it was handed is not
 * one it has -- two rows for one organisation, with the rows that belong
 * together split across them and nothing erroring.
 *
 * It was `roster init --tenant-id` and had nowhere to go when `init` stopped
 * making customers. `docs/usage/customers.md` § "Giving it an identifier" carries the warning.
 */
async function stand(
	writes: Writes,
	alias: string,
	name: string,
	who: string,
	id: string,
): Promise<{ tenant: Tenant; said: string }> {
	// Before anything is written, so a typo is a refusal rather than a customer
	// under an identifier nobody meant.
	let given: Uint8Array | undefined
	if (id !== '') {
		given = bytesOf(id)
		if (given === undefined) {
			throw new Error(`${id} is not an identifier, and nothing was created`)
		}
	}

	let tenant: Tenant
	try {
		tenant = await writes.tenant.add(
			given === undefined ? { alias, name } : { alias, name, id: given },
		)
	} catch (e) {
		throw new Error(`${alias} was not created: ${e instanceof Error ? e.message : 'no'}`)
	}

	// The way in, which is the ordinary verb pointed at the person the write
	// above made: `admin` in the tenant, by slug, because a caller that has
	// just made one knows both halves. It answers a password once.
	let secret: string
	try {
		const res = await writes.credential.issue({
			ref: {
				key: {
					case: 'slug',
					value: { alias: who, tenant: { key: { case: 'id', value: tenant.id } } },
				},
			},
		})
		secret = res.secret
	} catch (e) {
		throw new Error(
			`${alias} is up and ${who} has no password yet: ${e instanceof Error ? e.message : 'no'}`,
		)
	}

	return {
		tenant,
		said:
			`${alias} is up, and ${who} holds ${every} in it. ` +
			`Their password is ${secret} — it is shown once, and they should change it.`,
	}
}

/**
 * NewCustomer is the screen `roster init` used to be.
 *
 * `init` seeded a tenant, somebody in it and the `everything` role, so every
 * deployment started life with a customer nobody had asked for -- named after
 * an example company, in a production database. It seeds the control plane now
 * and this is where a customer comes from, which makes an operator's first act
 * the same act as their hundredth.
 *
 * # What it deliberately does not do
 *
 * Give the person a way in. A password and a key are both one panel down, on
 * the person themselves, because that is where they belong whether somebody was
 * created a minute ago or a year ago -- and because a form that minted a secret
 * as a side effect of creating a row would put one on the screen before the
 * operator had anybody to read it to.
 */
function NewTenant(props: {
	writes: Writes
	may: (method: string) => boolean
	onMade: (v: Tenant) => void
}): React.ReactNode {
	const [busy, setBusy] = useState(false)
	const [said, say] = useState<{ kind: 'done' | 'bad'; text: string } | null>(null)

	const allowed = needs.every((m) => props.may(m))

	return (
		<div className="new-tenant">
			<form
				onSubmit={(e) => {
					e.preventDefault()

					const form = e.currentTarget
					const f = new FormData(form)
					const alias = String(f.get('alias') ?? '').trim()
					const who = String(f.get('who') ?? '').trim()
					if (alias === '' || who === '') return

					setBusy(true)
					say(null)
					void stand(
						props.writes,
						alias,
						String(f.get('name') ?? '').trim(),
						who,
						String(f.get('id') ?? '').trim(),
					)
						.then((v) => {
							form.reset()
							props.onMade(v.tenant)
							say({ kind: 'done', text: v.said })
						})
						.catch((e: unknown) =>
							say({ kind: 'bad', text: e instanceof Error ? e.message : 'no' }),
						)
						.finally(() => setBusy(false))
				}}
			>
				<input name="alias" placeholder="tenant" required />
				<input name="name" placeholder="name (optional)" />
				<input name="who" placeholder="their first person" defaultValue="admin" required />
				{/* Almost always empty. It is here because an app that already
				    knows this organisation has to agree with roster about which
				    tenant it is from the start, and disagreeing fails silently
				    -- see `stand`. */}
				<input name="id" placeholder="identifier (only if an app has one)" />
				<button type="submit" disabled={busy || !allowed}>
					new customer
				</button>
			</form>

			{/* A permission this operator does not hold, said rather than
			    hidden: a screen that dropped the form would leave them
			    wondering whether the feature exists. */}
			{!allowed && (
				<p className="none">
					this needs {needs.filter((m) => !props.may(m)).join(', ')}
				</p>
			)}
			{said?.kind === 'done' && <p className="note">{said.text}</p>}
			{said?.kind === 'bad' && <p className="bad">{said.text}</p>}
		</div>
	)
}

/**
 * Failed says what the server said.
 *
 * Which is the right thing here and not everywhere: this page is an operator's,
 * and a refusal they cannot read is one they cannot act on. A customer-facing
 * page says less.
 */
function Failed(props: { at: unknown }): React.ReactNode {
	return <p className="bad">{props.at instanceof Error ? props.at.message : 'no'}</p>
}
