/**
 * How one customer's people arrive: the names that reach them, the domains
 * that route to a provider, and the providers themselves.
 *
 * # The three rows, and what each one is for
 *
 * A `Host` is a name this deployment serves for this tenant --
 * `contoso.example.com` -- which is how a front door turns the address a
 * browser arrived at into a tenant (`FrontService.WhoseHost`), instead of
 * holding a map of its own. A `MailDomain` is *addresses at `@contoso.com` go
 * to Entra*, which is identifier-first routing (`FrontService.WhereFrom`), and
 * it hangs off the domain rather than the person on purpose: answered per
 * person it is an account-enumeration oracle. A `Connection` is the provider
 * -- issuer, client id, scopes -- which every front door would otherwise hold a
 * stale copy of.
 *
 * # What is not here, and why the form says so
 *
 * The client secret. `secret_ref` is *where the account app finds it*
 * (`env:CONTOSO_ENTRA_SECRET`), a string roster stores and answers with and
 * never reads: reading it would mean doing the OIDC exchange, which is being
 * the relying party, which roster is not (`connection.proto`). So this screen
 * cannot test a connection either; the account app can, by signing in.
 *
 * # Add, edit and remove -- and what edit never touches
 *
 * `Patch` is closed on the wire (`grpcx.GeneralWrite`), as it is for every
 * entity, so each of the three has an `Update` overlay the way `Tenant` and
 * `Holder` do (`proto/ext/app/host_svc.ext.proto`, `connection_svc.ext.proto`):
 * what a row says about itself, under the version read. The **name** is not
 * in any of them. A host name is what a tenant is resolved through, a domain
 * what an address is routed by, and a provider's name what `Identity.provider`
 * points at -- erased and re-added under another name, every person who
 * arrived through it is a stranger, silently. A name that must change is a
 * new row, and the two calls it takes to say so are the two this screen draws.
 *
 * # Reads go through the store, writes through `useCall`
 *
 * So a row added here appears in the list without this page inserting it, and
 * a second operator's console showing the same tenant sees it at the same
 * time. Removal keeps a local list of what this page erased, for the reason
 * `people.tsx` gives about keys: a table that still showed a row somebody just
 * removed would be worse than one a moment behind the server.
 */

import { useState } from 'react'
import { useCall, useQuery } from '@lesomnus/payday/react'

import { ConnectionService } from '../gen/app/connection_svc_pb.js'
import { HostService, MailDomainService } from '../gen/app/host_svc_pb.js'

/** uuid is the bytes an identifier arrives as, written the way a person reads one. */
function uuid(v: Uint8Array | undefined): string {
	if (v === undefined || v.length !== 16) return ''

	const h = [...v].map((b) => b.toString(16).padStart(2, '0')).join('')

	return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

function said(e: unknown): string {
	return e instanceof Error ? e.message : 'no'
}

/**
 * Arrives is the tab, under one tenant.
 *
 * `may` is the control plane's answer about the operator, passed down rather
 * than asked again: it decides what is worth **drawing** and never what is
 * allowed. The server refuses either way.
 */
export function Arrives(props: {
	tenant: { id?: Uint8Array; alias?: string } | undefined
	may: (method: string) => boolean
}): React.ReactNode {
	const id = props.tenant?.id
	if (id === undefined) return null

	return (
		<section className="within arrives">
			<h3>{props.tenant?.alias} — arrives through</h3>
			<Hosts tenant={id} may={props.may} />
			<Connections tenant={id} may={props.may} />
			<MailDomains tenant={id} may={props.may} />
		</section>
	)
}

const by = (tenant: Uint8Array) => ({
	filters: [{ tenant: { key: { case: 'id' as const, value: tenant } } }],
})

const at = (tenant: Uint8Array) => ({ key: { case: 'id' as const, value: tenant } })

function Hosts(props: { tenant: Uint8Array; may: (m: string) => boolean }): React.ReactNode {
	const vs = useQuery(HostService.method.list, by(props.tenant))
	const add = useCall(HostService.method.add)
	const update = useCall(HostService.method.update)
	const erase = useCall(HostService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [editing, setEditing] = useState<string | null>(null)
	const [bad, setBad] = useState<string | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section>
			<h4>names</h4>
			<p className="note">
				The hostnames a browser arrives at that mean this customer. A front door
				asks roster which tenant a name is rather than holding a map, so a name
				missing here is a sign-in page that says nobody is there.
			</p>

			{items.length === 0 && <p className="none">no names yet</p>}
			{items.length > 0 && (
				<table>
					<tbody>
						{items.map((v) =>
							editing === uuid(v.id) ? (
								<tr key={uuid(v.id)} className="editing">
									<td className="mono">{v.name}</td>
									<td colSpan={2}>
										<form
											className="edit"
											onSubmit={(e) => {
												e.preventDefault()
												const f = new FormData(e.currentTarget)
												setBad(null)
												void update
													.call({
														ref: { key: { case: 'id', value: v.id } },
														dateUpdated: v.dateUpdated,
														desc: String(f.get('desc') ?? '').trim(),
													})
													.then(() => setEditing(null))
													.catch((e: unknown) => setBad(said(e)))
											}}
										>
											<input name="desc" placeholder="note" defaultValue={v.desc} autoFocus />
											<button type="submit" disabled={update.state === 'pending'}>
												save
											</button>
											<button type="button" onClick={() => setEditing(null)}>
												cancel
											</button>
										</form>
									</td>
								</tr>
							) : (
							<tr key={uuid(v.id)}>
								<td className="mono">{v.name}</td>
								<td>{v.desc}</td>
								<td>
									<button disabled={!props.may('/roster.HostService/Update')} onClick={() => setEditing(uuid(v.id))}>
										edit
									</button>
									<button
										disabled={!props.may('/roster.HostService/Erase')}
										onClick={() => {
											setBad(null)
											void erase
												.call({ key: { case: 'id', value: v.id } })
												.then(() => setGone((was) => [...was, uuid(v.id)]))
												.catch((e: unknown) => setBad(said(e)))
										}}
									>
										remove
									</button>
								</td>
							</tr>
							),
						)}
					</tbody>
				</table>
			)}

			<form
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const f = new FormData(form)
					const name = String(f.get('name') ?? '').trim()
					if (name === '') return

					setBad(null)
					// Not normalised here: the layer refuses a name that is not
					// already the way it will be compared, and says what it
					// should have been -- which is worth more to the operator
					// than a silent fix they cannot see (`server/core/host.go`).
					void add
						.call({ tenant: at(props.tenant), name, desc: String(f.get('desc') ?? '').trim() })
						.then(() => form.reset())
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<input name="name" placeholder="contoso.example.com" required />
				<input name="desc" placeholder="note (optional)" />
				<button type="submit" disabled={add.state === 'pending' || !props.may('/roster.HostService/Add')}>
					add name
				</button>
			</form>
			{bad !== null && <p className="bad">{bad}</p>}
		</section>
	)
}

function Connections(props: { tenant: Uint8Array; may: (m: string) => boolean }): React.ReactNode {
	const vs = useQuery(ConnectionService.method.list, by(props.tenant))
	const add = useCall(ConnectionService.method.add)
	const update = useCall(ConnectionService.method.update)
	const erase = useCall(ConnectionService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [editing, setEditing] = useState<string | null>(null)
	const [bad, setBad] = useState<string | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section>
			<h4>providers</h4>
			<p className="note">
				Where this customer's people authenticate — Entra, Google, GitHub. What
				is here is public: the issuer, the client id, the scopes. The client
				secret is <strong>not</strong> here and never will be; <code>secret ref</code>{' '}
				is where the account app finds it (<code>env:CONTOSO_ENTRA_SECRET</code>),
				and roster stores that string without reading it.
			</p>

			{items.length === 0 && <p className="none">no providers yet — people here sign in with a password, or not at all</p>}
			{items.length > 0 && (
				<table>
					<thead>
						<tr>
							<th>name</th>
							<th>issuer</th>
							<th>client id</th>
							<th>scopes</th>
							<th>secret ref</th>
							<th />
						</tr>
					</thead>
					<tbody>
						{items.map((v) =>
							editing === uuid(v.id) ? (
								<tr key={uuid(v.id)} className="editing">
									<td className="mono">{v.name}</td>
									<td colSpan={5}>
										<form
											className="edit connection"
											onSubmit={(e) => {
												e.preventDefault()
												const f = new FormData(e.currentTarget)
												const scopes = String(f.get('scopes') ?? '')
													.split(/[\s,]+/)
													.map((s) => s.trim())
													.filter((s) => s !== '')
												setBad(null)
												void update
													.call({
														ref: { key: { case: 'id', value: v.id } },
														dateUpdated: v.dateUpdated,
														issuer: String(f.get('issuer') ?? '').trim(),
														clientId: String(f.get('client_id') ?? '').trim(),
														scopes,
														secretRef: String(f.get('secret_ref') ?? '').trim(),
														desc: String(f.get('desc') ?? '').trim(),
													})
													.then(() => setEditing(null))
													.catch((e: unknown) => setBad(said(e)))
											}}
										>
											<input name="issuer" placeholder="issuer" defaultValue={v.issuer} required autoFocus />
											<input name="client_id" placeholder="client id" defaultValue={v.clientId} required />
											<input name="scopes" placeholder="scopes beyond openid" defaultValue={v.scopes.join(' ')} />
											<input name="secret_ref" placeholder="env:CONTOSO_ENTRA_SECRET" defaultValue={v.secretRef} />
											<input name="desc" placeholder="note" defaultValue={v.desc} />
											<button type="submit" disabled={update.state === 'pending'}>
												save
											</button>
											<button type="button" onClick={() => setEditing(null)}>
												cancel
											</button>
										</form>
									</td>
								</tr>
							) : (
							<tr key={uuid(v.id)}>
								<td className="mono">{v.name}</td>
								<td className="mono">{v.issuer}</td>
								<td className="mono">{v.clientId}</td>
								<td className="mono">{v.scopes.join(' ')}</td>
								<td className="mono">{v.secretRef}</td>
								<td>
									<button disabled={!props.may('/roster.ConnectionService/Update')} onClick={() => setEditing(uuid(v.id))}>
										edit
									</button>
									<button
										disabled={!props.may('/roster.ConnectionService/Erase')}
										onClick={() => {
											setBad(null)
											void erase
												.call({ key: { case: 'id', value: v.id } })
												.then(() => setGone((was) => [...was, uuid(v.id)]))
												.catch((e: unknown) => setBad(said(e)))
										}}
									>
										remove
									</button>
								</td>
							</tr>
							),
						)}
					</tbody>
				</table>
			)}

			<form
				className="connection"
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const f = new FormData(form)
					const name = String(f.get('name') ?? '').trim()
					const issuer = String(f.get('issuer') ?? '').trim()
					const clientId = String(f.get('client_id') ?? '').trim()
					if (name === '' || issuer === '' || clientId === '') return

					const scopes = String(f.get('scopes') ?? '')
						.split(/[\s,]+/)
						.map((s) => s.trim())
						.filter((s) => s !== '')

					setBad(null)
					void add
						.call({
							tenant: at(props.tenant),
							name,
							issuer,
							clientId,
							scopes,
							secretRef: String(f.get('secret_ref') ?? '').trim(),
							desc: String(f.get('desc') ?? '').trim(),
						})
						.then(() => form.reset())
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				{/* The name is what `Identity.provider` and `MailDomain.provider`
				    use, chosen once: a name that changed would make the same
				    person a new person. */}
				<input name="name" placeholder="entra" required />
				<input name="issuer" placeholder="https://login.microsoftonline.com/…/v2.0" required />
				<input name="client_id" placeholder="client id" required />
				<input name="scopes" placeholder="scopes beyond openid, e.g. email" />
				<input name="secret_ref" placeholder="env:CONTOSO_ENTRA_SECRET" />
				<input name="desc" placeholder="note (optional)" />
				<button
					type="submit"
					disabled={add.state === 'pending' || !props.may('/roster.ConnectionService/Add')}
				>
					add provider
				</button>
			</form>
			{bad !== null && <p className="bad">{bad}</p>}
		</section>
	)
}

function MailDomains(props: { tenant: Uint8Array; may: (m: string) => boolean }): React.ReactNode {
	const vs = useQuery(MailDomainService.method.list, by(props.tenant))
	const providers = useQuery(ConnectionService.method.list, by(props.tenant))
	const add = useCall(MailDomainService.method.add)
	const update = useCall(MailDomainService.method.update)
	const erase = useCall(MailDomainService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [editing, setEditing] = useState<string | null>(null)
	const [bad, setBad] = useState<string | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))
	const names = (providers.data?.items ?? []).map((v) => v.name)

	return (
		<section>
			<h4>mail domains</h4>
			<p className="note">
				<em>Addresses at @contoso.com go to Entra.</em> A front door that asks
				for an address first sends the person to the right provider without
				asking them which — and answers the same for every address at the
				domain, so nobody learns who is here by typing names.
			</p>

			{items.length === 0 && <p className="none">no domains routed yet</p>}
			{items.length > 0 && (
				<table>
					<thead>
						<tr>
							<th>domain</th>
							<th>goes to</th>
							<th />
							<th />
						</tr>
					</thead>
					<tbody>
						{items.map((v) =>
							editing === uuid(v.id) ? (
								<tr key={uuid(v.id)} className="editing">
									<td className="mono">@{v.name}</td>
									<td colSpan={3}>
										<form
											className="edit"
											onSubmit={(e) => {
												e.preventDefault()
												const f = new FormData(e.currentTarget)
												setBad(null)
												void update
													.call({
														ref: { key: { case: 'id', value: v.id } },
														dateUpdated: v.dateUpdated,
														provider: String(f.get('provider') ?? ''),
														desc: String(f.get('desc') ?? '').trim(),
													})
													.then(() => setEditing(null))
													.catch((e: unknown) => setBad(said(e)))
											}}
										>
											<select name="provider" defaultValue={v.provider} autoFocus>
												<option value="">nowhere (known, not routed)</option>
												{names.map((n) => (
													<option key={n} value={n}>
														{n}
													</option>
												))}
											</select>
											<input name="desc" placeholder="note" defaultValue={v.desc} />
											<button type="submit" disabled={update.state === 'pending'}>
												save
											</button>
											<button type="button" onClick={() => setEditing(null)}>
												cancel
											</button>
										</form>
									</td>
								</tr>
							) : (
							<tr key={uuid(v.id)}>
								<td className="mono">@{v.name}</td>
								<td className="mono">
									{v.provider === '' ? <span className="none">nowhere — known, not routed</span> : v.provider}
								</td>
								<td>{v.desc}</td>
								<td>
									<button disabled={!props.may('/roster.MailDomainService/Update')} onClick={() => setEditing(uuid(v.id))}>
										edit
									</button>
									<button
										disabled={!props.may('/roster.MailDomainService/Erase')}
										onClick={() => {
											setBad(null)
											void erase
												.call({ key: { case: 'id', value: v.id } })
												.then(() => setGone((was) => [...was, uuid(v.id)]))
												.catch((e: unknown) => setBad(said(e)))
										}}
									>
										remove
									</button>
								</td>
							</tr>
							),
						)}
					</tbody>
				</table>
			)}

			<form
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const f = new FormData(form)
					const name = String(f.get('name') ?? '')
						.trim()
						.replace(/^@/, '')
					if (name === '') return

					setBad(null)
					void add
						.call({
							tenant: at(props.tenant),
							name,
							provider: String(f.get('provider') ?? ''),
							desc: String(f.get('desc') ?? '').trim(),
						})
						.then(() => form.reset())
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<input name="name" placeholder="contoso.com" required />
				{/* A choice and not a text field: the value has to be a
				    `Connection.name` of this tenant, or the route goes nowhere. */}
				<select name="provider" defaultValue="">
					<option value="">nowhere (known, not routed)</option>
					{names.map((n) => (
						<option key={n} value={n}>
							{n}
						</option>
					))}
				</select>
				<input name="desc" placeholder="note (optional)" />
				<button
					type="submit"
					disabled={add.state === 'pending' || !props.may('/roster.MailDomainService/Add')}
				>
					route domain
				</button>
			</form>
			{bad !== null && <p className="bad">{bad}</p>}
		</section>
	)
}
