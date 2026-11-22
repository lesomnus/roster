import { Button, Section, Table as NoxTable, TableBody, TableCell, TableRow } from '@lesomnus/nox'
import { Form, Input } from '#lib/presentation.js'
/**
 * The names a tenant answers at, and the claims that prove them.
 *
 * Two entities and one screen, because one is the other's evidence: a `HostProof`
 * is a name being claimed and the token roster asked for at
 * `_roster-challenge.<name>`, and spending it is what writes the `Host`. Drawing
 * them apart would be two screens where the second is only ever read while the
 * first is being filled in (#42).
 *
 * # Why a tenant may write these at all
 *
 * `Host.name` is unique across the deployment, so the first writer used to take a
 * name and the rightful holder was refused -- which made registering one the
 * deployment's act rather than a tenant's. The proof is what changed that, and it
 * is why the claim is on this screen next to the row it becomes.
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

export function Hosts(props: {
	tenant: { id?: Uint8Array; alias?: string } | undefined
	may: (m: string) => boolean
}): React.ReactNode {
	const id = props.tenant?.id
	if (id === undefined) return null

	return (
		<Section className="within">
			<h3>{props.tenant?.alias}</h3>
			<HostList tenant={id} may={props.may} />
			<Claims tenant={id} may={props.may} />
		</Section>
	)
}

function HostList(props: { tenant: Uint8Array; may: (m: string) => boolean }): React.ReactNode {
	const vs = useQuery(HostService.method.list, by(props.tenant))
	const add = useCall(HostService.method.add)
	const update = useCall(HostService.method.update)
	const erase = useCall(HostService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [editing, setEditing] = useState<string | null>(null)
	const [bad, setBad] = useState<string | null>(null)
	const [adding, openAdd] = useState(false)

	// Over what was read; `HostService` has no `Search`.
	const [find, setFind] = useState('')

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const all = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))
	const q = find.trim().toLowerCase()
	const items = q === '' ? all : all.filter((v) => `${v.name} ${v.desc}`.toLowerCase().includes(q))

	return (
		<Section>
			<h4>hosts</h4>
			<p className="note">
				The hostnames a browser arrives at that mean this customer. A front door
				asks roster which tenant a name is rather than holding a map, so a name
				missing here is a sign-in page that says nobody is there.
			</p>
			<p className="note">
				A name is unique across the deployment, so there are two roads to one.
				Claiming it below and publishing what roster asks for is a tenant's own;
				writing it straight in is a roster operator's, because the person who
				routed the name is the person writing it. <em>proved</em> is which.
			</p>

			<Bar>
				<Input
					className="find"
					type="search"
					aria-label="filter hosts"
					placeholder={`filter these ${all.length}`}
					value={find}
					onChange={(e) => setFind(e.target.value)}
				/>
				<Fill />
				<Button
					variant="solid" className="add"
					disabled={!props.may('/roster.HostService/Add')}
					onClick={() => openAdd(true)}
				>
					add name
				</Button>
			</Bar>

			{items.length === 0 && (
				<p className="none">{all.length === 0 ? 'no names yet' : 'none of these match'}</p>
			)}
			{items.length > 0 && (
				<NoxTable>
					<TableBody>
						{items.map((v) =>
							editing === uuid(v.id) ? (
								<TableRow key={uuid(v.id)} className="editing">
									<TableCell className="mono">{v.name}</TableCell>
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
														desc: String(f.get('desc') ?? '').trim(),
													})
													.then(() => setEditing(null))
													.catch((e: unknown) => setBad(said(e)))
											}}
										>
											<Input name="desc" placeholder="note" defaultValue={v.desc} autoFocus />
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
								<TableCell className="mono">
									{v.name}
									<span className="under">{v.desc}</span>
								</TableCell>
								<TableCell>
									{/* Which road wrote it. Not a gate -- a front door resolves a
									    name either way -- so this says nothing about the row being
									    usable and everything about what it is evidence of. */}
									{v.dateProved === undefined ? (
										<span className="dim">written</span>
									) : (
										<span>proved</span>
									)}
								</TableCell>
								<TableCell className="acts">
									<Button disabled={!props.may('/roster.HostService/Update')} onClick={() => setEditing(uuid(v.id))}>
										edit
									</Button>
									<Menu
										label={`more for ${v.name}`}
										items={[
											{
												label: 'remove name',
												danger: true,
												disabled: !props.may('/roster.HostService/Erase'),
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

			<Sheet at={adding} onClose={() => openAdd(false)} title="add a name" bad={bad}>
			<Form
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const f = new FormData(form)
					const name = String(f.get('name') ?? '').trim()
					if (name === '') return

					setBad(null)
					// Not normalised here: the layer refuses a name that is not
					// already the way it will be compared, and says what it
					// should have been -- which is worth more to the operator
					// than a silent fix they cannot see (`server/core/host.go`).
					void add
						.call({ tenant: at(props.tenant), name, desc: String(f.get('desc') ?? '').trim() })
						.then(() => {
							form.reset()
							openAdd(false)
						})
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<Input name="name" placeholder="contoso.example.com" required />
				<Input name="desc" placeholder="note (optional)" />
				<Button type="submit" disabled={add.state === 'pending' || !props.may('/roster.HostService/Add')}>
					add name
				</Button>
			</Form>
			</Sheet>
		</Section>
	)
}

/**
 * Claims is the names being proved, and what to publish to prove them.
 *
 * The second half of the exchange is `Host.Add` and not a verb of its own, which
 * is the thing worth knowing before reading this: roster does the lookup when the
 * row is written, so *take the name* below is the same call the form above makes.
 * A tenant who has not published yet is refused, saying which record is missing
 * and what it should say -- so the button needs no optimism and this screen needs
 * no state machine.
 */
function Claims(props: { tenant: Uint8Array; may: (m: string) => boolean }): React.ReactNode {
	const vs = useQuery(HostProofService.method.list, by(props.tenant))
	const add = useCall(HostProofService.method.add)
	const erase = useCall(HostProofService.method.erase)
	const take = useCall(HostService.method.add)
	const [gone, setGone] = useState<string[]>([])
	const [bad, setBad] = useState<string | null>(null)
	const [done, say] = useState<string | null>(null)
	const [claiming, openClaim] = useState(false)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	// The record to publish, written out the way a DNS provider's form asks for
	// it: a name, a type, and a value.
	const record = (name: string): string => `_roster-challenge.${name}`

	const drop = (id: Uint8Array): void => {
		setBad(null)
		void erase
			.call({ key: { case: 'id', value: id } })
			.then(() => setGone((was) => [...was, uuid(id)]))
			.catch((e: unknown) => setBad(said(e)))
	}

	return (
		<Section>
			<h4>host proofs</h4>
			<p className="note">
				Claim a name here, publish the value below as a <code>TXT</code> record,
				then take it. roster asks the servers that hold the zone rather than a
				cache, so there is nothing to wait for once the record is saved — and
				nothing is held by claiming: two tenants may be claiming one name at
				once, and whoever's value is published has it.
			</p>

			<Bar>
				<Fill />
				<Button
					variant="solid" className="add"
					disabled={!props.may('/roster.HostProofService/Add')}
					onClick={() => openClaim(true)}
				>
					claim a name
				</Button>
			</Bar>

			{items.length === 0 && <p className="none">nothing being proved</p>}
			{items.map((v) => (
				<div className="claim" key={uuid(v.id)}>
					<p className="mono">{v.name}</p>
					<NoxTable className="facts">
						<TableBody>
							<TableRow>
								<TableCell>name</TableCell>
								<TableCell className="mono">{record(v.name)}</TableCell>
							</TableRow>
							<TableRow>
								<TableCell>type</TableCell>
								<TableCell className="mono">TXT</TableCell>
							</TableRow>
							<TableRow>
								<TableCell>value</TableCell>
								<TableCell className="mono">{v.token}</TableCell>
							</TableRow>
							<TableRow>
								<TableCell>good until</TableCell>
								<TableCell>{when(v.dateExpires)}</TableCell>
							</TableRow>
						</TableBody>
					</NoxTable>
					<Button
						disabled={take.state === 'pending' || !props.may('/roster.HostService/Add')}
						onClick={() => {
							setBad(null)
							say(null)
							// The same call the form above makes. The lookup is
							// roster's and happens here.
							void take
								.call({ tenant: at(props.tenant), name: v.name })
								.then(() => {
									setGone((was) => [...was, uuid(v.id)])
									say(`${v.name} is yours`)
								})
								.catch((e: unknown) => setBad(said(e)))
						}}
					>
						take the name
					</Button>
					<Menu
						label={`more for ${v.name}`}
						items={[
							{
								label: 'give up this claim',
								danger: true,
								disabled: !props.may('/roster.HostProofService/Erase'),
								run: () => drop(v.id),
							},
						]}
					/>
				</div>
			))}

			<Sheet at={claiming} onClose={() => openClaim(false)} title="claim a name" bad={bad}>
			<Form
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const f = new FormData(form)
					const name = String(f.get('name') ?? '').trim()
					if (name === '') return

					setBad(null)
					say(null)
					// The token and the window are not sent: roster generates
					// both and refuses a request that carries either, for the
					// reason `server/core/hostproof.go` gives.
					void add
						.call({ tenant: at(props.tenant), name })
						.then(() => {
							form.reset()
							openClaim(false)
						})
						.catch((e: unknown) => setBad(said(e)))
				}}
			>
				<Input name="name" placeholder="contoso.example.com" required />
				<Button type="submit" disabled={add.state === 'pending' || !props.may('/roster.HostProofService/Add')}>
					claim a name
				</Button>
			</Form>
			</Sheet>
			{done !== null && <p className="note">{done}</p>}
			{bad !== null && <p className="bad">{bad}</p>}
		</Section>
	)
}
