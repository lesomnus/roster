/**
 * A tenant's groups, and who is in each.
 *
 * A **group** is a set of people that bindings are written to, so putting somebody
 * in one puts them under every binding the group holds. That makes it a **grant**
 * -- `server/core/escalate.go`: *a grant is any write that changes what the gate
 * will answer for somebody* -- and the server refuses one that hands out more than
 * the caller holds. This screen's part is to say what the button does, beside the
 * button, so nobody presses *add to group* thinking it is a directory.
 *
 * Unlike a team, a group is the tenant's rather than a site's, which is why it is
 * a screen of its own and teams are drawn inside a site.
 *
 * @module
 */

import { useState } from 'react'
import { useCall, useQuery } from '@lesomnus/payday/react'

import { GroupMembershipService, GroupService } from '#gen/app/group_svc_pb.js'

import { Alias, PickHolder, bytesOf, ref, said, uuid, type May } from './parts.js'

export function Groups(props: {
	tenant: { id?: Uint8Array; alias?: string } | undefined
	may: May
}): React.ReactNode {
	const id = props.tenant?.id
	if (id === undefined) return null

	return (
		<section className="within organisation">
			<h3>{props.tenant?.alias}</h3>
			<GroupList tenant={id} may={props.may} />
		</section>
	)
}

function GroupList(props: { tenant: Uint8Array; may: May }): React.ReactNode {
	const vs = useQuery(GroupService.method.list, { filters: [{ tenant: ref(props.tenant) }] })
	const add = useCall(GroupService.method.add)
	const erase = useCall(GroupService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [at, go] = useState<string | null>(null)
	const [bad, setBad] = useState<string | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section>
			<h4>groups</h4>
			<p className="note">
				A group is somewhere a binding can be written instead of to a person.
				Putting somebody in a group hands them <strong>everything bound to it</strong>{' '}
				— it is a grant as much as a binding is, and the server refuses one that
				hands out more than you hold.
			</p>
			{items.length === 0 && <p className="none">no groups</p>}
			{items.length > 0 && (
				<table>
					<tbody>
						{items.map((v) => {
							const k = uuid(v.id)

							return (
								<tr key={k} className={k === at ? 'at' : ''}>
									<td>{v.alias}</td>
									<td>{v.name}</td>
									<td className="acts">
										<button onClick={() => go(k === at ? null : k)}>
											{k === at ? 'hide' : 'members'}
										</button>
										<button
											disabled={!props.may('/roster.GroupService/Erase')}
											onClick={() => {
												setBad(null)
												void erase
													.call(ref(v.id))
													.then(() => setGone((was) => [...was, k]))
													.catch((e: unknown) => setBad(said(e)))
											}}
										>
											remove
										</button>
									</td>
								</tr>
							)
						})}
					</tbody>
				</table>
			)}
			<form
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const f = new FormData(form)
					const alias = String(f.get('alias') ?? '').trim()
					if (alias === '') return

					setBad(null)
					void add
						.call({ tenant: ref(props.tenant), alias, name: String(f.get('name') ?? '').trim() })
						.then(() => form.reset())
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<input name="alias" placeholder="group alias" required />
				<input name="name" placeholder="name (optional)" />
				<button type="submit" disabled={add.state === 'pending' || !props.may('/roster.GroupService/Add')}>
					add group
				</button>
			</form>
			{bad !== null && <p className="bad">{bad}</p>}

			{at !== null && (
				<GroupMembers
					tenant={props.tenant}
					group={items.find((v) => uuid(v.id) === at)}
					may={props.may}
				/>
			)}
		</section>
	)
}

function GroupMembers(props: {
	tenant: Uint8Array
	group: { id?: Uint8Array; alias?: string } | undefined
	may: May
}): React.ReactNode {
	const group = props.group?.id ?? new Uint8Array()
	const vs = useQuery(GroupMembershipService.method.list, {
		filters: [{ group: ref(group) }],
	})
	const add = useCall(GroupMembershipService.method.add)
	const erase = useCall(GroupMembershipService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [bad, setBad] = useState<string | null>(null)

	if (props.group?.id === undefined) return null
	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section className="within">
			<h6>{props.group?.alias} — members</h6>
			{items.length === 0 && <p className="none">nobody in this group</p>}
			{items.length > 0 && (
				<table>
					<tbody>
						{items.map((v) => (
							<tr key={uuid(v.id)}>
								<td>
									<Alias id={v.holder?.id} />
								</td>
								<td>
									<button
										disabled={!props.may('/roster.GroupMembershipService/Erase')}
										onClick={() => {
											setBad(null)
											void erase
												.call(ref(v.id))
												.then(() => setGone((was) => [...was, uuid(v.id)]))
												.catch((e: unknown) => setBad(said(e)))
										}}
									>
										remove
									</button>
								</td>
							</tr>
						))}
					</tbody>
				</table>
			)}
			<form
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const who = bytesOf(String(new FormData(form).get('who') ?? ''))
					if (who === undefined) return

					setBad(null)
					void add
						.call({ holder: ref(who), group: ref(group) })
						.then(() => form.reset())
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<PickHolder tenant={props.tenant} name="who" />
				<button
					type="submit"
					disabled={add.state === 'pending' || !props.may('/roster.GroupMembershipService/Add')}
				>
					add to group
				</button>
			</form>
			{bad !== null && <p className="bad">{bad}</p>}
		</section>
	)
}
