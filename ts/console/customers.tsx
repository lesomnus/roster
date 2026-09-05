/**
 * The customers screen: the tenants this deployment serves, the people in each,
 * and what each of them signs in with.
 *
 * # It reads a different port
 *
 * The rest of the console is the **control plane** -- who runs the deployment
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

import { Provider, useCall, useQuery } from '@lesomnus/payday/react'
import type { App } from '@lesomnus/payday/react'

import type { Tenant } from '../gen/roster/payday/tenant_pb.js'
import { TenantService } from '../gen/roster/payday/tenant_svc_pb.js'
import { HolderService } from '../gen/roster/payday/holder_svc_pb.js'

import type { Admin } from '../lib/client.js'
import { go, useRoute } from '../lib/route.js'
import { Devtools } from '@lesomnus/payday/react/devtools'
import { entities } from '../gen/entities.js'
import { Person } from './people.js'
import { Arrives } from './arrives.js'
import { Organisation } from './organisation.js'
import { Access } from './access.js'
import { Trail } from './trail.js'

/** Panel is what opens under a customer's row: one at a time, because they nest. */
type Panel = 'people' | 'arrives' | 'organisation' | 'access' | 'trail' | 'edit'
const panels: readonly Panel[] = ['people', 'arrives', 'organisation', 'access', 'trail', 'edit']

/** panelOf reads a customer and a panel off the path, or nothing open. */
function panelOf(id: string | undefined, on: string | undefined): { id: string; on: Panel } | null {
	if (id === undefined || id === '' || !(panels as readonly string[]).includes(on ?? '')) return null

	return { id, on: on as Panel }
}

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
 * Customers is the screen, under its own store.
 *
 * `app` is null while the store is opening, which is one render rather than a
 * state machine: the disk mirror is read before the first query runs, so a
 * spinner here is the only honest thing to draw.
 */
export function Customers(props: {
	app: App | null
	admin: Admin | null
	may: (method: string) => boolean
}): React.ReactNode {
	if (props.app === null || props.admin === null) return <p className="loading">…</p>

	return (
		<Provider app={props.app}>
			<Tenants admin={props.admin} may={props.may} />
			{/* The same window, on the data plane's store. */}
			{import.meta.env.DEV && <Devtools entities={entities} />}
		</Provider>
	)
}

function Tenants(props: { admin: Admin; may: (method: string) => boolean }): React.ReactNode {
	const vs = useQuery(TenantService.method.list, {})

	// Which customer is open, and on which panel: who is in it, how they
	// arrive, how they are organised, what they may do, what was done. One at
	// a time, because the panels nest under the row and two open at once read
	// as one. It is the address bar's to say -- `/customers/@<alias>/<panel>`,
	// the alias the CLI names a tenant by, because somebody reads the address
	// -- so the back button closes a panel and a reload keeps it open.
	const route = useRoute()
	const at = panelOf(route[1], route[2])
	const same = (v: Tenant): boolean => at !== null && '@' + v.alias === at.id

	// What this screen made since it read, which is the shape `Keys` already
	// uses one file over: a list query is not revalidated by a write this page
	// made, and a customer that vanished until a reload would read as one that
	// was not created. Merged by identifier, so a store that does pick the row
	// up does not draw it twice.
	const [made, setMade] = useState<Tenant[]>([])

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	const read = vs.data?.items ?? []
	const items = [...read, ...made.filter((v) => !read.some((w) => uuid(w.id) === uuid(v.id)))]

	return (
		<section>
			<h2>customers</h2>

			<NewCustomer
				admin={props.admin}
				may={props.may}
				onMade={(v) => setMade((was) => [...was, v])}
			/>

			{items.length === 0 && <p className="none">nobody yet</p>}

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
						const id = uuid(v.id)
						const open = same(v)
						const toggle = (on: Panel) => () =>
							go(open && at?.on === on ? ['customers'] : ['customers', '@' + v.alias, on])
						const label = (on: Panel, name: string) => (open && at?.on === on ? 'hide' : name)

						return (
							<tr key={id} className={open ? 'at' : ''}>
								<td>{v.alias}</td>
								<td>{v.name}</td>
								<td>{when(v.dateCreated)}</td>
								<td className="acts">
									<button onClick={toggle('people')}>{label('people', 'people')}</button>
									<button onClick={toggle('arrives')}>{label('arrives', 'arrives through')}</button>
									<button onClick={toggle('organisation')}>{label('organisation', 'organisation')}</button>
									<button onClick={toggle('access')}>{label('access', 'access')}</button>
									<button onClick={toggle('trail')}>{label('trail', 'trail')}</button>
									<button onClick={toggle('edit')}>{label('edit', 'edit')}</button>
								</td>
							</tr>
						)
					})}
				</tbody>
			</table>

			{at?.on === 'people' && (
				<People
					tenant={items.find(same)}
					admin={props.admin}
					may={props.may}
				/>
			)}
			{at?.on === 'arrives' && (
				<Arrives tenant={items.find(same)} may={props.may} />
			)}
			{at?.on === 'organisation' && (
				<Organisation tenant={items.find(same)} may={props.may} />
			)}
			{at?.on === 'access' && (
				<Access tenant={items.find(same)} may={props.may} />
			)}
			{at?.on === 'trail' && <Trail tenant={items.find(same)} />}
			{at?.on === 'edit' && <EditTenant tenant={items.find(same)} may={props.may} />}
		</section>
	)
}

/**
 * EditTenant is what a customer says about itself: name, a note, labels --
 * through `Tenant.Update`, under the version read, and never the alias every
 * reference resolves through nor the identifier an app has written down.
 * Labels are the one thing a page reads per tenant that the schema does not
 * name -- branding, a support address -- so they are drawn as lines of
 * `key=value` rather than as fields somebody would have to invent.
 */
function EditTenant(props: { tenant: Tenant | undefined; may: (method: string) => boolean }): React.ReactNode {
	const update = useCall(TenantService.method.update)
	const [said, say] = useState<{ kind: 'done' | 'bad'; text: string } | null>(null)

	const t = props.tenant
	if (t === undefined) return null

	const labels = Object.entries(t.labels ?? {})
		.map(([k, v]) => `${k}=${v}`)
		.join('\n')

	return (
		<section className="within">
			<h3>{t.alias} — edit</h3>
			{!props.may('/roster.TenantService/Update') && (
				<p className="none">this needs /roster.TenantService/Update</p>
			)}
			<form
				className="profile"
				onSubmit={(e) => {
					e.preventDefault()
					const f = new FormData(e.currentTarget)
					const parsed: Record<string, string> = {}
					for (const line of String(f.get('labels') ?? '').split('\n')) {
						const i = line.indexOf('=')
						if (i > 0) parsed[line.slice(0, i).trim()] = line.slice(i + 1).trim()
					}
					say(null)
					void update
						.call({
							ref: { key: { case: 'id', value: t.id } },
							dateUpdated: t.dateUpdated,
							name: String(f.get('name') ?? '').trim(),
							desc: String(f.get('desc') ?? '').trim(),
							labels: parsed,
						})
						.then(() => say({ kind: 'done', text: 'saved' }))
						.catch((e: unknown) => say({ kind: 'bad', text: e instanceof Error ? e.message : 'no' }))
				}}
			>
				<input name="name" placeholder="name" defaultValue={t.name} />
				<input name="desc" placeholder="note" defaultValue={t.desc} />
				<textarea name="labels" placeholder={'labels, one per line: brand=Contoso'} defaultValue={labels} rows={3} />
				<button type="submit" disabled={update.state === 'pending' || !props.may('/roster.TenantService/Update')}>
					save
				</button>
			</form>
			{said?.kind === 'done' && <p className="note">{said.text}</p>}
			{said?.kind === 'bad' && <p className="bad">{said.text}</p>}
		</section>
	)
}

/**
 * People is who is in one tenant.
 *
 * Filtered by tenant rather than listed and sifted here, which is the whole
 * reason `HolderFilter` grew the field: a page that read every holder and kept
 * the ones it wanted would be reading every customer's people to draw one
 * customer's.
 */
function People(props: {
	tenant: { id?: Uint8Array; alias?: string } | undefined
	admin: Admin
	may: (method: string) => boolean
}): React.ReactNode {
	const id = props.tenant?.id
	const vs = useQuery(HolderService.method.list, {
		filters: id === undefined ? [] : [{ tenant: { key: { case: 'id', value: id } } }],
	})

	// Which person is open: the fourth segment, `/customers/@<tenant>/people/<alias>`.
	const route = useRoute()
	const at = route[3] ?? null
	const under = '@' + (props.tenant?.alias ?? '')
	const open = (who: string | null): void =>
		go(who === null ? ['customers', under, 'people'] : ['customers', under, 'people', who])

	// Whom this screen erased since it read: a soft erase hides the row from
	// every read, and a list still showing them would say the erase did not take.
	const [gone, setGone] = useState<string[]>([])

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section className="within">
			<h3>{props.tenant?.alias}</h3>

			{items.length === 0 && <p className="none">nobody in it</p>}

			<table>
				<thead>
					<tr>
						<th>alias</th>
						<th>name</th>
						<th>since</th>
						<th />
					</tr>
				</thead>
				<tbody>
					{items.map((v) => {
						const who = v.alias

						return (
							<tr key={uuid(v.id)} className={who === at ? 'at' : ''}>
								<td>{v.alias}</td>
								<td>{v.name}</td>
								<td>{when(v.dateCreated)}</td>
								<td>
									<button onClick={() => open(who === at ? null : who)}>
										{who === at ? 'hide' : 'signs in with'}
									</button>
								</td>
							</tr>
						)
					})}
				</tbody>
			</table>

			{at !== null && (
				<Person
					holder={items.find((v) => v.alias === at)}
					admin={props.admin}
					may={props.may}
					onErased={() => {
						setGone((was) => [...was, at])
						open(null)
					}}
				/>
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

/** The four writes, so the button can be drawn as what it actually does. */
const needs = [
	'/roster.TenantService/Add',
	'/roster.HolderService/Add',
	'/roster.RoleService/Add',
	'/roster.BindingService/Add',
]

/**
 * stand puts a customer up: a tenant, somebody in it, a role, and the binding.
 *
 * Four calls and not one, because there is no fifth RPC that does this and
 * there should not be -- each of the four is checked by the same rules every
 * other write on this port is, and a composite would be a fifth thing to hold
 * them to.
 *
 * # It is not a transaction, and that is survivable here
 *
 * `BatchService` is served on the data plane and not on this port, and the
 * pattern an operator holds would not cover it in any case: that pattern is
 * roster's own package and a batch is payday's. So a call that fails part way
 * leaves what came before it.
 *
 * What that leaves is a tenant with nobody in it, or somebody with no role --
 * and neither is the deadlock `roster init` was fixed for. That one was real
 * because writing the first role needs a binding only writing the first role
 * could give; here the operator writes it from **outside** every tenant, so
 * they can simply finish. Which is why this says how far it got rather than
 * trying to undo it: rolling back would delete rows a second operator may
 * already be looking at.
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
 * making customers. `docs/operating.md` carries the warning.
 */
async function stand(
	admin: Admin,
	alias: string,
	name: string,
	who: string,
	id: string,
): Promise<{ tenant: Tenant; said: string }> {
	const said = (at: string, e: unknown): Error =>
		new Error(`${at}: ${e instanceof Error ? e.message : 'no'}`)

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
		tenant = await admin.tenant.add(
			given === undefined ? { alias, name } : { alias, name, id: given },
		)
	} catch (e) {
		throw said('the tenant was not created', e)
	}

	const at = { key: { case: 'id' as const, value: tenant.id } }

	let holder
	try {
		holder = await admin.holder.add({ tenant: at, alias: who })
	} catch (e) {
		throw said(`${alias} exists and has nobody in it`, e)
	}

	let role
	try {
		role = await admin.role.add({
			tenant: at,
			alias: 'everything',
			desc: 'Every RPC roster serves, including ones added by a later release.',
			methods: [every],
		})
	} catch (e) {
		throw said(`${alias} exists and ${who} is in it, holding nothing`, e)
	}

	try {
		await admin.binding.add({
			role: { key: { case: 'id', value: role.id } },
			holder: { key: { case: 'id', value: holder.id } },
		})
	} catch (e) {
		throw said(`${alias} exists with a role nobody is bound to`, e)
	}

	return {
		tenant,
		said:
			`${alias} is up, and ${who} holds ${every} in it — ` +
			'and nothing to call with yet. Open them below and write a password or a key.',
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
function NewCustomer(props: {
	admin: Admin
	may: (method: string) => boolean
	onMade: (v: Tenant) => void
}): React.ReactNode {
	const [busy, setBusy] = useState(false)
	const [said, say] = useState<{ kind: 'done' | 'bad'; text: string } | null>(null)

	const allowed = needs.every((m) => props.may(m))

	return (
		<div className="new-customer">
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
						props.admin,
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
