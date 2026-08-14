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

import { Provider, useQuery } from '@lesomnus/payday/react'
import type { App } from '@lesomnus/payday/react'

import { TenantService } from '../gen/roster/payday/tenant_svc_pb.js'
import { HolderService } from '../gen/roster/payday/holder_svc_pb.js'
import { IdentityService } from '../gen/app/identity_svc_pb.js'

/** uuid is the bytes an identifier arrives as, written the way a person reads one. */
function uuid(v: Uint8Array | undefined): string {
	if (v === undefined || v.length !== 16) return ''

	const h = [...v].map((b) => b.toString(16).padStart(2, '0')).join('')

	return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
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
export function Customers(props: { app: App | null }): React.ReactNode {
	if (props.app === null) return <p className="loading">…</p>

	return (
		<Provider app={props.app}>
			<Tenants />
		</Provider>
	)
}

function Tenants(): React.ReactNode {
	const vs = useQuery(TenantService.method.list, {})
	const [at, go] = useState<string | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	const items = vs.data?.items ?? []

	return (
		<section>
			<h2>customers</h2>

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

						return (
							<tr key={id} className={id === at ? 'at' : ''}>
								<td>{v.alias}</td>
								<td>{v.name}</td>
								<td>{when(v.dateCreated)}</td>
								<td>
									<button onClick={() => go(id === at ? null : id)}>
										{id === at ? 'hide' : 'people'}
									</button>
								</td>
							</tr>
						)
					})}
				</tbody>
			</table>

			{at !== null && <People tenant={items.find((v) => uuid(v.id) === at)} />}
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
function People(props: { tenant: { id?: Uint8Array; alias?: string } | undefined }): React.ReactNode {
	const id = props.tenant?.id
	const vs = useQuery(HolderService.method.list, {
		filters: id === undefined ? [] : [{ tenant: { key: { case: 'id', value: id } } }],
	})
	const [at, go] = useState<string | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	const items = vs.data?.items ?? []

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
						const who = uuid(v.id)

						return (
							<tr key={who} className={who === at ? 'at' : ''}>
								<td>{v.alias}</td>
								<td>{v.name}</td>
								<td>{when(v.dateCreated)}</td>
								<td>
									<button onClick={() => go(who === at ? null : who)}>
										{who === at ? 'hide' : 'signs in with'}
									</button>
								</td>
							</tr>
						)
					})}
				</tbody>
			</table>

			{at !== null && <Identities holder={items.find((v) => uuid(v.id) === at)} />}
		</section>
	)
}

/**
 * Identities is what one person signs in with.
 *
 * The subject is shown as the provider gave it and is not a name: it is
 * whatever that provider treats as immutable -- a numeric id for GitHub, an
 * `oid` for Entra. Somebody reading this screen is checking *which account*,
 * and the answer has to be the one the provider would answer with.
 */
function Identities(props: { holder: { id?: Uint8Array; alias?: string } | undefined }): React.ReactNode {
	const id = props.holder?.id
	const vs = useQuery(IdentityService.method.list, {
		filters: id === undefined ? [] : [{ holder: { key: { case: 'id', value: id } } }],
	})

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	const items = vs.data?.items ?? []

	return (
		<section className="within">
			<h4>{props.holder?.alias} signs in with</h4>

			{items.length === 0 && (
				<p className="none">
					nothing — they have a password here, or no way in at all
				</p>
			)}

			<table>
				<thead>
					<tr>
						<th>provider</th>
						<th>subject</th>
						<th>since</th>
					</tr>
				</thead>
				<tbody>
					{items.map((v) => (
						<tr key={uuid(v.id)}>
							<td>{v.provider}</td>
							<td className="mono">{v.subject}</td>
							<td>{when(v.dateCreated)}</td>
						</tr>
					))}
				</tbody>
			</table>
		</section>
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
