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

import { useQuery } from '@lesomnus/payday/react'
import type { App } from '@lesomnus/payday/react'

import { MeService } from '../gen/app/me_pb.js'
import { HolderService } from '../gen/roster/payday/holder_svc_pb.js'
import { ApiKeyService } from '../gen/app/apikey_svc_pb.js'

import { Customers } from './customers.js'

/**
 * covers is `frame.Covers` in the browser: three parts, each `*` or a name.
 *
 * The same three comparisons the server makes, because `MeService` answers with
 * **patterns** rather than an expansion — an expansion would be the methods
 * that exist in whichever replica answered, and during a rolling deploy two of
 * them would tell this page two different things about one person.
 */
export function covers(held: string, want: string): boolean {
	if (held === want) return true

	const h = parts(held)
	const w = parts(want)
	if (h === null || w === null) return false

	return h.every((v, i) => v === '*' || v === w[i])
}

function parts(v: string): [string, string, string] | null {
	if (!v.startsWith('/')) return null

	const i = v.indexOf('/', 1)
	if (i < 0) return null

	const full = v.slice(1, i)
	const method = v.slice(i + 1)
	if (full === '' || method === '' || method.includes('/')) return null

	// The **last** dot, because a package has dots in it: split at the first,
	// `/google.protobuf.Any/Pack` is package "google".
	const j = full.lastIndexOf('.')
	if (j < 0) return null

	const pkg = full.slice(0, j)
	const service = full.slice(j + 1)
	if (pkg === '' || service === '') return null

	return [pkg, service, method]
}

type Screen = 'operators' | 'services' | 'customers' | 'you'

export function Page(props: {
	onSignOut: () => void

	// The customers screen's store, on the admin listener. Null where there is
	// no such listener -- the sandbox -- and the screen is not offered.
	customers: App | null
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
				{at === 'operators' && <Operators />}
				{at === 'services' && <Services />}
				{at === 'customers' && <Customers app={props.customers} />}
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
function Operators(): React.ReactNode {
	const vs = useQuery(HolderService.method.list, {})

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	return (
		<section>
			<h2>signs in</h2>
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
function Services(): React.ReactNode {
	const vs = useQuery(ApiKeyService.method.list, {})

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	return (
		<section>
			<h2>calls in</h2>
			<table>
				<thead>
					<tr>
						<th>key</th>
						<th>may call</th>
						<th>last used</th>
					</tr>
				</thead>
				<tbody>
					{(vs.data?.items ?? []).map((v) => (
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
						</tr>
					))}
				</tbody>
			</table>
			<p className="note">
				An empty list allows <strong>nothing</strong>. A key somebody forgot to
				fill in opens no door.
			</p>
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
