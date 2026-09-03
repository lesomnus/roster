/**
 * The account app's page: sign in, and the account.
 *
 * # The same client as the console, one origin over
 *
 * Everything this page reads or writes about the person is a Connect call to
 * **this** origin -- `/roster.MeService/Get` and the rest -- which `roster
 * account serve` hands on to roster as the person (`frontdoor.Door.Proxy`). So
 * this is `ts/gen` and the same store the console uses, with the transport
 * pointed at `location.origin`; nothing here knows roster's address, and the
 * browser never holds a roster token.
 *
 * What is not a Connect call is the sign-in itself -- `POST /session`, the
 * second form at `/session/continue`, a provider's round trip through `/login`
 * -- because those are the app's own protocol (`frontdoor`), and `/providers`,
 * which the page needs before it has a session.
 *
 * # What it draws, in P4
 *
 * The sign-in page a tenant's people arrive at, with the providers the operator
 * configured and a password form; and, signed in, who they are and how they
 * sign in, with sign-out. The rest of the account -- profile, addresses,
 * factors, keys, sessions -- is P5 of `ts/plan.md`, section by section.
 */

import { createConnectTransport } from '@connectrpc/connect-web'
import { StrictMode, useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { Provider, useQuery } from '@lesomnus/payday/react'
import type { App } from '@lesomnus/payday/react'

import { MeService } from '../gen/app/me_pb.js'
import { open } from '../lib/store.js'
import '../lib/style.css'

const root = createRoot(document.getElementById('root') as HTMLElement)

/** Providers is what `/providers` says: who this front door is for, and how they arrive. */
interface Providers {
	tenant: { alias: string; name: string; labels: Record<string, string> }
	providers: { name: string; issuer: string }[]
	password: boolean
}

async function providers(): Promise<Providers | null> {
	const res = await fetch('/providers')
	if (!res.ok) return null

	return (await res.json()) as Providers
}

/**
 * SignIn is the first form and, when roster asks for it, the second.
 *
 * Three answers, three status codes (`frontdoor`): 204 signed in, 200 one
 * factor proved and another to prove, 401 everything else -- one answer for a
 * wrong password, an unknown person and a locked account, which the page must
 * not undo by guessing which.
 */
function SignIn(props: { of: Providers; onDone: () => void }): React.ReactNode {
	const [step, setStep] = useState<{ kinds: string[] } | null>(null)
	const [bad, setBad] = useState(false)
	const brand = props.of.tenant.name !== '' ? props.of.tenant.name : props.of.tenant.alias

	const first = (e: React.FormEvent<HTMLFormElement>): void => {
		e.preventDefault()
		const f = new FormData(e.currentTarget)
		setBad(false)
		void fetch('/session', {
			method: 'POST',
			headers: { 'content-type': 'application/json' },
			body: JSON.stringify({ alias: String(f.get('alias') ?? ''), password: String(f.get('password') ?? '') }),
		}).then(async (res) => {
			if (res.status === 204) return props.onDone()
			if (res.status === 200) {
				const v = (await res.json()) as { available?: { kind: string }[] }
				return setStep({ kinds: (v.available ?? []).map((a) => a.kind) })
			}
			setBad(true)
		})
	}

	const second = (e: React.FormEvent<HTMLFormElement>): void => {
		e.preventDefault()
		const f = new FormData(e.currentTarget)
		setBad(false)
		void fetch('/session/continue', {
			method: 'POST',
			headers: { 'content-type': 'application/json' },
			body: JSON.stringify({ kind: String(f.get('kind') ?? 'totp'), code: String(f.get('code') ?? '') }),
		}).then((res) => {
			if (res.status === 204) return props.onDone()
			setBad(true)
			if (res.status !== 401) setStep(null)
		})
	}

	return (
		<main className="sign-in">
			<h1>{brand}</h1>

			{props.of.providers.length > 0 && (
				<section className="providers">
					{props.of.providers.map((p) => (
						<a key={p.name} className="button" href={`/login?connection=${encodeURIComponent(p.name)}`}>
							sign in with {p.name}
						</a>
					))}
				</section>
			)}

			{props.of.password && step === null && (
				<form onSubmit={first}>
					<label>
						who
						<input name="alias" autoFocus autoComplete="username" />
					</label>
					<label>
						password
						<input name="password" type="password" autoComplete="current-password" />
					</label>
					<button type="submit">sign in</button>
				</form>
			)}

			{step !== null && (
				<form onSubmit={second}>
					<p className="note">one more: a code from your {step.kinds.join(' or ') || 'authenticator'}</p>
					<input type="hidden" name="kind" value={step.kinds[0] ?? 'totp'} />
					<label>
						code
						<input name="code" inputMode="numeric" autoComplete="one-time-code" autoFocus />
					</label>
					<button type="submit">continue</button>
				</form>
			)}

			{bad && <p className="bad">no</p>}
		</main>
	)
}

/** Account is the signed-in page: who you are, and how you sign in. */
function Account(props: { of: Providers; onSignOut: () => void }): React.ReactNode {
	const me = useQuery(MeService.method.get, {})

	if (me.state === 'pending') return <main className="loading">…</main>
	if (me.state === 'error') {
		return (
			<main className="error">
				<p>cannot read your record</p>
				<button onClick={props.onSignOut}>sign out</button>
			</main>
		)
	}

	const v = me.data
	const link = (name: string): void => {
		// A provider's round trip, started by somebody already signed in; the
		// session says whose account it is for. A form because it is a POST.
		const f = document.createElement('form')
		f.method = 'POST'
		f.action = `/ways?connection=${encodeURIComponent(name)}`
		document.body.appendChild(f)
		f.submit()
	}

	return (
		<div className="account">
			<nav>
				<h1>{props.of.tenant.name || props.of.tenant.alias}</h1>
				<span className="who">{v?.alias}</span>
				<button onClick={props.onSignOut}>sign out</button>
			</nav>
			<main>
				<section>
					<h2>you</h2>
					<p>
						<code>{v?.alias}</code>
						{v?.name !== undefined && v.name !== '' && <span> — {v.name}</span>}
					</p>
				</section>

				<section>
					<h2>signs in with</h2>
					<table>
						<tbody>
							{(v?.credentials ?? []).map((c) => (
								<tr key={`c:${c.kind}:${c.name}`}>
									<td>{c.kind}</td>
									<td>{c.name === '' ? <span className="none">the only one</span> : c.name}</td>
								</tr>
							))}
							{(v?.identities ?? []).map((i) => (
								<tr key={`i:${i.provider}:${i.subject}`}>
									<td>{i.provider}</td>
									<td className="mono">{i.subject}</td>
								</tr>
							))}
						</tbody>
					</table>
					{props.of.providers.length > 0 && (
						<p className="acts">
							{props.of.providers.map((p) => (
								<button key={p.name} onClick={() => link(p.name)}>
									add {p.name}
								</button>
							))}
						</p>
					)}
				</section>

				<section>
					<h2>keys</h2>
					{(v?.keys ?? []).length === 0 ? (
						<p className="none">none</p>
					) : (
						<ul className="methods">
							{(v?.keys ?? []).map((k) => (
								<li key={k.alias}>
									<code>{k.alias}</code> <span className="dim">{k.methods.join(', ')}</span>
								</li>
							))}
						</ul>
					)}
				</section>
			</main>
		</div>
	)
}

function Root(): React.ReactNode {
	const [of, setOf] = useState<Providers | null | undefined>(undefined)
	const [app, setApp] = useState<App | null | undefined>(undefined)

	useEffect(() => {
		void providers().then(setOf)
	}, [])

	// Whether there is a session is a question for roster: `Me.Get` through the
	// proxy answers who, or 401 -- which is the page's cue to draw the form.
	const check = (): void => {
		const transport = createConnectTransport({
			baseUrl: location.origin,
			fetch: (input, init) => fetch(input, { ...init, credentials: 'include' }),
		})
		void fetch('/roster.MeService/Get', {
			method: 'POST',
			headers: { 'content-type': 'application/json', 'connect-protocol-version': '1' },
			body: '{}',
			credentials: 'include',
		}).then(async (res) => {
			if (!res.ok) return setApp(null)
			setApp(await open(transport, 'account:' + location.host))
		})
	}
	useEffect(check, [])

	const out = (): void => {
		void fetch('/session', { method: 'DELETE' }).finally(() => {
			app?.store.forget()
			app?.store.close()
			setApp(null)
		})
	}

	if (of === undefined || app === undefined) return <main className="loading">…</main>
	if (of === null) return <main className="error">no operator here serves this name</main>
	if (app === null) return <SignIn of={of} onDone={check} />

	return (
		<Provider app={app}>
			<Account of={of} onSignOut={out} />
		</Provider>
	)
}

root.render(
	<StrictMode>
		<Root />
	</StrictMode>,
)
