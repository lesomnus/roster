/**
 * The Login App's page: the two forms, and the consent screen after them.
 *
 * # Why this is a page here and not one file of HTML
 *
 * It was one file of HTML for a day, on a reason that turned out to be about
 * something else. `frontdoor/web/frontdoor.js` has no build **on purpose**, so
 * that somebody else's Go product app can mount one route and write plain
 * markup -- and `examples/sso/account.html` is that page. roster's own pages
 * were never the subject, and roster already had one drawing this exact flow:
 * the account page, in React, with the second form, a security key and the
 * rule about one attempt per first form. The plain copy was a worse one.
 *
 * So the sign-in is `ts/lib/signin.tsx` and both pages draw it. What differs is
 * props: this one offers no recovery, because delivering a link is outside
 * roster and outside this app, and it carries the challenge Hydra redirected
 * with in every URL of the flow.
 *
 * # What is this app's own protocol, and what is not
 *
 * All of it. Unlike the console and the account page there is not one Connect
 * call here: what this page needs about a flow, it asks the app it is served
 * by, because the app is the only thing that may ask Hydra. `/flow` is that --
 * who is being signed in to, which client is asking, and for what.
 */
import { StrictMode, useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'

import { SignIn, type Provider } from '../lib/signin.js'
import '../lib/style.css'

/** Flow is what the app says about the challenge in this page's URL. */
interface Flow {
	brand: string
	client: string
	scope: string[]

	/** The ways in this operator has, which the app read out of roster. */
	providers: Provider[]
	password: boolean
}

/** The challenge, from the address the browser arrived at.
 *
 * Not a cookie: Hydra's is about two kilobytes against a cookie's four, which
 * is too close to design against -- and it is not a secret, because the browser
 * arrived holding it. What it is not, either way, is **believed**: the app
 * looks it up at Hydra rather than reading anything out of it.
 */
function challenge(): { name: string; value: string } {
	const q = new URLSearchParams(location.search)
	for (const name of ['login_challenge', 'consent_challenge']) {
		const value = q.get(name)
		if (value !== null && value !== '') return { name, value }
	}

	return { name: 'login_challenge', value: '' }
}

const c = challenge()
const at = (path: string): string => `${path}?${c.name}=${encodeURIComponent(c.value)}`

function Broken(): React.ReactNode {
	return (
		<main className="sign-in">
			<h1>this login is not working</h1>
			<p className="note">Nothing is wrong with what you typed. An operator has to look at it.</p>
		</main>
	)
}

/** Consent is the screen a deployment asks for, when it asks for one. */
function Consent(props: { of: Flow }): React.ReactNode {
	const [sent, setSent] = useState(false)

	const answer = (allow: boolean): void => {
		setSent(true)
		const body = new URLSearchParams({ consent_challenge: c.value })
		if (allow) body.set('allow', '1')
		void fetch('/consent', {
			method: 'POST',
			headers: { 'content-type': 'application/x-www-form-urlencoded' },
			body,
			redirect: 'follow',
		}).then((res) => location.assign(res.url))
	}

	return (
		<main className="sign-in">
			<h1>{props.of.client}</h1>
			<p>wants to know who you are and to see:</p>
			<ul>
				{props.of.scope.map((s) => (
					<li key={s}>{s}</li>
				))}
				{props.of.scope.length === 0 && <li className="note">nothing beyond who you are</li>}
			</ul>

			{/*
				Two buttons and no checkboxes. A screen that lets somebody grant
				less than a client asked for reads like a choice and is not one:
				an app that asked for a scope generally stops working without
				it, so the person is picking between "allow" and "allow, then
				find out something is broken".
			*/}
			<div className="acts">
				<button type="button" disabled={sent} onClick={() => answer(true)}>
					allow
				</button>
				<button type="button" className="link" disabled={sent} onClick={() => answer(false)}>
					no
				</button>
			</div>
		</main>
	)
}

function Root(): React.ReactNode {
	const [of, setOf] = useState<Flow | null>(null)
	const [bad, setBad] = useState(false)

	useEffect(() => {
		void fetch(at('/flow'))
			.then(async (res) => (res.ok ? ((await res.json()) as Flow) : Promise.reject(new Error('no'))))
			.then(setOf)
			.catch(() => setBad(true))
	}, [])

	if (bad) return <Broken />
	if (of === null) return <main className="sign-in" />

	if (c.name === 'consent_challenge') return <Consent of={of} />

	// The last hop, and the only thing on this page that is not an ordinary
	// sign-in: roster has said who this is, and Hydra is waiting to be told.
	const done = (): void => {
		void fetch(at('/accept'), { method: 'POST' })
			.then(async (res) => (res.ok ? ((await res.json()) as { redirect_to: string }) : Promise.reject(new Error('no'))))
			.then((v) => location.assign(v.redirect_to))
			.catch(() => setBad(true))
	}

	// A provider button leaves this page for the operator's directory and comes
	// back to `/callback`, which finishes the flow without ever returning here.
	// So the challenge travels on the link, the way it does on every other call.
	return (
		<SignIn
			brand={of.brand}
			password={of.password}
			providers={of.providers}
			providerHref={(name) => `${at('/provider')}&connection=${encodeURIComponent(name)}`}
			at={at}
			onDone={done}
		/>
	)
}

createRoot(document.getElementById('root') as HTMLElement).render(
	<StrictMode>
		<Root />
	</StrictMode>,
)
