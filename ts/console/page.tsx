/**
 * The console: what an operator runs a deployment with.
 *
 * It is the **control plane**, which is a different set of rows from every
 * other port this app serves. Here a `Holder` is not a person a product app
 * signs in — those are customers' people, in the other database — it is
 * somebody who runs this deployment, or a service that calls it.
 *
 * And a `Holder` is **not** a person or a machine. Nothing in the schema says
 * which, and nothing should: it is somebody or something registered here that
 * may exercise a permission, and how it proves itself is a separate row beside
 * it. A person with a `rt_` key holds both, which is exactly what that key is
 * for.
 *
 * So the two screens below split by **how a caller arrives**, not by what it
 * is, and say so on the page. That they line up with people and services today
 * is how this deployment happens to be used, not a rule.
 *
 * @module
 */

import { useState } from 'react'

import { useCall, useQuery } from '@lesomnus/payday/react'
import { covers } from '../lib/covers.js'
import type { App } from '@lesomnus/payday/react'

import { MeService } from '../gen/app/me_pb.js'
import { IssueService } from '../gen/app/issue_pb.js'
import { HolderService } from '../gen/roster/payday/holder_svc_pb.js'
import { ApiKeyService } from '../gen/app/apikey_svc_pb.js'

import type { Admin } from '../lib/client.js'
import { Customers } from './customers.js'


type Screen = 'operators' | 'services' | 'customers' | 'you'

export function Page(props: {
	onSignOut: () => void

	// The customers screen's store, on the admin listener. Null where there is
	// no such listener -- the sandbox -- and the screen is not offered.
	customers: App | null

	// And the clients for the writes that screen makes, which do not go through
	// the store: a reset answers with a secret rather than with a row.
	admin: Admin | null
}): React.ReactNode {
	const me = useQuery(MeService.method.get, {})
	const [at, go] = useState<Screen>('operators')

	if (me.state === 'pending') return <main className="loading">…</main>
	if (me.state === 'error') {
		return (
			<main className="error">
				<p>{me.error instanceof Error ? me.error.message : 'no'}</p>
				<button onClick={props.onSignOut}>sign out</button>
			</main>
		)
	}

	const held = me.data?.methods ?? []
	const may = (method: string): boolean => held.some((v) => covers(v, method))

	// What is worth drawing, and never what is allowed. The server refuses
	// either way, and a client that treated this as the decision would be one an
	// altered client could talk out of.
	const screens: { at: Screen; name: string; ok: boolean }[] = [
		{ at: 'operators', name: 'signs in', ok: may('/roster.HolderService/List') },
		{ at: 'services', name: 'calls in', ok: may('/roster.ApiKeyService/List') },

		// The data plane, through the admin listener. Two conditions rather than
		// one: the method an operator may call, and whether this deployment has
		// that listener at all.
		{
			at: 'customers',
			name: 'customers',
			ok: props.customers !== null && may('/roster.TenantService/List'),
		},
		{ at: 'you', name: 'you', ok: true },
	]

	return (
		<div className="console">
			<nav>
				<h1>roster</h1>
				{screens.map((s) => (
					<button
						key={s.at}
						disabled={!s.ok}
						className={s.at === at ? 'at' : ''}
						onClick={() => go(s.at)}
					>
						{s.name}
					</button>
				))}
				<span className="who">{me.data?.alias}</span>
				<button onClick={props.onSignOut}>sign out</button>
			</nav>

			<main>
				{at === 'operators' && <Operators may={may} />}
				{at === 'services' && <Services may={may} />}
				{at === 'customers' && (
					<Customers app={props.customers} admin={props.admin} may={may} />
				)}
				{at === 'you' && <You methods={held} />}
			</main>
		</div>
	)
}

/**
 * Everybody registered here, which is what a `Holder` is.
 *
 * Titled by how they arrive rather than by what they are, because the schema
 * does not say what they are and neither should this. `Services` below is the
 * same rows read from the other end: a holder with keys.
 */
function Operators(props: { may: (method: string) => boolean }): React.ReactNode {
	const vs = useQuery(HolderService.method.list, {})
	const issue = useCall(IssueService.method.issuePassword)
	const [said, say] = useState<{ kind: 'secret' | 'bad'; text: string } | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	return (
		<section>
			<h2>signs in</h2>

			{/* A new operator is one call: `IssueService.IssuePassword` makes
			    the person in the control plane's one tenant if they are not
			    there, and answers with a generated password, once. There is no
			    field to type one into, for the reason `roster init` has none. */}
			<form
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const alias = String(new FormData(form).get('alias') ?? '').trim()
					if (alias === '') return

					say(null)
					void issue
						.call({ alias })
						.then((r) => {
							form.reset()
							say({ kind: 'secret', text: `${alias}: ${r.password}` })
						})
						.catch((e: unknown) => say({ kind: 'bad', text: e instanceof Error ? e.message : 'no' }))
				}}
			>
				<input name="alias" placeholder="new operator, or one to reset" required />
				<button type="submit" disabled={issue.state === 'pending' || !props.may('/roster.IssueService/IssuePassword')}>
					issue a password
				</button>
			</form>
			{said?.kind === 'secret' && (
				<div className="secret">
					<p>
						Read this out. It is shown <strong>once</strong> — what is stored is a hash.
					</p>
					<code>{said.text}</code>
				</div>
			)}
			{said?.kind === 'bad' && <p className="bad">{said.text}</p>}

			<table>
				<thead>
					<tr>
						<th>alias</th>
						<th>name</th>
						<th>since</th>
					</tr>
				</thead>
				<tbody>
					{(vs.data?.items ?? []).map((v) => (
						<tr key={v.alias}>
							<td>
								<code>{v.alias}</code>
							</td>
							<td>{v.name}</td>
							<td>{when(v.dateCreated?.seconds)}</td>
						</tr>
					))}
				</tbody>
			</table>
			<p className="note">
				Everybody registered in this plane. A <code>Holder</code> is not a
				person or a machine — nothing here says which — so this is the same
				list <em>calls in</em> draws, seen from the other end.
			</p>
		</section>
	)
}

/**
 * The keys, which is how something calls in rather than signing in.
 *
 * `ApiKeyService` is served on this port and no other, because its generated
 * `Get` answers with the verifier column. The column is declared
 * `(payday.field).secret`, so it is cleared on the way out and never reaches a
 * page — and never reaches the trail either.
 */
function Services(props: { may: (method: string) => boolean }): React.ReactNode {
	const vs = useQuery(ApiKeyService.method.list, {})
	const issue = useCall(ApiKeyService.method.issue)
	const erase = useCall(ApiKeyService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [said, say] = useState<{ kind: 'secret' | 'bad'; text: string } | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(v.alias))

	return (
		<section>
			<h2>calls in</h2>
			<table>
				<thead>
					<tr>
						<th>key</th>
						<th>may call</th>
						<th>last used</th>
						<th />
					</tr>
				</thead>
				<tbody>
					{items.map((v) => (
						<tr key={v.alias}>
							<td>
								<code>{v.alias}</code>
							</td>
							<td>
								<ul className="methods">
									{v.methods.map((m) => (
										<li key={m}>
											<code>{m}</code>
										</li>
									))}
								</ul>
							</td>
							<td>{v.dateUsed === undefined ? 'never' : when(v.dateUsed.seconds)}</td>
							<td>
								{/* Revoking is a delete, and it is immediate: the
								    next call with this key finds no row. There is
								    no edit -- a key's methods are what it was
								    minted with, and a different list is a
								    different key. */}
								<button
									disabled={!props.may('/roster.ApiKeyService/Erase')}
									onClick={() => {
										say(null)
										void erase
											.call({ key: { case: 'id', value: v.id } })
											.then(() => setGone((was) => [...was, v.alias]))
											.catch((e: unknown) => say({ kind: 'bad', text: e instanceof Error ? e.message : 'no' }))
									}}
								>
									revoke
								</button>
							</td>
						</tr>
					))}
				</tbody>
			</table>
			<p className="note">
				An empty list allows <strong>nothing</strong>. A key somebody forgot to
				fill in opens no door.
			</p>

			{/* `roster key add --service custody --allow …`, from a page: the
			    service is made if it is not there, because a service is not
			    something set up on purpose before it is needed. The token is
			    shown once. */}
			<form
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const f = new FormData(form)
					const service = String(f.get('service') ?? '').trim()
					const alias = String(f.get('alias') ?? '').trim() || 'default'
					const methods = String(f.get('methods') ?? '')
						.split(/[\s,]+/)
						.map((s) => s.trim())
						.filter((s) => s !== '')
					if (service === '' || methods.length === 0) return

					say(null)
					void issue
						.call({ service, alias, methods })
						.then((r) => {
							form.reset()
							say({ kind: 'secret', text: r.token })
						})
						.catch((e: unknown) => say({ kind: 'bad', text: e instanceof Error ? e.message : 'no' }))
				}}
			>
				<input name="service" placeholder="service, e.g. custody" required />
				<input name="alias" placeholder="key name (default)" />
				<input name="methods" placeholder="/roster.VouchService/Verify, …" className="wide" required />
				<button type="submit" disabled={issue.state === 'pending' || !props.may('/roster.ApiKeyService/Issue')}>
					mint a service key
				</button>
			</form>
			{said?.kind === 'secret' && (
				<div className="secret">
					<p>
						The key, shown <strong>once</strong>. What is stored is a hash.
					</p>
					<code>{said.text}</code>
				</div>
			)}
			{said?.kind === 'bad' && <p className="bad">{said.text}</p>}
		</section>
	)
}

/** You: what this operator may call, which is the union the server enforces. */
function You(props: { methods: string[] }): React.ReactNode {
	return (
		<section>
			<h2>you</h2>
			<ul className="methods">
				{props.methods.map((m) => (
					<li key={m}>
						<code>{m}</code>
					</li>
				))}
			</ul>
			<p className="note">
				Patterns, not every RPC written out. <code>/roster.*/*</code> is
				everything roster serves, now and after an upgrade — which is why it is
				a pattern and not a list somebody has to keep in step.
			</p>
		</section>
	)
}

function Failed(props: { at: unknown }): React.ReactNode {
	return <p className="error">{props.at instanceof Error ? props.at.message : 'no'}</p>
}

function when(seconds: bigint | undefined): string {
	if (seconds === undefined) return ''

	return new Date(Number(seconds) * 1000).toISOString().slice(0, 10)
}
