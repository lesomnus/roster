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

			{mode === 'in' && props.providers.length > 0 && (
				<section className="providers">
					{props.providers.map((p) => (
						<a key={p.name} className="button" href={props.providerHref?.(p.name) ?? '#'}>
							sign in with {p.name}
						</a>
					))}
				</section>
			)}

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
