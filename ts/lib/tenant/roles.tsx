import { Button, Section, Table as NoxTable, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@lesomnus/nox'
import { Choice, Form, Input } from '#lib/presentation.js'
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
 * # Every write here but one is a grant
 *
 * `Role.Add`, `Role.Widen`, `Binding.Add` -- `server/core/escalate.go` refuses
 * each when it hands out a method the operator does not hold, or binds a role
 * somewhere it may not go. `Role.Narrow` takes a method out of a role, which
 * hands nobody anything, and has rules of its own (`server/core/narrow.go`):
 * nobody takes away what they could not have given, and a tenant keeps
 * somebody administering it. This page does not pre-check any of that; it
 * says what the server said.
 *
 * # A role's methods are changed one way at a time
 *
 * Each method of a role is taken out on its own, beside it (`Narrow`), and
 * more are added from the role's menu (`Widen`) -- the two writes the wire
 * has for a role's methods, because the one that could do both (`Patch`) is
 * closed there. Whatever either answers with goes into the store, so the row
 * redraws as the server has it.
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
		<Section className="within">
			<h3>{props.tenant?.alias}</h3>
			<RoleList tenant={id} may={props.may} />
		</Section>
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
	const widen = useCall(RoleService.method.widen)
	const narrow = useCall(RoleService.method.narrow)
	const [gone, setGone] = useState<string[]>([])
	const [at, go] = useState<string | null>(null)
	const [bad, setBad] = useState<string | null>(null)
	const [adding, openAdd] = useState(false)
	const [widening, openWiden] = useState<string | null>(null)

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
		<Section>
			<h4>roles</h4>
			<p className="note">
				A role is a list of method patterns. Bound in a site it reaches that
				site; bound without, the whole tenant. You can only write into a role
				what you hold yourself.
			</p>

			<Bar>
				<Input
					className="find"
					type="search"
					aria-label="filter roles"
					placeholder={`filter these ${all.length}`}
					value={find}
					onChange={(e) => setFind(e.target.value)}
				/>
				<Fill />
				<Button
					variant="solid" className="add"
					disabled={!props.may('/roster.RoleService/Add')}
					onClick={() => openAdd(true)}
				>
					add role
				</Button>
			</Bar>

			{items.length === 0 && (
				<p className="none">
					{all.length === 0 ? 'no roles — nobody here may call anything' : 'none of these match'}
				</p>
			)}
			{items.length > 0 && (
				<NoxTable>
					<TableHeader>
						<TableRow>
							<TableHead>role</TableHead>
							<TableHead>scoped to</TableHead>
							<TableHead>may call</TableHead>
							<TableHead />
						</TableRow>
					</TableHeader>
					<TableBody>
						{items.map((v) => {
							const k = uuid(v.id)

							return (
								<TableRow key={k} className={k === at ? 'at' : ''}>
									{/* What it is called and what it is for: the alias is written
								    into a binding, the note is read. */}
									<TableCell>
										{v.alias}
										<span className="under">{v.desc}</span>
									</TableCell>
									<TableCell>
										<SiteName id={v.site?.id} />
									</TableCell>
									<TableCell className="mono methods">
										{v.methods.length === 0 && <span className="dim">nothing</span>}
										{v.methods.map((m) => (
											<span key={m} className="method">
												{m}
												<Button
													type="button"
													className="link"
													aria-label={`take ${m} out of ${v.alias}`}
													title={`take ${m} out of ${v.alias}`}
													disabled={narrow.state === 'pending' || !props.may('/roster.RoleService/Narrow')}
													onClick={() => {
														setBad(null)
														void narrow
															.call({ ref: ref(v.id), methods: [m] })
															.catch((e: unknown) => setBad(said(e)))
													}}
												>
													×
												</Button>
											</span>
										))}
									</TableCell>
									<TableCell className="acts">
										<Button onClick={() => go(k === at ? null : k)}>
											{k === at ? 'hide' : 'who has it'}
										</Button>
										<Menu
											label={`more for ${v.alias}`}
											items={[
												{
													label: 'add methods',
													disabled: !props.may('/roster.RoleService/Widen'),
													run: () => {
														setBad(null)
														openWiden(k)
													},
												},
												{
													label: 'remove role',
													danger: true,
													disabled: !props.may('/roster.RoleService/Erase'),
													run: () => {
														setBad(null)
														void erase
															.call(ref(v.id))
															.then(() => setGone((was) => [...was, k]))
															.catch((e: unknown) => setBad(said(e)))
													},
												},
											]}
										/>
									</TableCell>
								</TableRow>
							)
						})}
					</TableBody>
				</NoxTable>
			)}

			{bad !== null && <p className="bad">{bad}</p>}

			<Sheet at={adding} onClose={() => openAdd(false)} title="add a role" bad={bad}>
			<Form
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
				<Input name="alias" placeholder="role alias" required />
				<Choice name="site" defaultValue="">
					<option value="">whole tenant</option>
					{ss.map((s) => (
						<option key={uuid(s.id)} value={uuid(s.id)}>
							in {s.alias}
						</option>
					))}
				</Choice>
				<Input
					name="methods"
					placeholder="/roster.HolderService/List /roster.SiteService/*"
					className="wide"
					required
				/>
				<Input name="desc" placeholder="what it is for (optional)" />
				<Button type="submit" disabled={add.state === 'pending' || !props.may('/roster.RoleService/Add')}>
					add role
				</Button>
			</Form>
			</Sheet>

			<Sheet
				at={widening !== null}
				onClose={() => openWiden(null)}
				title={`add methods to ${all.find((v) => uuid(v.id) === widening)?.alias ?? 'the role'}`}
				bad={bad}
			>
				<Form
					className="widen"
					onSubmit={(e) => {
						e.preventDefault()
						const form = e.currentTarget
						const role = all.find((v) => uuid(v.id) === widening)
						const methods = String(new FormData(form).get('methods') ?? '')
							.split(/[\s,]+/)
							.map((s) => s.trim())
							.filter((s) => s !== '')
						if (role === undefined || methods.length === 0) return

						setBad(null)
						void widen
							.call({ ref: ref(role.id), methods })
							.then(() => {
								form.reset()
								openWiden(null)
							})
							.catch((e: unknown) => setBad(said(e)))
					}}
				>
					<Input
						name="methods"
						aria-label="methods to add"
						placeholder="/roster.HolderService/Get /loft.Permission/*"
						className="wide"
						required
					/>
					<Button type="submit" disabled={widen.state === 'pending' || !props.may('/roster.RoleService/Widen')}>
						add methods
					</Button>
				</Form>
			</Sheet>

			{at !== null && (
				<Bound tenant={props.tenant} role={items.find((v) => uuid(v.id) === at)} may={props.may} />
			)}
		</Section>
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
		<Section className="within">
			<h5>{props.role?.alias} — who has it</h5>
			<Bar>
				<Fill />
				<Button
					variant="solid" className="add"
					disabled={!props.may('/roster.BindingService/Add')}
					onClick={() => openBind(true)}
				>
					give it to somebody
				</Button>
			</Bar>
			{items.length === 0 && <p className="none">nobody — the role exists and hands out nothing yet</p>}
			{items.length > 0 && (
				<NoxTable>
					<TableBody>
						{items.map((v) => (
							<TableRow key={uuid(v.id)}>
								<TableCell>
									{v.group !== undefined ? <GroupName id={v.group.id} /> : <Alias id={v.holder?.id} />}
								</TableCell>
								<TableCell>
									<SiteName id={v.site?.id} />
								</TableCell>
								<TableCell className="acts">
									<Menu
										label="more for this binding"
										items={[
											{
												label: 'take the role away',
												danger: true,
												disabled: !props.may('/roster.BindingService/Erase'),
												run: () => {
													setBad(null)
													void erase
														.call(ref(v.id))
														.then(() => setGone((was) => [...was, uuid(v.id)]))
														.catch((e: unknown) => setBad(said(e)))
												},
											},
										]}
									/>
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</NoxTable>
			)}

			{bad !== null && <p className="bad">{bad}</p>}

			<Sheet at={binding} onClose={() => openBind(false)} title={`give ${props.role?.alias ?? 'this role'} to somebody`} bad={bad}>
			<Form
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
				<Choice value={to} onChange={(e) => setTo(e.currentTarget.value === 'group' ? 'group' : 'holder')}>
					<option value="holder">a person</option>
					<option value="group">a group</option>
				</Choice>
				{to === 'holder' ? (
					<PickHolder tenant={props.tenant} name="who" />
				) : (
					<Choice name="who" defaultValue="" required>
						<option value="" disabled>
							which group
						</option>
						{gs.map((g) => (
							<option key={uuid(g.id)} value={uuid(g.id)}>
								{g.alias}
							</option>
						))}
					</Choice>
				)}
				{/* A site-scoped role can only be bound in its site; the server
				    refuses otherwise (`bindableIn`). The choice is drawn anyway,
				    because the refusal names the rule and a hidden field hides it. */}
				<Choice name="site" defaultValue="">
					<option value="">whole tenant</option>
					{ss.map((s) => (
						<option key={uuid(s.id)} value={uuid(s.id)}>
							in {s.alias}
						</option>
					))}
				</Choice>
				<Button
					type="submit"
					disabled={add.state === 'pending' || !props.may('/roster.BindingService/Add')}
				>
					bind
				</Button>
			</Form>
			</Sheet>
		</Section>
	)
}
