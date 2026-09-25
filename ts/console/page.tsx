/**
 * The admin console: what an operator runs a deployment with.
 *
 * It is the **control plane**, which is a different set of rows from every
 * other port this app serves. Here a `Holder` is not a person a product app
 * signs in — those are customers' people, in the other database — it is
 * somebody who runs this deployment, or something that calls it.
 *
 * And a `Holder` is **not** a person or a machine. Nothing in the schema says
 * which, and nothing should: it is somebody or something registered here that
 * may exercise a permission, and how it proves itself is a separate row beside
 * it. A person with a `rt_` key holds both, which is exactly what that key is
 * for.
 *
 * So the two screens below split by **how a caller arrives**, not by what it
 * is, and say so on the page. That they line up with people and machines today
 * is how this deployment happens to be used, not a rule.
 *
 * @module
 */

import { useState } from 'react'
import type { Transport } from '@connectrpc/connect'

import { useCall, useQuery } from '@lesomnus/payday/react'
import { covers } from '../lib/covers.js'
import { go, useRoute } from '../lib/route.js'
import type { App } from '@lesomnus/payday/react'

import { MeService } from '../gen/app/me_pb.js'
import { HolderService } from '../gen/roster/payday/holder_svc_pb.js'
import { ApiKeyService } from '../gen/app/apikey_svc_pb.js'
import { CredentialService } from '../gen/app/credential_svc_pb.js'

import type { Writes } from '../lib/client.js'
import { Customers } from './customers.js'


type Screen = 'customers' | 'you'
const screenNames: readonly Screen[] = ['customers', 'you']

function screenOf(v: string | undefined): Screen {
	return (screenNames as readonly string[]).includes(v ?? '') ? (v as Screen) : 'customers'
}

export function Page(props: {
	onSignOut: () => void

	// The customers screen's store, on the admin listener. Null where there is
	// no such listener -- the sandbox -- and the screen is not offered.
	customers: App | null

	// And the clients for the writes that screen makes, which do not go through
	// the store: a reset answers with a secret rather than with a row.
	writes: Writes | null

	// The data plane with no wall, for that screen's devtools panel; only the
	// sandbox has one to hand (`main.tsx`, `ungatedTransports`).
	ungated?: Transport | undefined
}): React.ReactNode {
	const me = useQuery(MeService.method.get, {})

	// Which screen is the address bar's to say (`lib/route.ts`): the first
	// segment under the base, and the first screen when there is none or it
	// names nothing.
	const route = useRoute()
	const at = screenOf(route[0])

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
		// The data plane with no wall, which is this listener: the page, the
		// sign-in and these rows are one host (#27, #32).
		{ at: 'customers', name: 'customers', ok: may('/roster.TenantService/List') },
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
						onClick={() => go([s.at])}
					>
						{s.name}
					</button>
				))}
				<span className="who">{me.data?.alias}</span>
				<button onClick={props.onSignOut}>sign out</button>
			</nav>

			<main>
				{at === 'customers' && (
					<Customers app={props.customers} writes={props.writes} may={may} ungated={props.ungated} />
				)}
				{at === 'you' && <You methods={held} />}
			</main>
		</div>
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
