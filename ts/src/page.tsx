/**
 * The console: what an operator runs a deployment with.
 *
 * It is the **control plane**, which is a different set of rows from every
 * other port this app serves. Here a `Holder` is not a person a product app
 * signs in — those are customers' people, in the other database — it is
 * somebody who runs this deployment, or a service that calls it.
 *
 * So the vocabulary on screen is deliberately not the schema's:
 *
 *	Tenant  the owner. There is one, and it is not shown
 *	Holder  an operator, if a Credential hangs off them
 *	Holder  a service, if ApiKeys do
 *	ApiKey  what one service may call, and when it last did
 *
 * @module
 */

import { useState } from 'react'

import { useQuery } from '@lesomnus/payday/react'

import { MeService } from '../gen/app/me_pb.js'
import { HolderService } from '../gen/roster/payday/holder_svc_pb.js'
import { ApiKeyService } from '../gen/app/apikey_svc_pb.js'

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

type Screen = 'operators' | 'services' | 'you'

export function Page(props: { onSignOut: () => void }): React.ReactNode {
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
		{ at: 'operators', name: 'operators', ok: may('/roster.HolderService/List') },
		{ at: 'services', name: 'services', ok: may('/roster.ApiKeyService/List') },
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
				{at === 'you' && <You methods={held} />}
			</main>
		</div>
	)
}

/**
 * Operators: everybody who can sign in to this console.
 *
 * A `Holder` of the owner tenant. Whether they are a person or a service is not
 * a column — it is what hangs off them, a `Credential` or an `ApiKey` — so this
 * lists both and `Services` is the same rows read from the other end.
 */
function Operators(): React.ReactNode {
	const vs = useQuery(HolderService.method.list, {})

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	return (
		<section>
			<h2>operators</h2>
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
				A password is set by <code>roster init</code> and cannot be read back
				— this deployment cannot tell anybody what theirs was any more than it
				can tell them their key.
			</p>
		</section>
	)
}

/**
 * Services: what may call this deployment, and what each may call.
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
			<h2>services</h2>
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
