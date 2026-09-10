import React, { useState } from 'react'

/**
 * The sign-in, for every page of roster's that draws one.
 *
 * # Why this is shared and the rest of a page is not
 *
 * `frontdoor/web/frontdoor.js` says what is worth extracting, having tried and
 * refused the component library D22 asked for: *three answers where a page
 * expects two, and a second form that must not be drawn from anything the
 * server said to call it.* That is this, and it is the whole of it -- the
 * markup around it is the app's, which is why the account page's own screens
 * and the Login App's consent screen are not here.
 *
 * The two pages that draw it want different things around the same middle. Both
 * offer the providers an operator wrote down; the account page offers a
 * recovery form and the Login App does not, because delivering a link is
 * outside roster and outside that app, and the Login App carries a challenge in
 * every URL. So what differs is props, and what does not is the part that was
 * got wrong before it was written down.
 *
 * # It is a page and not a client
 *
 * Every call here is to **this** origin -- `/session`, `/session/continue` --
 * which is the app's own protocol and not roster's. The three status codes are
 * `frontdoor`'s: 204 signed in, 200 one factor proved and another to prove, 401
 * everything else. One answer for a wrong password, an unknown person and a
 * locked account, which a page must not undo by guessing which.
 */

const json = (body: unknown): RequestInit => ({
	method: 'POST',
	headers: { 'content-type': 'application/json' },
	body: JSON.stringify(body),
})

export function b64url(v: Uint8Array): string {
	let s = ''
	for (const b of v) s += String.fromCharCode(b)

	return btoa(s).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '')
}

/** Factor is one second factor the first form said is left to prove. */
interface Factor {
	kind: string
	name: string
	id?: string
}

function b64urlDecode(v: string): Uint8Array<ArrayBuffer> {
	const s = v.replaceAll('-', '+').replaceAll('_', '/')
	const bin = atob(s + '='.repeat((4 - (s.length % 4)) % 4))
	const out = new Uint8Array(bin.length)
	for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)

	return out
}

/**
 * assert is the browser's half of a security key at sign-in: the challenge is
 * this page's, `navigator.credentials.get` is asked for exactly the key roster
 * offered, and what comes back is wrapped in the envelope `server/vouch`
 * checks -- relying party, origin, challenge, and the authenticator's answer.
 */
async function assertKey(f: Factor): Promise<string> {
	if (f.id === undefined) throw new Error('roster did not say which key')
	const challenge = crypto.getRandomValues(new Uint8Array(32))
	const cred = (await navigator.credentials.get({
		publicKey: {
			challenge,
			rpId: location.hostname,
			allowCredentials: [{ type: 'public-key', id: b64urlDecode(f.id) }],
			userVerification: 'preferred',
		},
	})) as (PublicKeyCredential & { toJSON?: () => unknown }) | null
	if (cred === null) throw new Error('no key answered')
	const r = cred.response as AuthenticatorAssertionResponse
	const response = cred.toJSON?.() ?? {
		id: cred.id,
		rawId: b64url(new Uint8Array(cred.rawId)),
		type: cred.type,
		response: {
			authenticatorData: b64url(new Uint8Array(r.authenticatorData)),
			clientDataJSON: b64url(new Uint8Array(r.clientDataJSON)),
			signature: b64url(new Uint8Array(r.signature)),
			userHandle: r.userHandle === null ? null : b64url(new Uint8Array(r.userHandle)),
		},
	}

	return JSON.stringify({ rp_id: location.hostname, origins: [location.origin], challenge: b64url(challenge), response })
}


/** Provider is one way in that is not a password. */
export interface Provider {
	name: string

	/**
	 * Where the browser is about to go, which is also the only honest way to
	 * know **whose** directory this is.
	 *
	 * The name is the operator's -- a `Connection` may be called `entra`, `ms`
	 * or `work` -- so drawing a mark from it would be drawing from a label,
	 * which D22 refuses. A host is a fact: `login.microsoftonline.com` is
	 * Microsoft whatever the row is called.
	 *
	 * Optional, because a front door may not send it and a name alone is a
	 * complete button.
	 */
	issuer?: string
}

/**
 * mark is the vendor's own, from the host the issuer names.
 *
 * Inline and not fetched: a sign-in page that reaches a third party to draw
 * itself tells that third party who is signing in and when, before anybody has
 * agreed to anything. These are trademarks used as the vendors ask them to be
 * -- on the button that signs somebody in with them -- and an unknown host
 * simply has no mark, which is a button with a name on it and nothing wrong.
 */
function mark(issuer: string | undefined): React.ReactNode {
	let host = ''
	try {
		host = new URL(issuer ?? '').hostname.toLowerCase()
	} catch {
		return null
	}

	const is = (...vs: string[]): boolean => vs.some((v) => host === v || host.endsWith('.' + v))

	// Microsoft's four squares, which Entra signs in with.
	if (is('microsoftonline.com', 'windows.net', 'microsoftonline.us', 'microsoft.com')) {
		return (
			<svg viewBox="0 0 21 21" aria-hidden="true">
				<path fill="#f25022" d="M0 0h10v10H0z" />
				<path fill="#7fba00" d="M11 0h10v10H11z" />
				<path fill="#00a4ef" d="M0 11h10v10H0z" />
				<path fill="#ffb900" d="M11 11h10v10H11z" />
			</svg>
		)
	}

	if (is('github.com')) {
		return (
			<svg viewBox="0 0 16 16" aria-hidden="true">
				<path
					fill="currentColor"
					d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82a7.4 7.4 0 0 1 2-.27c.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z"
				/>
			</svg>
		)
	}

	if (is('google.com', 'accounts.google.com')) {
		return (
			<svg viewBox="0 0 48 48" aria-hidden="true">
				<path fill="#4285f4" d="M45 24c0-1.6-.1-2.7-.4-4H24v7.5h12c-.2 2-1.5 5-4.4 7l6.7 5.2C42.2 36 45 30.6 45 24Z" />
				<path fill="#34a853" d="M24 46c5.9 0 10.9-2 14.5-5.3l-6.9-5.4c-1.9 1.3-4.4 2.2-7.6 2.2-5.8 0-10.7-3.8-12.5-9.1l-7.1 5.5C8.1 41.1 15.4 46 24 46Z" />
				<path fill="#fbbc05" d="M11.5 28.4a13.4 13.4 0 0 1 0-8.7l-7.1-5.5a22 22 0 0 0 0 19.8l7.1-5.6Z" />
				<path fill="#ea4335" d="M24 9.5c3.3 0 5.5 1.4 6.8 2.6l5.9-5.8C33.1 3 29.1 1 24 1 15.4 1 8.1 5.9 4.4 13.2l7.1 5.5C13.3 13.3 18.2 9.5 24 9.5Z" />
			</svg>
		)
	}

	if (is('okta.com', 'oktapreview.com')) {
		return (
			<svg viewBox="0 0 24 24" aria-hidden="true">
				<path fill="currentColor" d="M12 5.5A6.5 6.5 0 1 0 12 18.5 6.5 6.5 0 0 0 12 5.5Zm0 3.25a3.25 3.25 0 1 1 0 6.5 3.25 3.25 0 0 1 0-6.5Z" />
			</svg>
		)
	}

	return null
}

/** What a page has to say to draw a sign-in. */
export interface SignInProps {
	/** What to call the operator being signed in to. */
	brand: string

	/** Whether there is a password form at all. */
	password: boolean

	/** The providers to offer, and where a button for one goes. */
	providers: Provider[]
	providerHref?: (name: string) => string

	/**
	 * Where a recovery link is asked for, when this page offers one. The
	 * account page does; the Login App does not, because delivery is outside
	 * roster and outside it.
	 */
	recover?: string

	/**
	 * Where `/session` and `/session/continue` are, when they are not there.
	 * The Login App carries a challenge in every URL of a flow.
	 */
	at?: (path: string) => string

	onDone: () => void
}

export function SignIn(props: SignInProps): React.ReactNode {
	const [step, setStep] = useState<{ factors: Factor[] } | null>(null)
	const [mode, setMode] = useState<'in' | 'recover'>('in')
	const [bad, setBad] = useState(false)
	const [note, setNote] = useState<string | null>(null)
	const brand = props.brand
	const at = props.at ?? ((p: string) => p)

	const first = (e: React.FormEvent<HTMLFormElement>): void => {
		e.preventDefault()
		const f = new FormData(e.currentTarget)
		setBad(false)
		void fetch(at('/session'), json({ alias: String(f.get('alias') ?? ''), password: String(f.get('password') ?? '') })).then(
			async (res) => {
				if (res.status === 204) return props.onDone()
				if (res.status === 200) {
					const v = (await res.json()) as { factors?: Factor[] }
					return setStep({ factors: v.factors ?? [] })
				}
				setBad(true)
			},
		)
	}

	// `frontdoor` reads `{kind, name, secret}`: the secret is the code for an
	// authenticator app and the assertion envelope for a security key, and
	// `name` picks one of several of a kind.
	// One attempt at the second form per first form: a wrong code ends the
	// half-session on the app's side and spends the continuation on roster's
	// (`frontdoor.Door.Second`), so the answer to it is the first form again,
	// where the lockout counts. Left on the second form, every further code
	// would be refused with nothing to say why.
	const proceed = (f: Factor, secret: string): void => {
		setBad(false)
		void fetch(at('/session/continue'), json({ kind: f.kind, name: f.name, secret })).then((res) => {
			if (res.status === 204) return props.onDone()
			setBad(true)
			setStep(null)
		})
	}

	const second = (e: React.FormEvent<HTMLFormElement>): void => {
		e.preventDefault()
		const f = new FormData(e.currentTarget)
		const totp = step?.factors.find((v) => v.kind === 'totp')
		if (totp === undefined) return
		proceed(totp, String(f.get('code') ?? ''))
	}

	const withKey = (f: Factor): void => {
		void assertKey(f)
			.then((secret) => proceed(f, secret))
			.catch(() => setBad(true))
	}

	const recover = (e: React.FormEvent<HTMLFormElement>): void => {
		e.preventDefault()
		const f = new FormData(e.currentTarget)
		setNote(null)
		void fetch(props.recover ?? '/recover', json({ address: String(f.get('address') ?? '') })).then((res) => {
			// 202 whatever was typed: the page cannot know, and must not say,
			// whether an address is here.
			setNote(
				res.status === 202
					? 'if that address is here, a link is on its way'
					: res.status === 501
						? 'this deployment cannot send mail; ask an operator for a new password'
						: 'no',
			)
		})
	}

	return (
		<main className="sign-in">
			<h1>{brand}</h1>

			{mode === 'in' && props.password && step === null && (
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
					{props.recover !== undefined && (
						<button type="button" className="link" onClick={() => setMode('recover')}>
							forgot?
						</button>
					)}
				</form>
			)}

			{/*
				After the form, and not before it. Somebody who has a password
				types it without reading the page; somebody who does not is
				looking for their organisation and finds it under a rule that
				says the first list has ended. Providers first makes the common
				case scroll past them.

				The rule is drawn only when there is something on both sides of
				it -- a deployment with no password form has a list, not a
				second half.
			*/}
			{mode === 'in' && step === null && props.providers.length > 0 && (
				<>
					{props.password && <p className="or">or</p>}
					<section className="providers">
						{props.providers.map((p) => (
							<a key={p.name} className="button" href={props.providerHref?.(p.name) ?? '#'}>
								{/*
									Three parts and not one sentence, so that
									the three line up **down** the list: the
									mark's slot is a fixed width whether or not
									there is a mark in it, and "sign in with" is
									the same string in every one, so the names
									start at the same place however long the one
									above is. Centred, they did not: a long name
									pushed its own logo left.
								*/}
								<span className="mark">{mark(p.issuer)}</span>
								<span className="with">sign in with</span>
								<span className="who">{p.name}</span>
							</a>
						))}
					</section>
				</>
			)}

			{mode === 'in' && step !== null && (
				<>
					{step.factors.some((f) => f.kind === 'webauthn') && (
						<section className="providers">
							{step.factors
								.filter((f) => f.kind === 'webauthn')
								.map((f) => (
									<button key={`${f.kind}:${f.name}`} type="button" onClick={() => withKey(f)}>
										use your security key{f.name !== '' ? ` (${f.name})` : ''}
									</button>
								))}
						</section>
					)}
					{step.factors.some((f) => f.kind === 'totp') && (
						<form onSubmit={second}>
							<p className="note">one more: a code from your authenticator app</p>
							<label>
								code
								<input name="code" inputMode="numeric" autoComplete="one-time-code" autoFocus />
							</label>
							<button type="submit">continue</button>
						</form>
					)}
					{step.factors.length === 0 && <p className="bad">a second factor is required and none can be offered here</p>}
				</>
			)}

			{mode === 'recover' && (
				<form onSubmit={recover}>
					<p className="note">
						A link goes to the address on your account. It hands you a new password, once,
						and signs you out of everything else.
					</p>
					<label>
						address
						<input name="address" type="email" autoFocus autoComplete="email" />
					</label>
					<button type="submit">send a link</button>
					<button type="button" className="link" onClick={() => setMode('in')}>
						back
					</button>
				</form>
			)}

			{bad && <p className="bad">{step === null ? 'no' : 'no; start again from your password'}</p>}
			{note !== null && <p className="note">{note}</p>}
		</main>
	)
}
