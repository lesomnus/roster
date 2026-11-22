import { Button, Section, Table as NoxTable, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@lesomnus/nox'
import { Choice, Form, Input } from '#lib/presentation.js'
/**
 * The mail domains a tenant claims: which addresses belong to whose people.
 *
 * `MailDomain` is what `FrontService.WhereFrom` answers from -- home-realm
 * discovery, so a form that collects an address can send somebody to the right
 * directory before anybody is resolved. It hangs off the **domain** and never off
 * a person, for the reason that Rpc gives: answered per person it would be an
 * account-enumeration oracle.
 *
 * Unlike a `Host` it needs no proof, and `host.proto` says why: its key is
 * `(tenant, name)`, so it claims nothing another tenant could be refused.
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

export function MailDomains(props: {
	tenant: { id?: Uint8Array; alias?: string } | undefined
	may: (m: string) => boolean
}): React.ReactNode {
	const id = props.tenant?.id
	if (id === undefined) return null

	return (
		<Section className="within">
			<h3>{props.tenant?.alias}</h3>
			<MailDomainList tenant={id} may={props.may} />
		</Section>
	)
}

function MailDomainList(props: { tenant: Uint8Array; may: (m: string) => boolean }): React.ReactNode {
	const vs = useQuery(MailDomainService.method.list, by(props.tenant))
	const providers = useQuery(ConnectionService.method.list, by(props.tenant))
	const add = useCall(MailDomainService.method.add)
	const update = useCall(MailDomainService.method.update)
	const erase = useCall(MailDomainService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [editing, setEditing] = useState<string | null>(null)
	const [bad, setBad] = useState<string | null>(null)
	const [adding, openAdd] = useState(false)

	// Over what was read; `MailDomainService` has no `Search`.
	const [find, setFind] = useState('')

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const all = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))
	const q = find.trim().toLowerCase()
	const items = q === '' ? all : all.filter((v) => `${v.name} ${v.provider}`.toLowerCase().includes(q))
	const names = (providers.data?.items ?? []).map((v) => v.name)

	return (
		<Section>
			<h4>mail domains</h4>
			<p className="note">
				<em>Addresses at @contoso.com go to Entra.</em> A front door that asks
				for an address first sends the person to the right provider without
				asking them which — and answers the same for every address at the
				domain, so nobody learns who is here by typing names.
			</p>

			<Bar>
				<Input
					className="find"
					type="search"
					aria-label="filter domains"
					placeholder={`filter these ${all.length}`}
					value={find}
					onChange={(e) => setFind(e.target.value)}
				/>
				<Fill />
				<Button
					variant="solid" className="add"
					disabled={!props.may('/roster.MailDomainService/Add')}
					onClick={() => openAdd(true)}
				>
					route a domain
				</Button>
			</Bar>

			{items.length === 0 && (
				<p className="none">{all.length === 0 ? 'no domains routed yet' : 'none of these match'}</p>
			)}
			{items.length > 0 && (
				<NoxTable>
					<TableHeader>
						<TableRow>
							<TableHead>domain, and where it goes</TableHead>
							<TableHead>note</TableHead>
							<TableHead />
						</TableRow>
					</TableHeader>
					<TableBody>
						{items.map((v) =>
							editing === uuid(v.id) ? (
								<TableRow key={uuid(v.id)} className="editing">
									<TableCell className="mono">@{v.name}</TableCell>
									<TableCell colSpan={2}>
										<Form
											className="edit"
											onSubmit={(e) => {
												e.preventDefault()
												const f = new FormData(e.currentTarget)
												setBad(null)
												void update
													.call({
														ref: { key: { case: 'id', value: v.id } },
														dateUpdated: v.dateUpdated,
														provider: String(f.get('provider') ?? ''),
														desc: String(f.get('desc') ?? '').trim(),
													})
													.then(() => setEditing(null))
													.catch((e: unknown) => setBad(said(e)))
											}}
										>
											<Choice name="provider" defaultValue={v.provider} autoFocus>
												<option value="">nowhere (known, not routed)</option>
												{names.map((n) => (
													<option key={n} value={n}>
														{n}
													</option>
												))}
											</Choice>
											<Input name="desc" placeholder="note" defaultValue={v.desc} />
											<Button type="submit" disabled={update.state === 'pending'}>
												save
											</Button>
											<Button type="button" onClick={() => setEditing(null)}>
												cancel
											</Button>
										</Form>
									</TableCell>
								</TableRow>
							) : (
							<TableRow key={uuid(v.id)}>
								{/* The domain and where it routes are one fact read together: an
								    address at this domain goes to that provider. */}
								<TableCell className="mono">
									@{v.name}
									<span className="under">
										{v.provider === '' ? 'nowhere — known, not routed' : `→ ${v.provider}`}
									</span>
								</TableCell>
								<TableCell>{v.desc}</TableCell>
								<TableCell className="acts">
									<Button disabled={!props.may('/roster.MailDomainService/Update')} onClick={() => setEditing(uuid(v.id))}>
										edit
									</Button>
									<Menu
										label={`more for @${v.name}`}
										items={[
											{
												label: 'stop routing it',
												danger: true,
												disabled: !props.may('/roster.MailDomainService/Erase'),
												run: () => {
													setBad(null)
													void erase
														.call({ key: { case: 'id', value: v.id } })
														.then(() => setGone((was) => [...was, uuid(v.id)]))
														.catch((e: unknown) => setBad(said(e)))
												},
											},
										]}
									/>
								</TableCell>
							</TableRow>
							),
						)}
					</TableBody>
				</NoxTable>
			)}

			{bad !== null && <p className="bad">{bad}</p>}

			<Sheet at={adding} onClose={() => openAdd(false)} title="route a mail domain" bad={bad}>
			<Form
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const f = new FormData(form)
					const name = String(f.get('name') ?? '')
						.trim()
						.replace(/^@/, '')
					if (name === '') return

					setBad(null)
					void add
						.call({
							tenant: at(props.tenant),
							name,
							provider: String(f.get('provider') ?? ''),
							desc: String(f.get('desc') ?? '').trim(),
						})
						.then(() => {
							form.reset()
							openAdd(false)
						})
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<Input name="name" placeholder="contoso.com" required />
				{/* A choice and not a text field: the value has to be a
				    `Connection.name` of this tenant, or the route goes nowhere. */}
				<Choice name="provider" defaultValue="">
					<option value="">nowhere (known, not routed)</option>
					{names.map((n) => (
						<option key={n} value={n}>
							{n}
						</option>
					))}
				</Choice>
				<Input name="desc" placeholder="note (optional)" />
				<Button
					type="submit"
					disabled={add.state === 'pending' || !props.may('/roster.MailDomainService/Add')}
				>
					route domain
				</Button>
			</Form>
			</Sheet>
		</Section>
	)
}
