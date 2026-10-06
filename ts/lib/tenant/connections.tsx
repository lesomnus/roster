/**
 * The directories a tenant's people arrive through: its `Connection` rows.
 *
 * What a deployment keeps here is what a **login app** needs to talk to a
 * provider -- the issuer, the client id, and where the secret is kept. roster is
 * not the relying party: it holds no client secret, validates no token, and has no
 * opinion about which provider a tenant uses.
 *
 * Which is why `secret_ref` is a **reference** and not a secret. A page that
 * offered to type one in would be a page asking somebody to put a credential
 * somewhere roster would then have to protect.
 *
 * @module
 */

import { useState } from 'react'
import { useCall, useQuery } from '@lesomnus/payday/react'

import { ConnectionService } from '#gen/app/connection_svc_pb.js'
import { HostProofService, HostService, MailDomainService } from '#gen/app/host_svc_pb.js'

// when is a moment written the way somebody reads one, or nothing.
/**
 * Arrives is the tab, under one tenant.
 *
 * `may` is `Me.Get`'s answer about whoever is signed in, passed down rather
 * than asked again: it decides what is worth **drawing** and never what is
 * allowed. The server refuses either way.
 */

import { by, ref as at, said, uuid, when } from './parts.js'
import { Bar, Fill, Menu, Sheet } from '#lib/ui.js'

export function Connections(props: {
	tenant: { id?: Uint8Array; alias?: string } | undefined
	may: (m: string) => boolean
}): React.ReactNode {
	const id = props.tenant?.id
	if (id === undefined) return null

	return (
		<section className="within">
			<h3>{props.tenant?.alias}</h3>
			<ConnectionList tenant={id} may={props.may} />
		</section>
	)
}

function ConnectionList(props: { tenant: Uint8Array; may: (m: string) => boolean }): React.ReactNode {
	const vs = useQuery(ConnectionService.method.list, by(props.tenant))
	const add = useCall(ConnectionService.method.add)
	const update = useCall(ConnectionService.method.update)
	const erase = useCall(ConnectionService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [editing, setEditing] = useState<string | null>(null)
	const [bad, setBad] = useState<string | null>(null)
	const [adding, openAdd] = useState(false)

	// Over what was read: `ConnectionService` has no `Search`, and the placeholder
	// says how many it is narrowing rather than passing for a search of the table.
	const [find, setFind] = useState('')

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const all = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))
	const q = find.trim().toLowerCase()
	const items = q === '' ? all : all.filter((v) => `${v.name} ${v.issuer}`.toLowerCase().includes(q))

	return (
		<section>
			<h4>connections</h4>
			<p className="note">
				Where this customer's people authenticate — Entra, Google, GitHub. What
				is here is public: the issuer, the client id, the scopes. The client
				secret is <strong>not</strong> here and never will be; <code>secret ref</code>{' '}
				is where the account app finds it (<code>env:CONTOSO_ENTRA_SECRET</code>),
				and roster stores that string without reading it. It names one of the
				deployment's secrets, so a new one — or a new issuer for a connection
				that has one — is the deployment's to write: a tenant keeps it or takes
				it away, and a connection with no secret is the tenant's own.
			</p>
			<p className="note">
				<code>subject</code> is which claim of the token names the person: <code>sub</code>{' '}
				unless it says otherwise, and <code>oid</code> for Entra, whose <code>sub</code> is
				one per app and whose <code>oid</code> is what a directory provisioning them sends.
				Moving to <code>oid</code> moves everybody at their next sign-in; moving back is
				the deployment's.
			</p>

			<Bar>
				<input
					className="find"
					type="search"
					aria-label="filter providers"
					placeholder={`filter these ${all.length}`}
					value={find}
					onChange={(e) => setFind(e.target.value)}
				/>
				<Fill />
				<button
					className="add"
					disabled={!props.may('/roster.ConnectionService/Add')}
					onClick={() => openAdd(true)}
				>
					add provider
				</button>
			</Bar>

			{items.length === 0 && (
				<p className="none">
					{all.length === 0
						? 'no providers yet — people here sign in with a password, or not at all'
						: 'none of these match'}
				</p>
			)}
			{items.length > 0 && (
				<table>
					<thead>
						<tr>
							<th>name, and the issuer it is</th>
							<th>client id, and the scopes asked for</th>
							<th>secret ref</th>
							<th />
						</tr>
					</thead>
					<tbody>
						{items.map((v) =>
							editing === uuid(v.id) ? (
								<tr key={uuid(v.id)} className="editing">
									<td className="mono">{v.name}</td>
									<td colSpan={3}>
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
														subjectClaim: String(f.get('subject_claim') ?? ''),
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
											<Subject value={v.subjectClaim} />
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
								{/* The name is what a row elsewhere points at and the issuer is
								    what it means; one is copied and one is read, so they are a
								    line each rather than two columns to scan across. */}
								<td className="mono">
									{v.name}
									<span className="under">{v.issuer}</span>
								</td>
								<td className="mono">
									{v.clientId}
									<span className="under">
										{v.scopes.join(' ')}
										{v.subjectClaim !== '' && ` · subject ${v.subjectClaim}`}
									</span>
								</td>
								<td className="mono">{v.secretRef}</td>
								<td className="acts">
									<button disabled={!props.may('/roster.ConnectionService/Update')} onClick={() => setEditing(uuid(v.id))}>
										edit
									</button>
									<Menu label={`more for ${v.name}`}>
										<button
											className="danger"
											disabled={!props.may('/roster.ConnectionService/Erase')}
											onClick={() => {
												setBad(null)
												void erase
													.call({ key: { case: 'id', value: v.id } })
													.then(() => setGone((was) => [...was, uuid(v.id)]))
													.catch((e: unknown) => setBad(said(e)))
											}}
										>
											remove provider
										</button>
									</Menu>
								</td>
							</tr>
							),
						)}
					</tbody>
				</table>
			)}

			{bad !== null && <p className="bad">{bad}</p>}

			<Sheet at={adding} onClose={() => openAdd(false)} title="add a provider" bad={bad}>
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
							subjectClaim: String(f.get('subject_claim') ?? ''),
							desc: String(f.get('desc') ?? '').trim(),
						})
						.then(() => {
							form.reset()
							openAdd(false)
						})
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
				<Subject value="" />
				<input name="desc" placeholder="note (optional)" />
				<button
					type="submit"
					disabled={add.state === 'pending' || !props.may('/roster.ConnectionService/Add')}
				>
					add provider
				</button>
			</form>
			</Sheet>
		</section>
	)
}

// Subject is which claim of a token names the person at this connection, as a
// choice of the two a front door reads (`Connection.subject_claim`).
function Subject(props: { value: string }) {
	return (
		<select name="subject_claim" aria-label="subject claim" defaultValue={props.value}>
			<option value="">subject: sub</option>
			<option value="oid">subject: oid (Entra)</option>
		</select>
	)
}
