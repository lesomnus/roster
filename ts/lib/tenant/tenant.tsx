/**
 * The tenant itself: what it is, and what it says about itself.
 *
 * Every other screen beside this one lists a tenant's **rows** -- its holders, its
 * hosts, its roles. This one is the row those are all inside, which is why it is
 * the head of the sidebar rather than an entry in it: the button that says which
 * tenant is the button that opens the tenant.
 *
 * # Why it reads the row it was told about
 *
 * Its siblings take `{ id, alias }` and filter by it, and this takes the same --
 * but it needs the fields as well as the identity, so it reads them here rather
 * than having them handed down. That costs one call on one screen and buys a
 * screen that can be mounted anywhere the others can, which is the whole of why
 * `ts/lib/tenant/` is one directory: what differs between the two consoles is who
 * is calling and which listener answers, and neither is a thing a tenant's own
 * fields know about.
 *
 * On the admin listener that read is answered for whichever tenant was named, and
 * through the wall it can only ever answer with the caller's. The screen is the
 * same either way, because in both cases it was told which one.
 *
 * # What is read and what is written
 *
 * `Tenant.Get` draws it, and `Tenant.Update` is what the form calls -- so a caller
 * whose role covers the first and not the second gets the page and a form that
 * says which permission it wants. Not a hidden form: somebody who cannot change a
 * tenant is still owed the ability to read what it currently says, and a screen
 * that vanished would leave them unable to tell a missing permission from a
 * missing tenant.
 *
 * `dateUpdated` goes back with the write. It is payday's version column, so a
 * second person saving the same tenant from another tab is refused rather than
 * silently last-wins.
 *
 * @module
 */

import { useState } from 'react'
import { useCall, useQuery } from '@lesomnus/payday/react'

import { TenantService } from '#gen/roster/payday/tenant_svc_pb.js'

import { ref, said, uuid, when, type May } from './parts.js'

export function Tenant(props: {
	tenant: { id?: Uint8Array; alias?: string } | undefined
	may: May
}): React.ReactNode {
	const id = props.tenant?.id
	if (id === undefined) return null

	return (
		<section className="within">
			<h3>{props.tenant?.alias}</h3>
			<Row tenant={id} may={props.may} />
		</section>
	)
}

function Row(props: { tenant: Uint8Array; may: May }): React.ReactNode {
	const v = useQuery(TenantService.method.get, { ref: ref(props.tenant) })
	const update = useCall(TenantService.method.update)
	const [told, tell] = useState<{ kind: 'done' | 'bad'; text: string } | null>(null)

	if (v.state === 'pending') return <p className="loading">…</p>
	if (v.state === 'error') return <p className="error">{said(v.error)}</p>

	const t = v.data
	if (t === undefined) return null

	const labels = Object.entries(t.labels ?? {})
		.map(([k, v]) => `${k}=${v}`)
		.join('\n')

	const mayWrite = props.may('/roster.TenantService/Update')

	return (
		<>
			{/* The identity first, and read-only because none of it is a field: the
			    alias is how every reference to this tenant is written, and the dates
			    are stamps. A screen that offered to edit them would be offering a
			    write the server refuses. */}
			<h4>what it is</h4>
			<table>
				<tbody>
					<tr>
						<th>alias</th>
						<td>{t.alias}</td>
					</tr>
					<tr>
						<th>id</th>
						<td>
							<code>{uuid(t.id)}</code>
						</td>
					</tr>
					<tr>
						<th>created</th>
						<td>{when(t.dateCreated)}</td>
					</tr>
					<tr>
						<th>updated</th>
						<td>{when(t.dateUpdated)}</td>
					</tr>
				</tbody>
			</table>

			<h4>what it says about itself</h4>
			{!mayWrite && <p className="none">this needs /roster.TenantService/Update</p>}
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
					tell(null)
					void update
						.call({
							ref: ref(t.id),
							dateUpdated: t.dateUpdated,
							name: String(f.get('name') ?? '').trim(),
							desc: String(f.get('desc') ?? '').trim(),
							labels: parsed,
							// Replaced whole, so the checkbox sends the whole
							// message. `password: true` and not "unset" once
							// somebody has touched the box: unset means the
							// default and this is now a decision.
							config: { password: f.get('password') !== null },
						})
						.then(() => tell({ kind: 'done', text: 'saved' }))
						.catch((e: unknown) => tell({ kind: 'bad', text: said(e) }))
				}}
			>
				<input name="name" placeholder="name" defaultValue={t.name} />
				<input name="desc" placeholder="note" defaultValue={t.desc} />
				<textarea name="labels" placeholder={'labels, one per line: brand=Contoso'} defaultValue={labels} rows={3} />
				{/*
					A way in, and not a screen setting: roster refuses a password
					for a tenant with this off, so what the sign-in pages draw is
					what is already true. For an operator whose people all arrive
					through a directory -- a form nobody uses is a form that says
					somebody here has a password.
				*/}
				<label className="check">
					<input type="checkbox" name="password" defaultChecked={t.config?.password ?? true} />
					a password is a way in here
				</label>
				<button type="submit" disabled={update.state === 'pending' || !mayWrite}>
					save
				</button>
			</form>
			{told?.kind === 'done' && <p className="note">{told.text}</p>}
			{told?.kind === 'bad' && <p className="bad">{told.text}</p>}
		</>
	)
}
