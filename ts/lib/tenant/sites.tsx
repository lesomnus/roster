/**
 * A tenant's sites, the teams in them, and who is in each.
 *
 * A **site** is a place inside one tenant and a **team** is inside a site, which
 * is why teams are drawn in a site's panel rather than as a screen of their own:
 * `Team` has no meaning without the site it is in.
 *
 * # What a membership hands out, said on the screen
 *
 * A `TeamMembership` may name a role, so it is a **grant** -- as much as
 * `Binding.Add` is (`server/core/escalate.go`: *a grant is any write that changes
 * what the gate will answer for somebody*). The server refuses one that hands out
 * more than the caller holds; this page's part is to say what the button does,
 * beside the button.
 *
 * # Reads through the store, writes through `useCall`
 *
 * Every list here is narrowed by what it asks for -- a tenant, a site, a team --
 * which is what the filters that grew in P2a are for: on the admin port there is
 * no wall, and a list of *all* sites is every tenant's.
 *
 * @module
 */

import { useState } from 'react'
import { useCall, useQuery } from '@lesomnus/payday/react'

import { SiteMembershipService, TeamMembershipService } from '#gen/app/membership_svc_pb.js'
import { RoleService } from '#gen/app/role_svc_pb.js'
import { SiteService } from '#gen/app/site_svc_pb.js'
import { TeamService } from '#gen/app/team_svc_pb.js'

import { Alias, PickHolder, RoleName, bytesOf, ref, said, uuid, type May } from './parts.js'
import { Bar, Fill, Menu, Sheet } from '#lib/ui.js'

export function Sites(props: {
	tenant: { id?: Uint8Array; alias?: string } | undefined
	may: May
}): React.ReactNode {
	const id = props.tenant?.id
	if (id === undefined) return null

	return (
		<section className="within">
			<h3>{props.tenant?.alias}</h3>
			<SiteList tenant={id} may={props.may} />
		</section>
	)
}

function SiteList(props: { tenant: Uint8Array; may: May }): React.ReactNode {
	const vs = useQuery(SiteService.method.list, { filters: [{ tenant: ref(props.tenant) }] })
	const add = useCall(SiteService.method.add)
	const erase = useCall(SiteService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [at, go] = useState<string | null>(null)
	const [bad, setBad] = useState<string | null>(null)
	const [adding, openAdd] = useState(false)

	// Over what was read; `SiteService` has no `Search`.
	const [find, setFind] = useState('')

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const all = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))
	const q = find.trim().toLowerCase()
	const items = q === '' ? all : all.filter((v) => `${v.alias} ${v.name}`.toLowerCase().includes(q))

	return (
		<section>
			<h4>sites</h4>
			<p className="note">
				A site is the set a role can be scoped to — a binding made in a site
				reaches that site, one made without reaches the whole tenant. Teams
				live in a site; people are members of one.
			</p>

			<Bar>
				<input
					className="find"
					type="search"
					aria-label="filter sites"
					placeholder={`filter these ${all.length}`}
					value={find}
					onChange={(e) => setFind(e.target.value)}
				/>
				<Fill />
				<button
					className="add"
					disabled={!props.may('/roster.SiteService/Add')}
					onClick={() => openAdd(true)}
				>
					add site
				</button>
			</Bar>

			{items.length === 0 && (
				<p className="none">
					{all.length === 0 ? 'no sites — every role is tenant-wide' : 'none of these match'}
				</p>
			)}
			{items.length > 0 && (
				<table>
					<tbody>
						{items.map((v) => {
							const k = uuid(v.id)

							return (
								<tr key={k} className={k === at ? 'at' : ''}>
									<td>
										{v.alias}
										<span className="under">{v.name}</span>
									</td>
									<td className="acts">
										<button onClick={() => go(k === at ? null : k)}>
											{k === at ? 'hide' : 'open'}
										</button>
										<Menu label={`more for ${v.alias}`}>
											<button
												className="danger"
												disabled={!props.may('/roster.SiteService/Erase')}
												onClick={() => {
													setBad(null)
													void erase
														.call(ref(v.id))
														.then(() => setGone((was) => [...was, k]))
														.catch((e: unknown) => setBad(said(e)))
												}}
											>
												remove site
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

			<Sheet at={adding} onClose={() => openAdd(false)} title="add a site" bad={bad}>
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
				<input name="alias" placeholder="site alias" required />
				<input name="name" placeholder="name (optional)" />
				<button type="submit" disabled={add.state === 'pending' || !props.may('/roster.SiteService/Add')}>
					add site
				</button>
			</form>
			</Sheet>

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
	const [adding, openAdd] = useState(false)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section>
			<h6>members</h6>
			<Bar>
				<Fill />
				<button
					className="add"
					disabled={!props.may('/roster.SiteMembershipService/Add')}
					onClick={() => openAdd(true)}
				>
					add somebody
				</button>
			</Bar>
			{items.length === 0 && <p className="none">nobody in this site</p>}
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
											disabled={!props.may('/roster.SiteMembershipService/Erase')}
											onClick={() => {
												setBad(null)
												void erase
													.call(ref(v.id))
													.then(() => setGone((was) => [...was, uuid(v.id)]))
													.catch((e: unknown) => setBad(said(e)))
											}}
										>
											take out of site
										</button>
									</Menu>
								</td>
							</tr>
						))}
					</tbody>
				</table>
			)}
			<Sheet at={adding} onClose={() => openAdd(false)} title="put somebody in this site" bad={bad}>
			<form
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const who = bytesOf(String(new FormData(form).get('who') ?? ''))
					if (who === undefined) return

					setBad(null)
					void add
						.call({ holder: ref(who), site: ref(props.site) })
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
					disabled={add.state === 'pending' || !props.may('/roster.SiteMembershipService/Add')}
				>
					add to site
				</button>
			</form>
			</Sheet>
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
	const [adding, openAdd] = useState(false)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section>
			<h6>teams</h6>
			<Bar>
				<Fill />
				<button
					className="add"
					disabled={!props.may('/roster.TeamService/Add')}
					onClick={() => openAdd(true)}
				>
					add team
				</button>
			</Bar>
			{items.length === 0 && <p className="none">no teams in this site</p>}
			{items.length > 0 && (
				<table>
					<tbody>
						{items.map((v) => {
							const k = uuid(v.id)

							return (
								<tr key={k} className={k === at ? 'at' : ''}>
									<td>
										{v.alias}
										<span className="under">{v.name}</span>
									</td>
									<td className="acts">
										<button onClick={() => go(k === at ? null : k)}>
											{k === at ? 'hide' : 'members'}
										</button>
										<Menu label={`more for ${v.alias}`}>
											<button
												className="danger"
												disabled={!props.may('/roster.TeamService/Erase')}
												onClick={() => {
													setBad(null)
													void erase
														.call(ref(v.id))
														.then(() => setGone((was) => [...was, k]))
														.catch((e: unknown) => setBad(said(e)))
												}}
											>
												remove team
											</button>
										</Menu>
									</td>
								</tr>
							)
						})}
					</tbody>
				</table>
			)}
			<Sheet at={adding} onClose={() => openAdd(false)} title="add a team" bad={bad}>
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
						.then(() => {
							form.reset()
							openAdd(false)
						})
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<input name="alias" placeholder="team alias" required />
				<input name="name" placeholder="name (optional)" />
				<button type="submit" disabled={add.state === 'pending' || !props.may('/roster.TeamService/Add')}>
					add team
				</button>
			</form>
			</Sheet>
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
	const [adding, openAdd] = useState(false)

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
			<Bar>
				<Fill />
				<button
					className="add"
					disabled={!props.may('/roster.TeamMembershipService/Add')}
					onClick={() => openAdd(true)}
				>
					add somebody
				</button>
			</Bar>

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
								<td className="acts">
									<Menu label="more for this member">
										<button
											className="danger"
											disabled={!props.may('/roster.TeamMembershipService/Erase')}
											onClick={() => {
												setBad(null)
												void erase
													.call(ref(v.id))
													.then(() => setGone((was) => [...was, uuid(v.id)]))
													.catch((e: unknown) => setBad(said(e)))
											}}
										>
											take out of team
										</button>
									</Menu>
								</td>
							</tr>
						))}
					</tbody>
				</table>
			)}
			<Sheet at={adding} onClose={() => openAdd(false)} title="put somebody in this team" bad={bad}>
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
						.then(() => {
							form.reset()
							openAdd(false)
						})
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
			</Sheet>
			{bad !== null && <p className="bad">{bad}</p>}
		</section>
	)
}
