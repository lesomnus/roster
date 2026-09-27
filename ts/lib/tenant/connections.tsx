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

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section>
			<h4>connections</h4>
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
