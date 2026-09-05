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
 * What is not a Connect call is the app's own protocol: the sign-in itself
 * (`/session`, `/session/continue`, a provider's round trip through `/login`
 * and `/ways`), `/providers` which the page needs before it has a session, and
 * the two flows a mailed link finishes (`/recover`, `/verify`), because roster
 * mints and the app delivers.
 *
 * # Every write names the person's own row
 *
 * There is no self-only verb in roster (CLAUDE.md, *no self-only twin of a
 * verb*): the page calls `Holder.Update`, `Email.Add`, `Credential.Set`,
 * `Credential.Enrol`, `Credential.Erase`, `Delegation.Erase`, `ApiKey.Issue` and `ApiKey.Erase`
 * with the reference `Me.Get` answered, and roster's layer lets a person write
 * their own row. The role the deployment gave them has to name each method --
 * a delegation narrows to the intersection -- so a section whose call the role
 * does not cover says so rather than drawing a button that would be refused.
 */

import { createConnectTransport } from '@connectrpc/connect-web'
import { StrictMode, useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { Provider, useApp, useCall, useQuery } from '@lesomnus/payday/react'
import type { App } from '@lesomnus/payday/react'

import { ApiKeyService } from '../gen/app/apikey_svc_pb.js'
import { CredentialService } from '../gen/app/credential_svc_pb.js'
import { DelegationService } from '../gen/app/delegation_svc_pb.js'
import { EmailService } from '../gen/app/email_svc_pb.js'
import { MeService } from '../gen/app/me_pb.js'
import { HolderService } from '../gen/roster/payday/holder_svc_pb.js'
import { covers } from '../lib/covers.js'
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

const json = (body: unknown): RequestInit => ({
	method: 'POST',
	headers: { 'content-type': 'application/json' },
	body: JSON.stringify(body),
})

function b64url(v: Uint8Array): string {
	let s = ''
	for (const b of v) s += String.fromCharCode(b)

	return btoa(s).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '')
}

function uuid(v: Uint8Array | undefined): string {
	if (v === undefined || v.length !== 16) return ''
	const h = [...v].map((b) => b.toString(16).padStart(2, '0')).join('')

	return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

function when(v: { seconds: bigint } | undefined): string {
	if (v === undefined) return ''

	return new Date(Number(v.seconds) * 1000).toISOString().replace('T', ' ').slice(0, 16)
}

function said(e: unknown): string {
	return e instanceof Error ? e.message : 'no'
}

const ref = (id: Uint8Array) => ({ key: { case: 'id' as const, value: id } })

/**
 * SignIn is the first form and, when roster asks for it, the second -- and the
 * recovery form for somebody who has neither.
 *
 * Three answers, three status codes (`frontdoor`): 204 signed in, 200 one
 * factor proved and another to prove, 401 everything else -- one answer for a
 * wrong password, an unknown person and a locked account, which the page must
 * not undo by guessing which.
 */
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

function SignIn(props: { of: Providers; onDone: () => void }): React.ReactNode {
	const [step, setStep] = useState<{ factors: Factor[] } | null>(null)
	const [mode, setMode] = useState<'in' | 'recover'>('in')
	const [bad, setBad] = useState(false)
	const [note, setNote] = useState<string | null>(null)
	const brand = props.of.tenant.name !== '' ? props.of.tenant.name : props.of.tenant.alias

	const first = (e: React.FormEvent<HTMLFormElement>): void => {
		e.preventDefault()
		const f = new FormData(e.currentTarget)
		setBad(false)
		void fetch('/session', json({ alias: String(f.get('alias') ?? ''), password: String(f.get('password') ?? '') })).then(
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
		void fetch('/session/continue', json({ kind: f.kind, name: f.name, secret })).then((res) => {
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
		void fetch('/recover', json({ address: String(f.get('address') ?? '') })).then((res) => {
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

			{mode === 'in' && props.of.providers.length > 0 && (
				<section className="providers">
					{props.of.providers.map((p) => (
						<a key={p.name} className="button" href={`/login?connection=${encodeURIComponent(p.name)}`}>
							sign in with {p.name}
						</a>
					))}
				</section>
			)}

			{mode === 'in' && props.of.password && step === null && (
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
					<button type="button" className="link" onClick={() => setMode('recover')}>
						forgot?
					</button>
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

/** Account is the signed-in page, section by section. */
function Account(props: { of: Providers; onSignOut: () => void }): React.ReactNode {
	const me = useQuery(MeService.method.get, {})

	if (me.state === 'pending') return <main className="loading">…</main>
	if (me.state === 'error' || me.data === undefined) {
		return (
			<main className="error">
				<p>cannot read your record</p>
				<button onClick={props.onSignOut}>sign out</button>
			</main>
		)
	}

	const v = me.data
	const own = v.id
	const held = v.methods
	const may = (method: string): boolean => held.some((m) => covers(m, method))

	return (
		<div className="account">
			<nav>
				<h1>{props.of.tenant.name || props.of.tenant.alias}</h1>
				<span className="who">{v.alias}</span>
				<button onClick={props.onSignOut}>sign out</button>
			</nav>
			<main>
				<Profile own={own} alias={v.alias} may={may} />
				<Ways of={props.of} own={own} identities={v.identities} credentials={v.credentials} may={may} />
				<Addresses own={own} may={may} />
				<Password own={own} may={may} />
				<Factors own={own} alias={v.alias} brand={props.of.tenant.name || props.of.tenant.alias} may={may} />
				<Sessions own={own} may={may} />
				<Keys own={own} keys={v.keys} may={may} />
			</main>
		</div>
	)
}

/** Needs says which grant a section is waiting on, instead of drawing a dead button. */
function Needs(props: { method: string }): React.ReactNode {
	return <p className="none">this needs a role naming {props.method}</p>
}

function Profile(props: { own: Uint8Array; alias: string; may: (m: string) => boolean }): React.ReactNode {
	const allowed = props.may('/roster.HolderService/Get') && props.may('/roster.HolderService/Update')
	const row = useQuery(HolderService.method.get, allowed ? { ref: ref(props.own) } : {})
	const update = useCall(HolderService.method.update)
	const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null)

	return (
		<section>
			<h2>you</h2>
			<p>
				<code>{props.alias}</code>
			</p>
			{!allowed && <Needs method="/roster.HolderService/Update" />}
			{allowed && row.state === 'ok' && row.data !== undefined && (
				<form
					className="profile"
					onSubmit={(e) => {
						e.preventDefault()
						const f = new FormData(e.currentTarget)
						const s = (k: string) => String(f.get(k) ?? '').trim()
						setNote(null)
						void update
							.call({
								ref: ref(props.own),
								dateUpdated: row.data?.dateUpdated,
								profile: {
									displayName: s('display_name'),
									picture: s('picture'),
									department: s('department'),
									employeeNo: s('employee_no'),
									locale: s('locale'),
								},
							})
							.then(() => setNote({ ok: true, text: 'saved' }))
							.catch((e: unknown) => setNote({ ok: false, text: said(e) }))
					}}
				>
					<input name="display_name" placeholder="display name" defaultValue={row.data.profile?.displayName ?? ''} />
					<input name="department" placeholder="department" defaultValue={row.data.profile?.department ?? ''} />
					<input name="employee_no" placeholder="employee no" defaultValue={row.data.profile?.employeeNo ?? ''} />
					<input name="locale" placeholder="locale" defaultValue={row.data.profile?.locale ?? ''} />
					<input name="picture" placeholder="picture url" defaultValue={row.data.profile?.picture ?? ''} />
					<button type="submit" disabled={update.state === 'pending'}>
						save
					</button>
				</form>
			)}
			{note !== null && <p className={note.ok ? 'note' : 'bad'}>{note.text}</p>}
		</section>
	)
}

/** Ways is how the person signs in, and the provider accounts they can add or take back. */
function Ways(props: {
	of: Providers
	own: Uint8Array
	identities: { id: Uint8Array; provider: string; subject: string }[]
	credentials: { kind: string; name: string }[]
	may: (m: string) => boolean
}): React.ReactNode {
	const unlink = useCall(MeService.method.unlink)
	const [gone, setGone] = useState<string[]>([])
	const [bad, setBad] = useState<string | null>(null)

	const link = (name: string): void => {
		const f = document.createElement('form')
		f.method = 'POST'
		f.action = `/ways?connection=${encodeURIComponent(name)}`
		document.body.appendChild(f)
		f.submit()
	}

	const ids = props.identities.filter((i) => !gone.includes(uuid(i.id)))
	const passwords = props.credentials.filter((c) => c.kind === 'password')

	return (
		<section>
			<h2>signs in with</h2>
			<table>
				<tbody>
					{passwords.map((c) => (
						<tr key="password">
							<td>password</td>
							<td className="dim">{c.name === '' ? '' : c.name}</td>
							<td />
						</tr>
					))}
					{ids.map((i) => (
						<tr key={uuid(i.id)}>
							<td>{i.provider}</td>
							<td className="mono">{i.subject}</td>
							<td>
								{/* `Me.Unlink` is waived: taking back a way in needs no
								    role. roster refuses the last one. */}
								<button
									onClick={() => {
										setBad(null)
										void unlink
											.call({ id: i.id })
											.then(() => setGone((was) => [...was, uuid(i.id)]))
											.catch((e: unknown) => setBad(said(e)))
									}}
								>
									unlink
								</button>
							</td>
						</tr>
					))}
				</tbody>
			</table>
			{props.of.providers.length > 0 && (
				<p className="acts">
					{props.of.providers.map((p) => (
						<button key={p.name} disabled={!props.may('/roster.IdentityService/Add')} onClick={() => link(p.name)}>
							add {p.name}
						</button>
					))}
					{!props.may('/roster.IdentityService/Add') && <Needs method="/roster.IdentityService/Add" />}
				</p>
			)}
			{bad !== null && <p className="bad">{bad}</p>}
		</section>
	)
}

/** Addresses is where recovery goes, and whether anybody checked each. */
function Addresses(props: { own: Uint8Array; may: (m: string) => boolean }): React.ReactNode {
	const allowed = props.may('/roster.EmailService/List')
	const vs = useQuery(EmailService.method.list, allowed ? { filters: [{ holder: ref(props.own) }] } : {})
	const add = useCall(EmailService.method.add)
	const erase = useCall(EmailService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null)

	if (!allowed) {
		return (
			<section>
				<h2>addresses</h2>
				<Needs method="/roster.EmailService/List" />
			</section>
		)
	}

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section>
			<h2>addresses</h2>
			<p className="note">where a recovery link goes. An address counts once it is confirmed from your mailbox.</p>
			{items.length === 0 && <p className="none">none — a recovery link has nowhere to go</p>}
			{items.length > 0 && (
				<table>
					<tbody>
						{items.map((v) => (
							<tr key={uuid(v.id)}>
								<td className="mono">{v.address}</td>
								<td>
									{v.dateVerified === undefined ? (
										<button
											disabled={!props.may('/roster.EmailService/Verify')}
											onClick={() => {
												setNote(null)
												void fetch('/verify', json({ id: b64url(v.id) })).then((res) =>
													setNote(
														res.status === 202
															? { ok: true, text: `a link is on its way to ${v.address}` }
															: { ok: false, text: res.status === 501 ? 'this deployment cannot send mail' : 'no' },
													),
												)
											}}
										>
											confirm
										</button>
									) : (
										<span className="dim">confirmed {when(v.dateVerified)}</span>
									)}
								</td>
								<td>
									<button
										disabled={!props.may('/roster.EmailService/Erase')}
										onClick={() => {
											setNote(null)
											void erase
												.call(ref(v.id))
												.then(() => setGone((was) => [...was, uuid(v.id)]))
												.catch((e: unknown) => setNote({ ok: false, text: said(e) }))
										}}
									>
										remove
									</button>
								</td>
							</tr>
						))}
					</tbody>
				</table>
			)}
			<form
				onSubmit={(e) => {
					e.preventDefault()
					const form = e.currentTarget
					const address = String(new FormData(form).get('address') ?? '').trim()
					if (address === '') return
					setNote(null)
					void add
						.call({ holder: ref(props.own), address })
						.then(() => form.reset())
						.catch((e: unknown) => setNote({ ok: false, text: said(e) }))
				}}
			>
				<input name="address" type="email" placeholder="you@example.com" required />
				<button type="submit" disabled={add.state === 'pending' || !props.may('/roster.EmailService/Add')}>
					add address
				</button>
			</form>
			{note !== null && <p className={note.ok ? 'note' : 'bad'}>{note.text}</p>}
		</section>
	)
}

/** Password is `Credential.Set` on your own row, which asks for the one you hold. */
function Password(props: { own: Uint8Array; may: (m: string) => boolean }): React.ReactNode {
	const set = useCall(CredentialService.method.set)
	const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null)
	const allowed = props.may('/roster.CredentialService/Set')

	return (
		<section>
			<h2>password</h2>
			{!allowed && <Needs method="/roster.CredentialService/Set" />}
			{allowed && (
				<form
					onSubmit={(e) => {
						e.preventDefault()
						const form = e.currentTarget
						const f = new FormData(form)
						const enc = new TextEncoder()
						setNote(null)
						void set
							.call({
								ref: ref(props.own),
								current: enc.encode(String(f.get('current') ?? '')),
								secret: enc.encode(String(f.get('next') ?? '')),
							})
							.then(() => {
								form.reset()
								setNote({ ok: true, text: 'changed' })
							})
							.catch((e: unknown) => setNote({ ok: false, text: said(e) }))
					}}
				>
					<input name="current" type="password" placeholder="current" autoComplete="current-password" required />
					<input name="next" type="password" placeholder="new" autoComplete="new-password" required />
					<button type="submit" disabled={set.state === 'pending'}>
						change
					</button>
				</form>
			)}
			<p className="note">
				Your own row asks for the password you hold, so a credential that merely acts as you cannot
				replace it. Lost it? Sign out and use <em>forgot?</em>
			</p>
			{note !== null && <p className={note.ok ? 'note' : 'bad'}>{note.text}</p>}
		</section>
	)
}

/**
 * Factors is the second factors: an authenticator app (`totp`), a security key
 * (`webauthn`), each enrolled on your own row and removed from it.
 *
 * The WebAuthn ceremony is the browser's: this page makes the challenge, asks
 * `navigator.credentials.create`, and hands roster the envelope it checks --
 * the relying party, the origin, the challenge, and what the authenticator
 * answered (`server/vouch/webauthn.go`).
 */
function Factors(props: { own: Uint8Array; alias: string; brand: string; may: (m: string) => boolean }): React.ReactNode {
	const me = useQuery(MeService.method.get, {})
	const enrol = useCall(CredentialService.method.enrol)
	const erase = useCall(CredentialService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	// The factors are read off `Me.Get`, which no write here answers with: an
	// enrolment answers a seed, a proof is a fetch to this app, and the store
	// cannot know either changed the record. So it is told to ask again.
	const app = useApp()
	const [, bump] = useState(0)
	const reread = (): void => {
		app.queries.forget(MeService.method.get)
		bump((n) => n + 1)
	}
	const [seed, setSeed] = useState<{ uri: string; seed: string; name: string } | null>(null)
	const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null)

	const factors = (me.data?.credentials ?? []).filter((c) => c.kind !== 'password' && !gone.includes(`${c.kind}:${c.name}`))

	// `/prove` is `Vouch.Verify` about you, made by the app: one code from the
	// app you just scanned, so the factor counts now rather than at your next
	// sign-in -- and a mis-scanned QR is found here, not when you are half in.
	const prove = (e: React.FormEvent<HTMLFormElement>): void => {
		e.preventDefault()
		if (seed === null) return
		const code = String(new FormData(e.currentTarget).get('code') ?? '')
		setNote(null)
		void fetch('/prove', json({ kind: 'totp', name: seed.name, secret: code })).then((res) => {
			if (res.status === 204) {
				setSeed(null)
				setNote({ ok: true, text: 'proved; it counts from now on' })
				reread()
			} else {
				setNote({ ok: false, text: 'that code did not match; scan again and try once more' })
			}
		})
	}
	const byKind = (kind: string, name: string) => ({
		key: { case: 'kind' as const, value: { holder: ref(props.own), kind, name } },
	})

	const totp = (name: string): void => {
		setNote(null)
		setSeed(null)
		void enrol
			.call({ ref: ref(props.own), kind: 'totp', name, issuer: props.brand })
			.then((r) => setSeed({ uri: r.uri, seed: r.seed, name }))
			.catch((e: unknown) => setNote({ ok: false, text: said(e) }))
	}

	const key = async (name: string): Promise<void> => {
		setNote(null)
		try {
			const challenge = crypto.getRandomValues(new Uint8Array(32))
			const cred = (await navigator.credentials.create({
				publicKey: {
					challenge,
					rp: { id: location.hostname, name: props.brand },
					user: { id: new Uint8Array(props.own), name: props.alias, displayName: props.alias },
					pubKeyCredParams: [
						{ type: 'public-key', alg: -7 },
						{ type: 'public-key', alg: -257 },
						{ type: 'public-key', alg: -8 },
					],
					authenticatorSelection: { userVerification: 'preferred' },
				},
			})) as (PublicKeyCredential & { toJSON?: () => unknown }) | null
			if (cred === null) throw new Error('no key was made')

			const r = cred.response as AuthenticatorAttestationResponse
			const response = cred.toJSON?.() ?? {
				id: cred.id,
				rawId: b64url(new Uint8Array(cred.rawId)),
				type: cred.type,
				response: {
					attestationObject: b64url(new Uint8Array(r.attestationObject)),
					clientDataJSON: b64url(new Uint8Array(r.clientDataJSON)),
				},
			}
			const envelope = { rp_id: location.hostname, origins: [location.origin], challenge: b64url(challenge), response }
			await enrol.call({
				ref: ref(props.own),
				kind: 'webauthn',
				name,
				attestation: new TextEncoder().encode(JSON.stringify(envelope)),
			})
			setNote({ ok: true, text: `${name} is enrolled and counts from now on: the ceremony that enrolled it proved it` })
			reread()
		} catch (e) {
			setNote({ ok: false, text: said(e) })
		}
	}

	return (
		<section>
			<h2>second factors</h2>
			{factors.length === 0 && <p className="none">none — a password alone signs you in</p>}
			{factors.length > 0 && (
				<table>
					<tbody>
						{factors.map((c) => (
							<tr key={`${c.kind}:${c.name}`}>
								<td>{c.kind === 'totp' ? 'authenticator app' : c.kind === 'webauthn' ? 'security key' : c.kind}</td>
								<td>{c.name === '' ? <span className="dim">the only one</span> : c.name}</td>
								<td>
									<button
										disabled={!props.may('/roster.CredentialService/Erase')}
										onClick={() => {
											setNote(null)
											void erase
												.call(byKind(c.kind, c.name))
												.then(() => {
													setGone((was) => [...was, `${c.kind}:${c.name}`])
													reread()
												})
												.catch((e: unknown) => setNote({ ok: false, text: said(e) }))
										}}
									>
										remove
									</button>
								</td>
							</tr>
						))}
					</tbody>
				</table>
			)}
			{!props.may('/roster.CredentialService/Enrol') && <Needs method="/roster.CredentialService/Enrol" />}
			{props.may('/roster.CredentialService/Enrol') && (
				<form
					onSubmit={(e) => {
						e.preventDefault()
						const f = new FormData(e.currentTarget)
						const name = String(f.get('name') ?? '').trim()
						const kind = String(f.get('kind') ?? 'totp')
						if (kind === 'webauthn') void key(name)
						else totp(name)
					}}
				>
					<select name="kind" defaultValue="totp">
						<option value="totp">authenticator app</option>
						<option value="webauthn">security key</option>
					</select>
					<input name="name" placeholder="name it, e.g. phone" />
					<button type="submit" disabled={enrol.state === 'pending'}>
						enrol
					</button>
				</form>
			)}
			{seed !== null && (
				<div className="secret">
					<p>
						Scan this, then prove it with one code: the factor does not count until it is proved.
						Shown <strong>once</strong>.
					</p>
					<code>{seed.uri}</code>
					<p className="note">
						or type the seed: <code>{seed.seed}</code>
					</p>
					<form onSubmit={prove}>
						<input name="code" inputMode="numeric" autoComplete="one-time-code" placeholder="the code it shows" required />
						<button type="submit">prove</button>
					</form>
				</div>
			)}
			{note !== null && <p className={note.ok ? 'note' : 'bad'}>{note.text}</p>}
		</section>
	)
}

/** Sessions is where you are signed in -- your delegations -- and ending one, or all. */
function Sessions(props: { own: Uint8Array; may: (m: string) => boolean }): React.ReactNode {
	const allowed = props.may('/roster.DelegationService/List')
	const vs = useQuery(DelegationService.method.list, allowed ? { filters: [{ holder: ref(props.own) }] } : {})
	const erase = useCall(DelegationService.method.erase)
	const everywhere = useCall(MeService.method.signOutEverywhere)
	const [gone, setGone] = useState<string[]>([])
	const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null)

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section>
			<h2>where you are signed in</h2>
			{!allowed && <Needs method="/roster.DelegationService/List" />}
			{allowed && items.length === 0 && <p className="none">nowhere but here</p>}
			{allowed && items.length > 0 && (
				<table>
					<tbody>
						{items.map((v) => (
							<tr key={uuid(v.id)}>
								<td className="dim">since {when(v.dateCreated)}</td>
								<td className="dim">until {when(v.dateExpires)}</td>
								<td className="mono">{v.methods.length} method(s)</td>
								<td>
									<button
										disabled={!props.may('/roster.DelegationService/Erase')}
										onClick={() => {
											setNote(null)
											void erase
												.call(ref(v.id))
												.then(() => setGone((was) => [...was, uuid(v.id)]))
												.catch((e: unknown) => setNote({ ok: false, text: said(e) }))
										}}
									>
										end
									</button>
								</td>
							</tr>
						))}
					</tbody>
				</table>
			)}
			<p className="acts">
				{/* Waived: nobody may be refused this for want of a role. It voids
				    everything issued before now, including this session's
				    delegation -- the page reloads into the sign-in form. */}
				<button
					onClick={() => {
						setNote(null)
						void everywhere
							.call({})
							.then(() => location.reload())
							.catch((e: unknown) => setNote({ ok: false, text: said(e) }))
					}}
				>
					sign out everywhere
				</button>
			</p>
			{note !== null && <p className={note.ok ? 'note' : 'bad'}>{note.text}</p>}
		</section>
	)
}

/**
 * Keys is what a machine of yours may call as you, minted here and revoked here.
 *
 * An **app password** is one of them: a key whose only method is `Me.Get`, for
 * a client that speaks a protocol with a password field and nothing else --
 * LDAP, IMAP. `roster ldap serve` binds with it by reading who it belongs to
 * (`docs/ldap.md` § The bind). Nothing new in roster: the same `ApiKey.Issue`,
 * with the methods filled in, so the person types the app's name and nothing
 * they would have to look up.
 */
const appPasswordMethods = ['/roster.MeService/Get']

function Keys(props: {
	own: Uint8Array
	keys: { id: Uint8Array; alias: string; methods: string[]; dateUsed?: { seconds: bigint } | undefined }[]
	may: (m: string) => boolean
}): React.ReactNode {
	const issue = useCall(ApiKeyService.method.issue)
	const revoke = useCall(ApiKeyService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [made, setMade] = useState<{ id: Uint8Array; alias: string; methods: string[] }[]>([])
	const [token, setToken] = useState<string | null>(null)
	const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null)

	// One list, both halves filtered: a key minted a moment ago and revoked
	// the moment after is gone too, which the browser found it was not.
	const keys = [...props.keys, ...made].filter((k) => !gone.includes(uuid(k.id)))

	const mint = (form: HTMLFormElement, alias: string, methods: string[]): void => {
		if (alias === '' || methods.length === 0) return
		setNote(null)
		setToken(null)
		void issue
			.call({ holder: ref(props.own), alias, methods })
			.then((r) => {
				form.reset()
				setToken(r.token)
				if (r.key !== undefined) setMade((was) => [...was, { id: r.key!.id, alias: r.key!.alias, methods: r.key!.methods }])
			})
			.catch((e: unknown) => setNote({ ok: false, text: said(e) }))
	}

	return (
		<section>
			<h2>keys</h2>
			{keys.length === 0 && <p className="none">none — nothing of yours calls this deployment</p>}
			{keys.length > 0 && (
				<table>
					<tbody>
						{keys.map((k) => (
							<tr key={uuid(k.id)}>
								<td>{k.alias}</td>
								<td className="mono">{k.methods.join(', ')}</td>
								<td>
									<button
										disabled={!props.may('/roster.ApiKeyService/Erase')}
										onClick={() => {
											setNote(null)
											void revoke
												.call(ref(k.id))
												.then(() => setGone((was) => [...was, uuid(k.id)]))
												.catch((e: unknown) => setNote({ ok: false, text: said(e) }))
										}}
									>
										revoke
									</button>
								</td>
							</tr>
						))}
					</tbody>
				</table>
			)}
			{!props.may('/roster.ApiKeyService/Issue') && <Needs method="/roster.ApiKeyService/Issue" />}
			{props.may('/roster.ApiKeyService/Issue') && (
				<>
					<form
						className="app-password"
						onSubmit={(e) => {
							e.preventDefault()
							const f = new FormData(e.currentTarget)
							mint(e.currentTarget, String(f.get('app') ?? '').trim(), appPasswordMethods)
						}}
					>
						<input name="app" placeholder="the app's name: nas, jenkins, …" required />
						<button type="submit" disabled={issue.state === 'pending'}>
							mint an app password
						</button>
						<p className="hint">
							for a client with a password field and nothing else (LDAP, IMAP): a key that can only say who it is,
							revoked here when that app goes
						</p>
					</form>
					<form
						onSubmit={(e) => {
							e.preventDefault()
							const f = new FormData(e.currentTarget)
							mint(
								e.currentTarget,
								String(f.get('alias') ?? '').trim(),
								String(f.get('methods') ?? '')
									.split(/[\s,]+/)
									.map((s) => s.trim())
									.filter((s) => s !== ''),
							)
						}}
					>
						<input name="alias" placeholder="what to call it" required />
						<input name="methods" placeholder="/roster.MeService/Get, …" className="wide" required />
						<button type="submit" disabled={issue.state === 'pending'}>
							mint a key
						</button>
					</form>
				</>
			)}
			{token !== null && (
				<div className="secret">
					<p>
						The key, shown <strong>once</strong>. It acts as you, and never wider than you. Where a client asks for
						a password, this is what to paste.
					</p>
					<code>{token}</code>
				</div>
			)}
			{note !== null && <p className={note.ok ? 'note' : 'bad'}>{note.text}</p>}
		</section>
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
