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
import { Bar, Fill, Menu, Sheet } from '#lib/ui.js'

export function Groups(props: {
	tenant: { id?: Uint8Array; alias?: string } | undefined
	may: May
}): React.ReactNode {
	const id = props.tenant?.id
	if (id === undefined) return null

	return (
		<section className="within">
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
	const [adding, openAdd] = useState(false)

	// Over what was read, and the placeholder says so. `GroupService` has no
	// `Search`, so this narrows the page it already has rather than pretending to
	// be a search of the table -- the same argument `ts/console/tenants.tsx` makes,
	// and the same reason it is said out loud instead of left to be discovered.
	const [find, setFind] = useState('')

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const all = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))
	const q = find.trim().toLowerCase()
	const items = q === '' ? all : all.filter((v) => `${v.alias} ${v.name}`.toLowerCase().includes(q))

	return (
		<section>
			<h4>groups</h4>
			<p className="note">
				A group is somewhere a binding can be written instead of to a person.
				Putting somebody in a group hands them <strong>everything bound to it</strong>{' '}
				— it is a grant as much as a binding is, and the server refuses one that
				hands out more than you hold.
			</p>

			<Bar>
				<input
					className="find"
					type="search"
					aria-label="filter groups"
					placeholder={`filter these ${all.length}`}
					value={find}
					onChange={(e) => setFind(e.target.value)}
				/>
				<Fill />
				<button
					className="add"
					disabled={!props.may('/roster.GroupService/Add')}
					onClick={() => openAdd(true)}
				>
					add group
				</button>
			</Bar>

			{items.length === 0 && (
				<p className="none">{all.length === 0 ? 'no groups' : 'none of these match'}</p>
			)}
			{items.length > 0 && (
				<table>
					<tbody>
						{items.map((v) => {
							const k = uuid(v.id)

							return (
								<tr key={k} className={k === at ? 'at' : ''}>
									{/* Two lines, because the alias is what a binding is written
									    to and the name is what a person calls it: one is copied
									    and one is read. */}
									<td>
										{v.alias}
										{/* Always drawn, empty or not: a row with a name and one
										    without are the same height, so the list does not go
										    ragged on whether somebody filled a field in. */}
										<span className="under">{v.name}</span>
									</td>
									<td className="acts">
										<button onClick={() => go(k === at ? null : k)}>
											{k === at ? 'hide' : 'members'}
										</button>
										<Menu label={`more for ${v.alias}`}>
											<button
												className="danger"
												disabled={!props.may('/roster.GroupService/Erase')}
												onClick={() => {
													setBad(null)
													void erase
														.call(ref(v.id))
														.then(() => setGone((was) => [...was, k]))
														.catch((e: unknown) => setBad(said(e)))
												}}
											>
												remove group
											</button>
										</Menu>
									</td>
								</tr>
							)
						})}
					</tbody>
				</table>
			)}
			{bad !== null && <p className="bad">{bad}</p>}

			<Sheet at={adding} onClose={() => openAdd(false)} title="add a group" bad={bad}>
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
							.then(() => {
								form.reset()
								openAdd(false)
							})
							.catch((e: unknown) => setBad(said(e)))
					}}
				>
					<input name="alias" placeholder="group alias" required />
					<input name="name" placeholder="name (optional)" />
					<button type="submit" disabled={add.state === 'pending' || !props.may('/roster.GroupService/Add')}>
						add group
					</button>
				</form>
			</Sheet>

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
	const [adding, openAdd] = useState(false)

	if (props.group?.id === undefined) return null
	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section className="within">
			<h6>{props.group?.alias} — members</h6>
			<Bar>
				<Fill />
				<button
					className="add"
					disabled={!props.may('/roster.GroupMembershipService/Add')}
					onClick={() => openAdd(true)}
				>
					add somebody
				</button>
			</Bar>
			{items.length === 0 && <p className="none">nobody in this group</p>}
			{items.length > 0 && (
				<table>
					<tbody>
						{items.map((v) => (
							<tr key={uuid(v.id)}>
								<td>
									<Alias id={v.holder?.id} />
								</td>
								<td className="acts">
									<Menu label="more for this member">
										<button
											className="danger"
											disabled={!props.may('/roster.GroupMembershipService/Erase')}
											onClick={() => {
												setBad(null)
												void erase
													.call(ref(v.id))
													.then(() => setGone((was) => [...was, uuid(v.id)]))
													.catch((e: unknown) => setBad(said(e)))
											}}
										>
											take out of group
										</button>
									</Menu>
								</td>
							</tr>
						))}
					</tbody>
				</table>
			)}
			{bad !== null && <p className="bad">{bad}</p>}

			<Sheet at={adding} onClose={() => openAdd(false)} title={`put somebody in ${props.group?.alias ?? 'this group'}`} bad={bad}>
				<form
					onSubmit={(e) => {
						e.preventDefault()
						const form = e.currentTarget
						const who = bytesOf(String(new FormData(form).get('who') ?? ''))
						if (who === undefined) return

						setBad(null)
						void add
							.call({ holder: ref(who), group: ref(group) })
							.then(() => {
								form.reset()
								openAdd(false)
							})
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
			</Sheet>
		</section>
	)
}
