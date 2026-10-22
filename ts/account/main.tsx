/**
 * The account app's page: sign in, and the account.
 *
 * # The same client as the admin console, one origin over
 *
 * Everything this page reads or writes about the person is a Connect call to
 * **this** origin -- `/roster.MeService/Get` and the rest -- which `roster
 * account serve` hands on to roster as the person (`frontdoor.Door.Proxy`). So
 * this is `ts/gen` and the same store the admin console uses, with the transport
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
 *
 * Three take no reference at all and need no role, because they are about
 * nobody but the caller: `Me.SignOutEverywhere`, `Me.Unlink`, and -- where the
 * operator offers it -- `Me.Leave`, deleting the account.
 */

import { Code, ConnectError } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { StrictMode, useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { Provider, useApp, useCall, useQuery } from '@lesomnus/payday/react'
import type { App } from '@lesomnus/payday/react'

import { ApiKeyService } from '#gen/app/apikey_svc_pb.js'
import { CredentialService } from '#gen/app/credential_svc_pb.js'
import { DelegationService } from '#gen/app/delegation_svc_pb.js'
import { EmailService } from '#gen/app/email_svc_pb.js'
import { MeService } from '#gen/app/me_pb.js'
import { HolderService } from '#gen/roster/payday/holder_svc_pb.js'
import { covers } from '#lib/covers.js'
import { expiries, expiresAt, until } from '#lib/expiry.js'
import { b64url, SignIn, vendor, type Vendor } from '#lib/signin.js'
import { Picture } from '#lib/picture.js'
import { caller } from '#lib/caller.js'
import { letGo, open, stillHere } from '#lib/store.js'
import '#lib/style.css'

const root = createRoot(document.getElementById('root') as HTMLElement)

/** Providers is what `/providers` says: who this front door is for, and how they arrive. */
interface Providers {
	tenant: { alias: string; name: string; labels: Record<string, string> }
	providers: { name: string; issuer: string }[]
	terminal?: boolean
	password: boolean
	/** Whether this operator lets its people delete their own account (`TenantConfig.leave`). */
	leave?: boolean
	/** Whether a recovery link can be had here: passwords offered, and mail to send it by. */
	recover?: boolean
	/** Whether a link that deletes an account can be had here: leaving offered, and mail to send it by. */
	leaveLink?: boolean
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

/** why is what a plain-text refusal from this app's own endpoints says. */
async function why(res: Response): Promise<string> {
	const text = (await res.text()).trim()

	return text === '' ? `that did not work (${res.status})` : text
}

const ref = (id: Uint8Array) => ({ key: { case: 'id' as const, value: id } })

/** brandOf is what to call the operator this front door is for. */
function brandOf(of: Providers): string {
	return of.tenant.name !== '' ? of.tenant.name : of.tenant.alias
}

/** Account is the signed-in page, section by section. */
function Account(props: { of: Providers; onSignOut: () => void; onLeft: (forgotten: string) => void }): React.ReactNode {
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
				<Password own={own} has={v.credentials.some((c) => c.kind === 'password')} offered={props.of.password} may={may} />
				<Factors own={own} alias={v.alias} brand={props.of.tenant.name || props.of.tenant.alias} may={may} />
				<Sessions own={own} may={may} />
				{props.of.terminal === true && <Terminal may={may} />}
				<Keys own={own} keys={v.keys} may={may} />
				{/* Not to somebody a directory provisions, whom roster refuses it: the directory says when they leave. */}
				{props.of.leave === true && v.provisionedThrough === '' && (
					<Leave
						of={props.of}
						alias={v.alias}
						identities={v.identities}
						password={v.credentials.some((c) => c.kind === 'password')}
						proved={v.emails.filter((e) => e.dateVerified !== undefined).map((e) => e.address)}
						onSignOut={props.onSignOut}
						onLeft={props.onLeft}
					/>
				)}
			</main>
		</div>
	)
}

/** Needs says which grant a section is waiting on, instead of drawing a dead button. */
/**
 * said is what `/callback` left in the URL after a *connect*, taken out of it.
 *
 * The account app is one screen with no router, so a word in the query is how a
 * round trip that ended at a directory says how it went. It is read once and
 * removed, because a reload should not repeat an answer about something that
 * happened before it.
 */
const landed = ((): { why: string; at: string } | null => {
	const q = new URLSearchParams(window.location.search)
	const why = q.get('link')
	if (why === null) {
		return null
	}

	window.history.replaceState(null, '', window.location.pathname)

	return { why, at: q.get('at') ?? '' }
})()

/**
 * waiting is the `user_code` a terminal's complete URL carried, if this browser
 * followed one -- so somebody who clicked the link does not retype what they were
 * about to type.
 */
const waiting = ((): string | null => {
	const v = new URLSearchParams(window.location.search).get('user_code')
	if (v === null) {
		return null
	}

	window.history.replaceState(null, '', window.location.pathname)

	return v.toUpperCase()
})()

/** how is what to say about it, in the section the button was in. */
function how(v: { why: string; at: string }): { ok: boolean; text: string } {
	switch (v.why) {
		case 'ok':
			return { ok: true, text: `${v.at} is a way in now` }
		case 'taken':
			return { ok: false, text: `that ${v.at} account is already a way in for somebody here` }
		case 'already':
			return { ok: false, text: `you already sign in with ${v.at}` }
		default:
			return { ok: false, text: `${v.at} could not be added` }
	}
}

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
			{allowed && row.state === 'ok' && row.data !== undefined && <Picture holder={row.data} may={props.may} />}
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

/**
 * Ways is how the person signs in: the password, and a row per directory this
 * operator offers.
 *
 * # A row per connection, not a button per provider
 *
 * This drew the identities the person had and then a button for **every**
 * connection the tenant has, which is one screen for two questions and gets the
 * common case wrong. An operator with Entra and GitHub registered, and somebody
 * who arrived through Entra, saw `add entra` beside `add github` -- and the first
 * of those is a round trip to Entra that ends in roster refusing it, because a
 * second identity at one provider is *a link that found the wrong row*
 * (`server/core/identity.go`). The only way to find that out was to go and come
 * back.
 *
 * So the list is the **connections**, each saying whether it is connected: the
 * one they used says who they are there and offers to unlink, and the one they
 * have not used offers to connect. Which is also the feature a tenant with two
 * directories wanted -- *I signed up with Entra and I would like GitHub too* is
 * one button, where it belongs, and the button that cannot work is not drawn.
 *
 * An identity at a provider the tenant no longer offers keeps its row, dim and
 * without a connect: a `Connection` is configuration and may be withdrawn, and
 * the way in it wrote is still a way in until somebody takes it back.
 */
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
	const [note, setNote] = useState(landed === null ? null : how(landed))

	const link = (name: string): void => {
		const f = document.createElement('form')
		f.method = 'POST'
		f.action = `/ways?connection=${encodeURIComponent(name)}`
		document.body.appendChild(f)
		f.submit()
	}

	const may = props.may('/roster.IdentityService/Add')
	const ids = props.identities.filter((i) => !gone.includes(uuid(i.id)))
	const passwords = props.credentials.filter((c) => c.kind === 'password')

	const at = (name: string): { id: Uint8Array; provider: string; subject: string } | undefined =>
		ids.find((i) => i.provider === name)

	// The connections, then whatever the person holds that is not one of them.
	const rows: { provider: string; offered: boolean }[] = [
		...props.of.providers.map((p) => ({ provider: p.name, offered: true })),
		...ids
			.filter((i) => !props.of.providers.some((p) => p.name === i.provider))
			.map((i) => ({ provider: i.provider, offered: false })),
	]

	const off = (i: { id: Uint8Array }): void => {
		setBad(null)
		void unlink
			.call({ id: i.id })
			.then(() => setGone((was) => [...was, uuid(i.id)]))
			.catch((e: unknown) => setBad(said(e)))
	}

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
					{rows.map((r) => {
						const has = at(r.provider)

						return (
							<tr key={r.provider}>
								<td className={r.offered ? undefined : 'dim'}>{r.provider}</td>
								<td className={has === undefined ? 'dim' : 'mono'}>
									{has === undefined ? 'not connected' : has.subject}
								</td>
								<td>
									{has !== undefined ? (
										/* `Me.Unlink` is waived: taking back a way in needs no
										   role. roster refuses the last one. */
										<button onClick={() => off(has)}>unlink</button>
									) : (
										r.offered && (
											<button disabled={!may} onClick={() => link(r.provider)}>
												connect
											</button>
										)
									)}
								</td>
							</tr>
						)
					})}
				</tbody>
			</table>
			{!may && rows.some((r) => r.offered && at(r.provider) === undefined) && (
				<Needs method="/roster.IdentityService/Add" />
			)}
			{note !== null && (
				<p className={note.ok ? 'note' : 'bad'} onClick={() => setNote(null)}>
					{note.text}
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
			<p className="note">
				where a link that recovers or deletes your account goes. An address counts once it is confirmed from your mailbox.
			</p>
			{items.length === 0 && <p className="none">none — a link has nowhere to go</p>}
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
												void fetch('/verify', json({ id: b64url(v.id) })).then(async (res) => {
													if (res.status === 202) {
														setNote({ ok: true, text: `a link is on its way to ${v.address}` })

														return
													}

													// The app's own words otherwise, which say when to ask
													// again after one too many (429) or a moment of too
													// many on their way (503).
													setNote({
														ok: false,
														text: res.status === 501 ? 'this deployment cannot send mail' : await why(res),
													})
												})
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
						.catch((e: unknown) => {
							// An address is one person's within a tenant, so the one
							// refusal a person can do something about is *somebody has
							// it* -- and what they can do is claim it, which mails a
							// link to that mailbox. A row nobody proved lets go; one
							// somebody proved does not. See docs/login.md § *Signing in
							// by address*.
							if (!(e instanceof ConnectError) || e.code !== Code.AlreadyExists) {
								setNote({ ok: false, text: said(e) })

								return
							}

							void fetch('/claim', json({ address })).then(async (res) => {
								if (res.status === 202) {
									form.reset()
									setNote({
										ok: true,
										text: `nobody has confirmed that address — a link is on its way to ${address}`,
									})

									return
								}

								// The app's own refusals are plain text and say what they
								// are, which is what `why` is for. The one worth reading is
								// a 409: somebody proved it, and there is nothing further to
								// try.
								setNote({ ok: false, text: await why(res) })
							})
						})
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

/**
 * Password is `Credential.Set` on your own row, which asks for the one you hold
 * -- and, of somebody who holds none, for a sign-in a few minutes old instead,
 * since there is no password to prove (`server/core/credential.go`, *a first
 * password of your own*). The form asked for a current password whatever they
 * held, which somebody who never had one could not give.
 *
 * Not drawn for somebody with none where the operator offers no passwords: a
 * first one there is a row no sign-in reads.
 */
function Password(props: { own: Uint8Array; has: boolean; offered: boolean; may: (m: string) => boolean }): React.ReactNode {
	const set = useCall(CredentialService.method.set)
	const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null)
	const allowed = props.may('/roster.CredentialService/Set')
	// The record says which ways in there are, and no write here answers with
	// it: told to ask again, the sections that read it -- the ways in, and what
	// deleting the account says -- read the password just set.
	const app = useApp()

	if (!props.has && !props.offered) return null

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
								setNote({ ok: true, text: props.has ? 'changed' : 'set; you can sign in with it' })
								if (!props.has) app.queries.forget(MeService.method.get)
							})
							.catch((e: unknown) => setNote({ ok: false, text: said(e) }))
					}}
				>
					{props.has && <input name="current" type="password" placeholder="current" autoComplete="current-password" required />}
					<input name="next" type="password" placeholder="new" autoComplete="new-password" required />
					<button type="submit" disabled={set.state === 'pending'}>
						{props.has ? 'change' : 'set'}
					</button>
				</form>
			)}
			{props.has ? (
				<p className="note">
					Your own row asks for the password you hold, so a credential that merely acts as you cannot
					replace it. Lost it? Sign out and use <em>forgot?</em>
				</p>
			) : (
				<p className="note">
					You have none: you sign in through a directory. Setting one takes a sign-in made a few minutes
					ago, at which you were asked who you are; where yours cannot be that, <em>forgot?</em> mails
					you a link that hands you one.
				</p>
			)}
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

/**
 * Terminal is a command line waiting to be signed in: RFC 8628's user code.
 *
 * It sits above the keys because that is what it makes -- one key, named after
 * the machine, with what that machine asked for and nothing more. The code is
 * typed here rather than guessed at by the page, and `?user_code=` fills it in
 * for anybody who followed the complete URL the terminal printed (the same thing
 * a QR code carries).
 *
 * What it shows before the button is what the terminal **asked to be allowed**,
 * because that is the whole of the decision: `account/device.go` refuses to mint
 * anything wider than the person approving it, and somebody who cannot see the
 * list cannot be said to have approved it.
 */
function Terminal(props: { may: (m: string) => boolean }): React.ReactNode {
	const [code, setCode] = useState(waiting ?? '')
	const [asked, setAsked] = useState<{ name: string; methods: string[] } | null>(null)
	const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null)

	const look = (v: string): void => {
		setNote(null)
		setAsked(null)
		void fetch(`/device/pending?user_code=${encodeURIComponent(v)}`)
			.then(async (res) =>
				res.ok
					? setAsked((await res.json()) as { name: string; methods: string[] })
					: setNote({ ok: false, text: await why(res) }),
			)
			.catch(() => setNote({ ok: false, text: 'that could not be looked up' }))
	}

	const answer = (deny: boolean): void => {
		setNote(null)
		void fetch('/device/approve', json({ user_code: code, deny }))
			.then(async (res) => {
				if (!res.ok) {
					setNote({ ok: false, text: await why(res) })

					return
				}
				setAsked(null)
				setCode('')
				setNote({ ok: true, text: deny ? 'refused, and the terminal has been told' : 'signed in; the terminal has its key' })
			})
			.catch(() => setNote({ ok: false, text: 'that could not be answered' }))
	}

	return (
		<section>
			<h2>a terminal</h2>
			<p className="note">
				Running <code>roster sign-in</code> somewhere prints a code. Type it here to give that
				machine a key of its own.
			</p>
			<p className="acts">
				<input
					name="user_code"
					value={code}
					placeholder="XXXX-XXXX"
					onChange={(e) => setCode(e.target.value.toUpperCase())}
				/>
				<button disabled={code.trim() === ''} onClick={() => look(code)}>
					look it up
				</button>
			</p>
			{asked !== null && (
				<>
					<table>
						<tbody>
							<tr>
								<td>the machine</td>
								<td className="mono">{asked.name}</td>
							</tr>
							<tr>
								<td>it may call</td>
								<td className="mono">{asked.methods.join(', ')}</td>
							</tr>
						</tbody>
					</table>
					<p className="acts">
						<button disabled={!props.may('/roster.ApiKeyService/Issue')} onClick={() => answer(false)}>
							sign it in
						</button>
						<button onClick={() => answer(true)}>no</button>
					</p>
					{!props.may('/roster.ApiKeyService/Issue') && <Needs method="/roster.ApiKeyService/Issue" />}
				</>
			)}
			{note !== null && <p className={note.ok ? 'note' : 'bad'}>{note.text}</p>}
		</section>
	)
}

function Keys(props: {
	own: Uint8Array
	keys: {
		id: Uint8Array
		alias: string
		methods: string[]
		dateExpires?: { seconds: bigint } | undefined
		dateUsed?: { seconds: bigint } | undefined
	}[]
	may: (m: string) => boolean
}): React.ReactNode {
	const issue = useCall(ApiKeyService.method.issue)
	const revoke = useCall(ApiKeyService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [made, setMade] = useState<{ id: Uint8Array; alias: string; methods: string[]; dateExpires?: { seconds: bigint } | undefined }[]>([])
	const [token, setToken] = useState<string | null>(null)
	const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null)

	// One list, both halves filtered: a key minted a moment ago and revoked
	// the moment after is gone too, which the browser found it was not.
	const keys = [...props.keys, ...made].filter((k) => !gone.includes(uuid(k.id)))

	const mint = (form: HTMLFormElement, alias: string, methods: string[], expires: string): void => {
		if (alias === '' || methods.length === 0) return
		setNote(null)
		setToken(null)
		void issue
			.call({ holder: ref(props.own), alias, methods, expires: expiresAt(expires) })
			.then((r) => {
				form.reset()
				setToken(r.token)
				if (r.key !== undefined) {
					setMade((was) => [
						...was,
						{ id: r.key!.id, alias: r.key!.alias, methods: r.key!.methods, dateExpires: r.key!.dateExpires },
					])
				}
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
								<td className={k.dateExpires === undefined ? 'none' : ''}>{until(k.dateExpires)}</td>
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
							mint(e.currentTarget, String(f.get('app') ?? '').trim(), appPasswordMethods, String(f.get('expires') ?? 'never'))
						}}
					>
						<input name="app" placeholder="the app's name: nas, jenkins, …" required />
						<Expires />
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
								String(f.get('expires') ?? 'never'),
							)
						}}
					>
						<input name="alias" placeholder="what to call it" required />
						<input name="methods" placeholder="/roster.MeService/Get, …" className="wide" required />
						<Expires />
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

/** Expires is the one choice both key forms share. */
function Expires(): React.ReactNode {
	return (
		<select name="expires" defaultValue="never">
			{expiries.map((v) => (
				<option key={v.value} value={v.value}>
					{v.name}
				</option>
			))}
		</select>
	)
}

/**
 * Leave is deleting your own account, where the operator offers it:
 * `Me.Leave`, which erases you the way an operator's erase does.
 *
 * It asks for two things roster checks again on its side: a sign-in a few
 * minutes old -- a page left open since the morning signs in again first, and
 * comes back here (`#leave`), and a directory is asked to authenticate them
 * again rather than remember them -- and your alias typed, so that nothing does
 * it by accident. What it says it does is what roster does: nothing is destroyed
 * at once, the operator can bring the account back until it is forgotten, and
 * then everything held about you is.
 *
 * Not every way in can be that sign-in, and a refusal says which of the
 * person's cannot and what is left. GitHub never says when it asked anybody;
 * Google says so only where its project lets it, and does not ask again when
 * asked; a password always can. Somebody with none is mailed a link that
 * deletes the account and does nothing else (`/leave/link`, `Vouch.LeaveLink`):
 * to an address of theirs that has been checked, where the deployment can
 * send mail, and clicking it asks for the alias typed before anything happens.
 * Where no link can be had, it is the operator who deletes the account.
 */
function Leave(props: {
	of: Providers
	alias: string
	identities: { provider: string }[]
	password: boolean
	/** The addresses on the account somebody has checked: where a link may go. */
	proved: string[]
	onSignOut: () => void
	onLeft: (forgotten: string) => void
}): React.ReactNode {
	const leave = useCall(MeService.method.leave)
	const [note, setNote] = useState<string | null>(null)
	const [stale, setStale] = useState(false)
	const brand = brandOf(props.of)

	// Which of the person's own ways in cannot be a sign-in a moment ago, by
	// whose sign-in each connection is -- so a refusal names the road that is
	// left rather than the one they just took. `arrives/google.go` is why
	// Google is one of them.
	const through = (who: Vendor): boolean =>
		props.identities.some((i) => vendor(props.of.providers.find((p) => p.name === i.provider)?.issuer) === who)
	const github = through('github')
	const google = through('google')
	// A password they can sign in with, which is one wherever passwords are.
	const password = props.password && props.of.password

	useEffect(() => {
		if (location.hash === '#leave') document.getElementById('leave')?.scrollIntoView()
	}, [])

	return (
		<section id="leave">
			<h2>delete your account</h2>
			<p className="note">
				You will not be able to sign in to {brand} again, nor to anything that signs you in through
				it, and every app that holds a session for you is told. Nothing is destroyed at once: until the
				account is forgotten, whoever runs {brand} can bring it back if you ask them. After that,
				everything held about you is destroyed.
			</p>
			<form
				onSubmit={(e) => {
					e.preventDefault()
					const f = new FormData(e.currentTarget)
					setNote(null)
					setStale(false)
					void leave
						.call({ confirm: String(f.get('confirm') ?? '') })
						.then((v) => props.onLeft(when(v.dateForgotten)))
						.catch((e: unknown) => {
							if (e instanceof ConnectError && e.code === Code.FailedPrecondition) {
								setStale(true)
								return
							}
							setNote(said(e))
						})
				}}
			>
				<input name="confirm" placeholder={`type ${props.alias} to confirm`} autoComplete="off" required />
				<button type="submit" disabled={leave.state === 'pending'}>
					delete my account
				</button>
			</form>
			{stale && (
				<>
					<p className="bad">
						Deleting an account takes a sign-in made a few minutes ago, at which you were asked who you are.{' '}
						<button
							onClick={() => {
								location.hash = 'leave'
								props.onSignOut()
							}}
						>
							sign in again
						</button>
					</p>
					{github && <p className="note">A sign-in through GitHub never is one: GitHub does not say when it asked you.</p>}
					{google && (
						<p className="note">
							One through Google is, if whoever runs {brand} has let Google say when it asked you and
							Google just did. It does not ask again when asked, so sign in through it from a private
							window, where it has to.
						</p>
					)}
					{password && <p className="note">Your password always is one.</p>}
					{!password && props.of.leaveLink === true && props.proved.length > 0 && (
						<ByMail alias={props.alias} proved={props.proved} />
					)}
					{!password && props.of.leaveLink === true && props.proved.length === 0 && (
						<p className="note">
							A link mailed to you can delete it instead, to an address on your account that has been checked:
							confirm one under <em>addresses</em> first.
						</p>
					)}
					{!password && props.of.leaveLink !== true && (
						<p className="note">If none of your ways in can be, whoever runs {brand} can delete your account.</p>
					)}
				</>
			)}
			{note !== null && <p className="bad">{note}</p>}
		</section>
	)
}

/**
 * ByMail asks for a link that deletes the account, mailed to an address of
 * theirs somebody checked: the road for somebody whose way in cannot be the
 * sign-in deleting asks for. The link signs nobody in -- clicking it asks for
 * the alias typed, and only that deletes anything -- so a page somebody walked
 * away from sends a link only its owner's mailbox can use.
 */
function ByMail(props: { alias: string; proved: string[] }): React.ReactNode {
	const [to, setTo] = useState(props.proved[0] ?? '')
	const [sent, setSent] = useState<{ address: string; expires: string } | null>(null)
	const [bad, setBad] = useState<string | null>(null)
	const [busy, setBusy] = useState(false)

	return (
		<>
			<p className="note">
				Or have a link mailed to you instead. It deletes your account once you type <strong>{props.alias}</strong>{' '}
				on the page it opens, and it signs nobody in.
			</p>
			<form
				onSubmit={(e) => {
					e.preventDefault()
					setBad(null)
					setBusy(true)
					void fetch('/leave/link', json({ address: to }))
						.then(async (res) => {
							if (res.status === 202) {
								setSent((await res.json()) as { address: string; expires: string })
								return
							}
							setBad(await why(res))
						})
						.catch(() => setBad('the link could not be asked for just now'))
						.finally(() => setBusy(false))
				}}
			>
				{props.proved.length > 1 && (
					<select value={to} onChange={(e) => setTo(e.currentTarget.value)} aria-label="the address to send it to">
						{props.proved.map((a) => (
							<option key={a} value={a}>
								{a}
							</option>
						))}
					</select>
				)}
				<button type="submit" disabled={busy}>
					mail a link to {props.proved.length > 1 ? 'this address' : to}
				</button>
			</form>
			{sent !== null && (
				<p className="note">
					A link is on its way to {sent.address}. Open it before{' '}
					{new Date(sent.expires).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}; after that it is
					nothing, and you can ask for another.
				</p>
			)}
			{bad !== null && <p className="bad">{bad}</p>}
		</>
	)
}

/** Left is what is left to say once the account is gone. */
function Left(props: { brand: string; forgotten: string; said?: string | undefined }): React.ReactNode {
	return (
		<main className="left">
			<h1>your account is deleted</h1>
			{props.said !== undefined && (
				<p className="bad" role="alert">
					{props.said}
				</p>
			)}
			<p>
				You are signed out of {props.brand} everywhere.{' '}
				{props.forgotten !== ''
					? `Until ${props.forgotten} whoever runs ${props.brand} can bring it back if you ask; after that, everything held about you is destroyed.`
					: `Whoever runs ${props.brand} can bring it back if you ask them, until they forget it.`}
			</p>
		</main>
	)
}

/**
 * which is the session this browser holds at this app, by the name the page's
 * store goes by for it -- `GET /session`, `frontdoor.Door.Which` -- or null
 * for none.
 */
async function which(): Promise<string | null> {
	const res = await fetch('/session', { credentials: 'include' })
	if (res.status === 401) return null
	if (!res.ok) throw new Error(`cannot tell who is signed in (${res.status})`)

	return ((await res.json()) as { identity: string }).identity
}

/**
 * Whose store the page draws, and when it lets go of it (`caller.ts`): every
 * call through the proxy says the store's name, and the app hands on only a
 * call whose name is the browser's session's (`frontdoor.Door.Proxy`).
 */
const calls = caller('account', which)

const transport = createConnectTransport({
	baseUrl: location.origin,
	fetch: (input, init) => fetch(input, { ...init, credentials: 'include' }),
	interceptors: [calls.interceptor],
})

/**
 * boot draws the page for whoever the browser's session is: the form for
 * nobody, and otherwise their store, opened and filled from its mirror.
 *
 * Asked again on a sign-in, on a sign-out in any tab, and when a call is
 * refused for a session that is not this store's -- which is how a page left
 * holding one person's store learns somebody else signed in, in another tab,
 * and lets go of it rather than drawing that person's rows into it.
 *
 * `said` is what the form says first, when there is one to draw: that the
 * sign-out before it could not clear this browser (`stillHere`).
 */
async function boot(said?: string): Promise<void> {
	const of = await providers()
	if (of === null) {
		root.render(
			<StrictMode>
				<main className="error">no operator here serves this name</main>
			</StrictMode>,
		)
		return
	}

	let identity: string | null
	try {
		identity = await which()
	} catch (err) {
		console.error('cannot tell who is signed in:', err)
		identity = null
	}
	if (identity === null) {
		calls.drop()
		// Signing in again to delete the account (`#leave`) asks the directory
		// to ask again rather than remember them: what roster reads is when the
		// directory last authenticated them, and a sign-in it remembered is not
		// a moment ago.
		const again = location.hash === '#leave' ? '&again=1' : ''
		root.render(
			<StrictMode>
				<SignIn
					brand={brandOf(of)}
					password={of.password}
					providers={of.providers}
					providerHref={(n: string) => `/login?connection=${encodeURIComponent(n)}${again}`}
					recover="/recover"
					onDone={() => void boot()}
					{...(said !== undefined ? { said } : {})}
				/>
			</StrictMode>,
		)
		return
	}

	const app = await open(transport, { name: 'roster', identity })

	// What the page does once it has let go of the store: ask again whom to
	// draw for, unless the account is gone and there is something to say --
	// and say too when the forget did not reach this browser's copy, rather
	// than leave the person to believe nothing of theirs is here.
	let then = (said?: string): void => void boot(said)
	const held = calls.hold(identity, (forget) => {
		void letGo(app, forget).then(
			() => then(),
			() => then(stillHere),
		)
	})

	// Signing out ends the session -- the delegation it held is revoked at
	// roster -- and every other tab is told, and forgets what it held for this
	// person, as this one does.
	const out = (): void => {
		void fetch('/session', { method: 'DELETE' }).finally(() => held.out())
	}

	// Gone, and the session with it: what this page held for the person is
	// forgotten here, and in every tab, as it is at roster.
	const gone = (forgotten: string): void => {
		void fetch('/session', { method: 'DELETE' }).finally(() => {
			then = (said) =>
				root.render(
					<StrictMode>
						<Left brand={brandOf(of)} forgotten={forgotten} said={said} />
					</StrictMode>,
				)
			held.out()
		})
	}

	root.render(
		<StrictMode>
			<Provider app={app}>
				<Account of={of} onSignOut={out} onLeft={gone} />
			</Provider>
		</StrictMode>,
	)
}

root.render(
	<StrictMode>
		<main className="loading">…</main>
	</StrictMode>,
)
void boot()
