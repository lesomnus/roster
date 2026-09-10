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

	/** Set on the one flow that asks nothing and offers no way in: a sign-out
	 * nobody proved an app started. There is no `client` on it, because a
	 * logout that named one is one Hydra answers without drawing anything.
	 */
	logout?: boolean
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
	for (const name of ['login_challenge', 'consent_challenge', 'logout_challenge']) {
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

/**
 * says is what a scope means, in words.
 *
 * The names are OIDC's and they are for a client to send, not for a person to
 * read: nobody outside this trade knows what `profile` covers, and a screen
 * that lists it has asked somebody to agree to a word. So the known ones are
 * spelled out and anything else is shown as it came -- an operator's own scope
 * is theirs to name, and inventing a sentence for one would be worse than
 * printing it.
 *
 * This is the **page** deciding, from a fact the app sent. The app sends the
 * scopes the client asked for and never a line of text to render, which is the
 * refusal D22 asks for; what to call them where somebody is reading is not the
 * server's to say.
 */
function says(scope: string): { what: string; how?: string } {
	switch (scope) {
		case 'openid':
			return { what: 'who you are', how: 'the identifier this account is known by' }
		case 'profile':
			return { what: 'your name', how: 'what you are called here, and the teams you are in' }
		case 'email':
			return { what: 'your email address', how: 'the one that has been confirmed, if there is one' }
		case 'offline':
		case 'offline_access':
			return { what: 'to stay signed in', how: 'so it does not ask again every time you open it' }
		default:
			return { what: scope }
	}
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
		<main className="sign-in consent">
			{/*
				Whose deployment this is, above whose app is asking. A consent
				screen is the one place somebody is being asked to agree to
				something, and the first question they have is where they are.
			*/}
			<p className="at">{props.of.brand}</p>

			<h1>
				<strong>{props.of.client}</strong> wants to sign you in
			</h1>

			<ul className="scopes">
				{props.of.scope.map((s) => {
					const v = says(s)

					return (
						<li key={s}>
							<span className="what">{v.what}</span>
							{v.how !== undefined && <span className="how">{v.how}</span>}
						</li>
					)
				})}
				{props.of.scope.length === 0 && (
					<li>
						<span className="what">who you are</span>
						<span className="how">nothing beyond the identifier this account is known by</span>
					</li>
				)}
			</ul>

			{/*
				Two buttons and no checkboxes. A screen that lets somebody grant
				less than a client asked for reads like a choice and is not one:
				an app that asked for a scope generally stops working without
				it, so the person is picking between "allow" and "allow, then
				find out something is broken".

				`allow` is the act and looks like one; `no` is a real button and
				not a link, because refusing is an answer this screen is asking
				for rather than a way out of it.
			*/}
			<div className="acts">
				<button type="button" className="go" disabled={sent} onClick={() => answer(true)}>
					allow
				</button>
				<button type="button" disabled={sent} onClick={() => answer(false)}>
					no
				</button>
			</div>

			<p className="note">
				You are agreeing to <strong>{props.of.client}</strong> knowing this, not to it acting for
				you elsewhere.
			</p>
		</main>
	)
}

/** Logout is the confirmation for a sign-out no relying party proved it asked
 * for -- which is any sign-out that arrived without an `id_token_hint`, and is
 * far more of them than the name suggests.
 *
 * It exists because a third party can send a browser here: a link, or a page
 * that loaded the address as an image. What that gets them is a question. What
 * it must not get them is somebody signed out without being asked, and what the
 * person who really did click sign out must not get is a page saying no.
 */
function Logout(props: { of: Flow }): React.ReactNode {
	const [sent, setSent] = useState(false)
	const [stayed, setStayed] = useState(false)

	const answer = (allow: boolean): void => {
		setSent(true)
		const body = new URLSearchParams({ logout_challenge: c.value })
		if (allow) body.set('allow', '1')
		void fetch('/logout', {
			method: 'POST',
			headers: { 'content-type': 'application/x-www-form-urlencoded' },
			body,
		})
			.then(async (res) => (res.ok ? ((await res.json()) as { signed_out: boolean; to?: string }) : Promise.reject(new Error('no'))))
			.then((v) => {
				// A no has nowhere to send the browser, because there is no
				// relying party waiting -- that is the whole reason this screen
				// was drawn. So it says what happened and stops.
				if (v.signed_out && v.to !== undefined) location.assign(v.to)
				else setStayed(true)
			})
			.catch(() => setStayed(true))
	}

	if (stayed) {
		return (
			<main className="sign-in consent">
				<h1>you are still signed in</h1>
				<p className="note">Nothing changed. You can close this page.</p>
			</main>
		)
	}

	return (
		<main className="sign-in consent">
			{props.of.brand !== '' && <p className="at">{props.of.brand}</p>}

			<h1>sign out?</h1>

			{/*
				It does not say which app asked, because nothing here knows: a
				request that proved which one is a request this screen is never
				drawn for. Naming a likely one would be a guess on a screen
				somebody is about to trust.
			*/}
			<p className="note">
				This signs you out here. Apps you are already signed in to may keep their own session
				until it runs out.
			</p>

			<div className="acts">
				<button type="button" className="go" disabled={sent} onClick={() => answer(true)}>
					sign out
				</button>
				<button type="button" disabled={sent} onClick={() => answer(false)}>
					stay signed in
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
	if (c.name === 'logout_challenge') return <Logout of={of} />

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
