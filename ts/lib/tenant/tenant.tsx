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
 * # A tenant a file declares
 *
 * Is the file's in what the file says of it: its name, its note, its labels and
 * each key of its settings the file says, which the row lists in
 * `roster.declared.config` (`server/core/tenant.go`, `mayUpdateDeclared`). Those
 * are shown and not edited here, saying which file, and go back as they were
 * read, which is no change; the labels do not go back at all, since they carry
 * `roster.declared`, which no caller sends. The settings the file leaves out
 * are the tenant's and are edited as any tenant's are. A declared tenant
 * without that label was written by a build before it, and every setting of it
 * is the file's.
 *
 * @module
 */

import { useState } from 'react'
import { useCall, useQuery } from '@lesomnus/payday/react'

import { TenantService } from '#gen/roster/payday/tenant_svc_pb.js'

import { ref, said, uuid, when, type May } from './parts.js'

// The labels a tenant a file declares carries: which file, and which keys of
// its settings the file says (`rstr/tenant.go`). Written by what applies the
// file, and by no caller.
const declared = 'roster.declared'
const declaredConfig = 'roster.declared.config'

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

	// What a file says of this tenant, and which file: see the top of this file.
	const from = t.labels?.[declared]
	const keys = t.labels?.[declaredConfig]
	const files = (key: string): boolean =>
		from !== undefined && (keys === undefined || keys.split(',').includes(key))
	const fileSays = from === undefined ? undefined : `declared in ${from}, and changed there`
	const ofFile = (key: string): string | undefined => (files(key) ? fileSays : undefined)

	return (
		<>
			{/* The identity first, and read-only because none of it is a field: the
			    alias is how every reference to this tenant is written, and the dates
			    are stamps. A screen that offered to edit them would be offering a
			    write the server refuses. */}
			<h4>what it is</h4>
			<table className="facts">
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
			{from !== undefined && (
				<p className="note">
					declared in <code>{from}</code>: what it says is shown here and changed there, and the
					rest is this tenant&apos;s
				</p>
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
					tell(null)
					void update
						.call({
							ref: ref(t.id),
							dateUpdated: t.dateUpdated,
							name: from !== undefined ? t.name : String(f.get('name') ?? '').trim(),
							desc: from !== undefined ? t.desc : String(f.get('desc') ?? '').trim(),
							// Empty is left as it is, which a declared
							// tenant's are.
							labels: from !== undefined ? {} : parsed,
							// Replaced whole, so the checkbox sends the whole
							// message. `password: true` and not "unset" once
							// somebody has touched the box: unset means the
							// default and this is now a decision. What the
							// file says goes back as it was read.
							config: {
								// Unset where the file says it and the row has
								// it unset: unset is a value of its own.
								...(!files('password')
									? { password: f.get('password') !== null }
									: t.config?.password === undefined
										? {}
										: { password: t.config.password }),
								frontDoor: files('front_door')
									? (t.config?.frontDoor ?? '')
									: String(f.get('front_door') ?? '').trim(),
								// The Slack reference goes back as it was read:
								// it names one of the deployment's secrets, and
								// roster refuses a new one from anybody but the
								// deployment (`tenant.ext.proto`).
								profile: {
									fill: files('profile.fill') ? (t.config?.profile?.fill ?? false) : f.get('fill') !== null,
									slackSecretRef: t.config?.profile?.slackSecretRef ?? '',
								},
								leave: files('leave') ? (t.config?.leave ?? false) : f.get('leave') !== null,
							},
						})
						.then(() => tell({ kind: 'done', text: 'saved' }))
						.catch((e: unknown) => tell({ kind: 'bad', text: said(e) }))
				}}
			>
				<input name="name" placeholder="name" defaultValue={t.name} readOnly={from !== undefined} title={fileSays} />
				<input name="desc" placeholder="note" defaultValue={t.desc} readOnly={from !== undefined} title={fileSays} />
				<textarea
					name="labels"
					placeholder={'labels, one per line: brand=Contoso'}
					defaultValue={labels}
					rows={3}
					readOnly={from !== undefined}
					title={fileSays}
				/>
				{/*
					A way in, and not a screen setting: roster refuses a password
					for a tenant with this off, so what the sign-in pages draw is
					what is already true. For an operator whose people all arrive
					through a directory -- a form nobody uses is a form that says
					somebody here has a password.
				*/}
				<label className="check" title={ofFile('password')}>
					<input
						type="checkbox"
						name="password"
						defaultChecked={t.config?.password ?? true}
						disabled={files('password')}
					/>
					a password is a way in here
				</label>
				{/*
					Where this tenant's people sign in through a directory, as an
					origin. The user console sends a browser there for a provider,
					and roster refuses to hand anybody into this tenant's console
					until one is named -- so this too is a fact roster acts on and
					not a screen setting (`tenant.ext.proto`). Empty is no front
					door, which is what a tenant whose people all have passwords is.
				*/}
				<input
					name="front_door"
					placeholder="front door: https://account.contoso.example"
					defaultValue={t.config?.frontDoor ?? ''}
					readOnly={files('front_door')}
					title={ofFile('front_door')}
				/>
				{/*
					What a sign-in through a directory fills a profile with, where
					it has nothing: the directory's name and picture, or the
					tenant's Slack workspace's when the deployment named one. The
					front doors read it at every sign-in.
				*/}
				<label className="check" title={ofFile('profile.fill')}>
					<input
						type="checkbox"
						name="fill"
						defaultChecked={t.config?.profile?.fill ?? false}
						disabled={files('profile.fill')}
					/>
					a sign-in fills a blank name and picture
					{(t.config?.profile?.slackSecretRef ?? '') !== '' ? ', from Slack' : ', from the directory'}
				</label>
				{/*
					Whether people here may delete their own account
					(`Me.Leave`). Off unless somebody turns it on: a directory a
					company provisions is the company's to empty, and roster
					refuses the call while this is off.
				*/}
				<label className="check" title={ofFile('leave')}>
					<input type="checkbox" name="leave" defaultChecked={t.config?.leave ?? false} disabled={files('leave')} />
					people here may delete their own account
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
