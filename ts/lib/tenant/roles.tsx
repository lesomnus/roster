/**
 * One customer's access: the roles, and who each is bound to.
 *
 * # A role is a list of methods, and that is the whole model
 *
 * There is no object rule here to draw. A role names methods -- as patterns,
 * `/roster.HolderService/*` -- and a binding hands the role to a person or a
 * group, tenant-wide or in one site. What somebody *effectively* holds is the
 * union over their bindings, their groups' bindings and their teams' roles,
 * which `Holder.Reaches` answers beside the holder (`holders.tsx`) rather than
 * this page adding it up.
 *
 * # Every write here is a grant
 *
 * `Role.Add`, `Binding.Add` -- `server/core/escalate.go` refuses each when it
 * hands out a method the operator does not hold, or binds a role somewhere it
 * may not go. This page does not pre-check that; it says what the server said.
 *
 * # Bindings are listed under their role
 *
 * `Binding` is walled through its role's tenant and lists by role, holder,
 * group or site -- not by tenant, because a binding has no tenant of its own.
 * So each role draws its own bindings, which is also how an operator reads
 * them: *who has this*.
 */

import { useState } from 'react'
import { useCall, useQuery, useRow } from '@lesomnus/payday/react'

import type { Group } from '#gen/app/group_pb.js'
import { GroupService } from '#gen/app/group_svc_pb.js'
import { BindingService, RoleService } from '#gen/app/role_svc_pb.js'
import type { Site } from '#gen/app/site_pb.js'
import { SiteService } from '#gen/app/site_svc_pb.js'

import { Alias, PickHolder, bytesOf, ref, said, uuid, type May } from './parts.js'
import { Bar, Fill, Menu, Sheet } from '#lib/ui.js'

export function Roles(props: {
	tenant: { id?: Uint8Array; alias?: string } | undefined
	may: May
}): React.ReactNode {
	const id = props.tenant?.id
	if (id === undefined) return null

	return (
		<section className="within">
			<h3>{props.tenant?.alias}</h3>
			<RoleList tenant={id} may={props.may} />
		</section>
	)
}

function SiteName(props: { id: Uint8Array | undefined }): React.ReactNode {
	const v = useRow<Site>('roster.Site', props.id)
	if (props.id === undefined) return <span className="dim">whole tenant</span>

	return <span>{v?.alias ?? uuid(props.id).slice(0, 8)}</span>
}

function GroupName(props: { id: Uint8Array | undefined }): React.ReactNode {
	const v = useRow<Group>('roster.Group', props.id)

	return <span>group {v?.alias ?? uuid(props.id).slice(0, 8)}</span>
}

function RoleList(props: { tenant: Uint8Array; may: May }): React.ReactNode {
	const vs = useQuery(RoleService.method.list, {
		filters: [{ tenant: ref(props.tenant) }],
	})
	const sites = useQuery(SiteService.method.list, { filters: [{ tenant: ref(props.tenant) }] })
	const add = useCall(RoleService.method.add)
	const erase = useCall(RoleService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [at, go] = useState<string | null>(null)
	const [bad, setBad] = useState<string | null>(null)
	const [adding, openAdd] = useState(false)

	// Over what was read; `RoleService` has no `Search`.
	const [find, setFind] = useState('')

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const all = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))
	const q = find.trim().toLowerCase()
	const items =
		q === '' ? all : all.filter((v) => `${v.alias} ${v.desc} ${v.methods.join(' ')}`.toLowerCase().includes(q))
	const ss = sites.data?.items ?? []

	return (
		<section>
			<h4>roles</h4>
			<p className="note">
				A role is a list of method patterns. Bound in a site it reaches that
				site; bound without, the whole tenant. You can only write into a role
				what you hold yourself.
			</p>

			<Bar>
				<input
					className="find"
					type="search"
					aria-label="filter roles"
					placeholder={`filter these ${all.length}`}
					value={find}
					onChange={(e) => setFind(e.target.value)}
				/>
				<Fill />
				<button
					className="add"
					disabled={!props.may('/roster.RoleService/Add')}
					onClick={() => openAdd(true)}
				>
					add role
				</button>
			</Bar>

			{items.length === 0 && (
				<p className="none">
					{all.length === 0 ? 'no roles — nobody here may call anything' : 'none of these match'}
				</p>
			)}
			{items.length > 0 && (
				<table>
					<thead>
						<tr>
							<th>role</th>
							<th>scoped to</th>
							<th>may call</th>
							<th />
						</tr>
					</thead>
					<tbody>
						{items.map((v) => {
							const k = uuid(v.id)

							return (
								<tr key={k} className={k === at ? 'at' : ''}>
									{/* What it is called and what it is for: the alias is written
								    into a binding, the note is read. */}
									<td>
										{v.alias}
										<span className="under">{v.desc}</span>
									</td>
									<td>
										<SiteName id={v.site?.id} />
									</td>
									<td className="mono">{v.methods.join(' ')}</td>
									<td className="acts">
										<button onClick={() => go(k === at ? null : k)}>
											{k === at ? 'hide' : 'who has it'}
										</button>
										<Menu label={`more for ${v.alias}`}>
											<button
												className="danger"
												disabled={!props.may('/roster.RoleService/Erase')}
												onClick={() => {
													setBad(null)
													void erase
														.call(ref(v.id))
														.then(() => setGone((was) => [...was, k]))
														.catch((e: unknown) => setBad(said(e)))
												}}
											>
												remove role
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

			<Sheet at={adding} onClose={() => openAdd(false)} title="add a role" bad={bad}>
			<form
				className="role"
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const f = new FormData(form)
					const alias = String(f.get('alias') ?? '').trim()
					const methods = String(f.get('methods') ?? '')
						.split(/[\s,]+/)
						.map((s) => s.trim())
						.filter((s) => s !== '')
					if (alias === '' || methods.length === 0) return
					const site = bytesOf(String(f.get('site') ?? ''))

					setBad(null)
					void add
						.call({
							tenant: ref(props.tenant),
							alias,
							desc: String(f.get('desc') ?? '').trim(),
							methods,
							...(site === undefined ? {} : { site: ref(site) }),
						})
						.then(() => {
							form.reset()
							openAdd(false)
						})
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<input name="alias" placeholder="role alias" required />
				<select name="site" defaultValue="">
					<option value="">whole tenant</option>
					{ss.map((s) => (
						<option key={uuid(s.id)} value={uuid(s.id)}>
							in {s.alias}
						</option>
					))}
				</select>
				<input
					name="methods"
					placeholder="/roster.HolderService/List /roster.SiteService/*"
					className="wide"
					required
				/>
				<input name="desc" placeholder="what it is for (optional)" />
				<button type="submit" disabled={add.state === 'pending' || !props.may('/roster.RoleService/Add')}>
					add role
				</button>
			</form>
			</Sheet>

			{at !== null && (
				<Bound tenant={props.tenant} role={items.find((v) => uuid(v.id) === at)} may={props.may} />
			)}
		</section>
	)
}

function Bound(props: {
	tenant: Uint8Array
	role: { id?: Uint8Array; alias?: string } | undefined
	may: May
}): React.ReactNode {
	const role = props.role?.id ?? new Uint8Array()
	const vs = useQuery(BindingService.method.list, {
		filters: [{ role: ref(role) }],
	})
	const groups = useQuery(GroupService.method.list, { filters: [{ tenant: ref(props.tenant) }] })
	const sites = useQuery(SiteService.method.list, { filters: [{ tenant: ref(props.tenant) }] })
	const add = useCall(BindingService.method.add)
	const erase = useCall(BindingService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [to, setTo] = useState<'holder' | 'group'>('holder')
	const [bad, setBad] = useState<string | null>(null)
	const [binding, openBind] = useState(false)

	if (props.role?.id === undefined) return null
	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))
	const gs = groups.data?.items ?? []
	const ss = sites.data?.items ?? []

	return (
		<section className="within">
			<h5>{props.role?.alias} — who has it</h5>
			<Bar>
				<Fill />
				<button
					className="add"
					disabled={!props.may('/roster.BindingService/Add')}
					onClick={() => openBind(true)}
				>
					give it to somebody
				</button>
			</Bar>
			{items.length === 0 && <p className="none">nobody — the role exists and hands out nothing yet</p>}
			{items.length > 0 && (
				<table>
					<tbody>
						{items.map((v) => (
							<tr key={uuid(v.id)}>
								<td>
									{v.group !== undefined ? <GroupName id={v.group.id} /> : <Alias id={v.holder?.id} />}
								</td>
								<td>
									<SiteName id={v.site?.id} />
								</td>
								<td className="acts">
									<Menu label="more for this binding">
										<button
											className="danger"
											disabled={!props.may('/roster.BindingService/Erase')}
											onClick={() => {
												setBad(null)
												void erase
													.call(ref(v.id))
													.then(() => setGone((was) => [...was, uuid(v.id)]))
													.catch((e: unknown) => setBad(said(e)))
											}}
										>
											take the role away
										</button>
									</Menu>
								</td>
							</tr>
						))}
					</tbody>
				</table>
			)}

			{bad !== null && <p className="bad">{bad}</p>}

			<Sheet at={binding} onClose={() => openBind(false)} title={`give ${props.role?.alias ?? 'this role'} to somebody`} bad={bad}>
			<form
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const f = new FormData(form)
					const who = bytesOf(String(f.get('who') ?? ''))
					if (who === undefined) return
					const site = bytesOf(String(f.get('site') ?? ''))

					setBad(null)
					void add
						.call({
							role: ref(role),
							...(to === 'holder' ? { holder: ref(who) } : { group: ref(who) }),
							...(site === undefined ? {} : { site: ref(site) }),
						})
						.then(() => {
							form.reset()
							openBind(false)
						})
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<select value={to} onChange={(e) => setTo(e.currentTarget.value === 'group' ? 'group' : 'holder')}>
					<option value="holder">a person</option>
					<option value="group">a group</option>
				</select>
				{to === 'holder' ? (
					<PickHolder tenant={props.tenant} name="who" />
				) : (
					<select name="who" defaultValue="" required>
						<option value="" disabled>
							which group
						</option>
						{gs.map((g) => (
							<option key={uuid(g.id)} value={uuid(g.id)}>
								{g.alias}
							</option>
						))}
					</select>
				)}
				{/* A site-scoped role can only be bound in its site; the server
				    refuses otherwise (`bindableIn`). The choice is drawn anyway,
				    because the refusal names the rule and a hidden field hides it. */}
				<select name="site" defaultValue="">
					<option value="">whole tenant</option>
					{ss.map((s) => (
						<option key={uuid(s.id)} value={uuid(s.id)}>
							in {s.alias}
						</option>
					))}
				</select>
				<button
					type="submit"
					disabled={add.state === 'pending' || !props.may('/roster.BindingService/Add')}
				>
					bind
				</button>
			</form>
			</Sheet>
		</section>
	)
}
