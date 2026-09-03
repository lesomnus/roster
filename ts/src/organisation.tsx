/**
 * One customer's organisation: sites, the teams and groups in them, and who is
 * in each.
 *
 * # What a membership hands out, said on the screen
 *
 * A `TeamMembership` may name a role, and a `GroupMembership` puts somebody
 * under every binding written to the group -- so both are **grants**, as much
 * as `Binding.Add` is (`server/core/escalate.go`: *a grant is any write that
 * changes what the gate will answer for somebody*). The server refuses one that
 * hands out more than the operator holds; this page's part is to say what the
 * button does, beside the button, so nobody presses "add to group" thinking it
 * is a directory.
 *
 * # Reads through the store, writes through `useCall`
 *
 * Every list here is narrowed by what it asks for -- a tenant, a site, a team,
 * a group -- which is what the filters that grew in P2a are for: on the admin
 * port there is no wall, and a list of *all* sites is every customer's.
 */

import { useState } from 'react'
import { useCall, useQuery, useRow } from '@lesomnus/payday/react'

import { GroupMembershipService, GroupService } from '../gen/app/group_svc_pb.js'
import { SiteMembershipService, TeamMembershipService } from '../gen/app/membership_svc_pb.js'
import { RoleService } from '../gen/app/role_svc_pb.js'
import { SiteService } from '../gen/app/site_svc_pb.js'
import { TeamService } from '../gen/app/team_svc_pb.js'
import type { Role } from '../gen/app/role_pb.js'
import type { Holder } from '../gen/roster/payday/holder_pb.js'
import { HolderService } from '../gen/roster/payday/holder_svc_pb.js'

/** uuid is the bytes an identifier arrives as, written the way a person reads one. */
export function uuid(v: Uint8Array | undefined): string {
	if (v === undefined || v.length !== 16) return ''

	const h = [...v].map((b) => b.toString(16).padStart(2, '0')).join('')

	return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

export function said(e: unknown): string {
	return e instanceof Error ? e.message : 'no'
}

export const ref = (id: Uint8Array) => ({ key: { case: 'id' as const, value: id } })

type May = (method: string) => boolean

/**
 * Alias draws a holder by name, from the store if the person is there and by
 * identifier if not -- a listed row names its holder and does not carry it.
 */
export function Alias(props: { id: Uint8Array | undefined }): React.ReactNode {
	const v = useRow<Holder>('roster.Holder', props.id)
	if (props.id === undefined) return <span className="none">nobody</span>

	return v?.alias !== undefined && v.alias !== '' ? (
		<span>{v.alias}</span>
	) : (
		<span className="mono dim">{uuid(props.id).slice(0, 8)}</span>
	)
}

/** RoleName draws a role by alias, from the store, or by identifier until it is there. */
function RoleName(props: { id: Uint8Array | undefined }): React.ReactNode {
	const v = useRow<Role>('roster.Role', props.id)
	if (props.id === undefined) return <span className="none">no role</span>

	return v?.alias !== undefined && v.alias !== '' ? (
		<span>{v.alias}</span>
	) : (
		<span className="mono dim">{uuid(props.id).slice(0, 8)}</span>
	)
}

/**
 * PickHolder is a choice among the tenant's people, by alias, answering the
 * identifier -- so a form never takes a name the server would have to resolve.
 */
export function PickHolder(props: { tenant: Uint8Array; name: string }): React.ReactNode {
	const vs = useQuery(HolderService.method.list, {
		filters: [{ tenant: ref(props.tenant) }],
	})
	const items = vs.data?.items ?? []

	return (
		<select name={props.name} defaultValue="" required>
			<option value="" disabled>
				who
			</option>
			{items.map((v) => (
				<option key={uuid(v.id)} value={uuid(v.id)}>
					{v.alias}
				</option>
			))}
		</select>
	)
}

/** bytesOf is a uuid string back to the sixteen bytes the wire carries. */
export function bytesOf(v: string): Uint8Array | undefined {
	const h = v.replaceAll('-', '')
	if (!/^[0-9a-fA-F]{32}$/.test(h)) return undefined

	const b = new Uint8Array(16)
	for (let i = 0; i < 16; i++) b[i] = Number.parseInt(h.slice(i * 2, i * 2 + 2), 16)

	return b
}

export function Organisation(props: {
	tenant: { id?: Uint8Array; alias?: string } | undefined
	may: May
}): React.ReactNode {
	const id = props.tenant?.id
	if (id === undefined) return null

	return (
		<section className="within organisation">
			<h3>{props.tenant?.alias} — organisation</h3>
			<Sites tenant={id} may={props.may} />
			<Groups tenant={id} may={props.may} />
		</section>
	)
}

function Sites(props: { tenant: Uint8Array; may: May }): React.ReactNode {
	const vs = useQuery(SiteService.method.list, { filters: [{ tenant: ref(props.tenant) }] })
	const add = useCall(SiteService.method.add)
	const erase = useCall(SiteService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [at, go] = useState<string | null>(null)
	const [bad, setBad] = useState<string | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section>
			<h4>sites</h4>
			<p className="note">
				A site is the set a role can be scoped to — a binding made in a site
				reaches that site, one made without reaches the whole tenant. Teams
				live in a site; people are members of one.
			</p>

			{items.length === 0 && <p className="none">no sites — every role is tenant-wide</p>}
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
											{k === at ? 'hide' : 'open'}
										</button>
										<button
											disabled={!props.may('/roster.SiteService/Erase')}
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
				<input name="alias" placeholder="site alias" required />
				<input name="name" placeholder="name (optional)" />
				<button type="submit" disabled={add.state === 'pending' || !props.may('/roster.SiteService/Add')}>
					add site
				</button>
			</form>
			{bad !== null && <p className="bad">{bad}</p>}

			{at !== null && (
				<Site
					tenant={props.tenant}
					site={items.find((v) => uuid(v.id) === at)}
					may={props.may}
				/>
			)}
		</section>
	)
}

function Site(props: {
	tenant: Uint8Array
	site: { id?: Uint8Array; alias?: string } | undefined
	may: May
}): React.ReactNode {
	const id = props.site?.id
	if (id === undefined) return null

	return (
		<section className="within">
			<h5>{props.site?.alias}</h5>
			<SiteMembers tenant={props.tenant} site={id} may={props.may} />
			<Teams tenant={props.tenant} site={id} may={props.may} />
		</section>
	)
}

function SiteMembers(props: { tenant: Uint8Array; site: Uint8Array; may: May }): React.ReactNode {
	const vs = useQuery(SiteMembershipService.method.list, {
		filters: [{ site: ref(props.site) }],
	})
	const add = useCall(SiteMembershipService.method.add)
	const erase = useCall(SiteMembershipService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [bad, setBad] = useState<string | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section>
			<h6>members</h6>
			{items.length === 0 && <p className="none">nobody in this site</p>}
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
										disabled={!props.may('/roster.SiteMembershipService/Erase')}
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
						.call({ holder: ref(who), site: ref(props.site) })
						.then(() => form.reset())
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<PickHolder tenant={props.tenant} name="who" />
				<button
					type="submit"
					disabled={add.state === 'pending' || !props.may('/roster.SiteMembershipService/Add')}
				>
					add to site
				</button>
			</form>
			{bad !== null && <p className="bad">{bad}</p>}
		</section>
	)
}

function Teams(props: { tenant: Uint8Array; site: Uint8Array; may: May }): React.ReactNode {
	const vs = useQuery(TeamService.method.list, { filters: [{ site: ref(props.site) }] })
	const add = useCall(TeamService.method.add)
	const erase = useCall(TeamService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [at, go] = useState<string | null>(null)
	const [bad, setBad] = useState<string | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section>
			<h6>teams</h6>
			{items.length === 0 && <p className="none">no teams in this site</p>}
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
											disabled={!props.may('/roster.TeamService/Erase')}
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
						.call({
							tenant: ref(props.tenant),
							site: ref(props.site),
							alias,
							name: String(f.get('name') ?? '').trim(),
						})
						.then(() => form.reset())
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<input name="alias" placeholder="team alias" required />
				<input name="name" placeholder="name (optional)" />
				<button type="submit" disabled={add.state === 'pending' || !props.may('/roster.TeamService/Add')}>
					add team
				</button>
			</form>
			{bad !== null && <p className="bad">{bad}</p>}

			{at !== null && (
				<TeamMembers
					tenant={props.tenant}
					team={items.find((v) => uuid(v.id) === at)}
					may={props.may}
				/>
			)}
		</section>
	)
}

function TeamMembers(props: {
	tenant: Uint8Array
	team: { id?: Uint8Array; alias?: string } | undefined
	may: May
}): React.ReactNode {
	const team = props.team?.id ?? new Uint8Array()
	const vs = useQuery(TeamMembershipService.method.list, {
		filters: [{ team: ref(team) }],
	})
	const roles = useQuery(RoleService.method.list, { filters: [{ tenant: ref(props.tenant) }] })
	const add = useCall(TeamMembershipService.method.add)
	const erase = useCall(TeamMembershipService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [bad, setBad] = useState<string | null>(null)

	if (props.team?.id === undefined) return null
	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))
	const rs = roles.data?.items ?? []

	return (
		<section className="within">
			<h6>{props.team?.alias} — members</h6>
			<p className="note">
				A membership may name a role, and then it is a <strong>grant</strong>: the
				person holds that role in this team's site. The server refuses one that
				hands out more than you hold.
			</p>
			{items.length === 0 && <p className="none">nobody in this team</p>}
			{items.length > 0 && (
				<table>
					<tbody>
						{items.map((v) => (
							<tr key={uuid(v.id)}>
								<td>
									<Alias id={v.holder?.id} />
								</td>
								<td className="mono">
									<RoleName id={v.role?.id} />
								</td>
								<td>
									<button
										disabled={!props.may('/roster.TeamMembershipService/Erase')}
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
					const f = new FormData(form)
					const who = bytesOf(String(f.get('who') ?? ''))
					if (who === undefined) return
					const role = bytesOf(String(f.get('role') ?? ''))

					setBad(null)
					void add
						.call(
							role === undefined
								? { holder: ref(who), team: ref(team) }
								: { holder: ref(who), team: ref(team), role: ref(role) },
						)
						.then(() => form.reset())
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<PickHolder tenant={props.tenant} name="who" />
				<select name="role" defaultValue="">
					<option value="">no role — a member and nothing more</option>
					{rs.map((r) => (
						<option key={uuid(r.id)} value={uuid(r.id)}>
							{r.alias}
						</option>
					))}
				</select>
				<button
					type="submit"
					disabled={add.state === 'pending' || !props.may('/roster.TeamMembershipService/Add')}
				>
					add to team
				</button>
			</form>
			{bad !== null && <p className="bad">{bad}</p>}
		</section>
	)
}

function Groups(props: { tenant: Uint8Array; may: May }): React.ReactNode {
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
