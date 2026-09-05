/**
 * One person, as the operator of a deployment sees them.
 *
 * # Why this is a screen and not a row in a table
 *
 * The customers screen lists tenants and the people in each, which is a read.
 * This is the other half: what somebody signs in with, and the four things an
 * operator does when they cannot sign in.
 *
 * All four exist because of one deployment shape. In an air gap there is no
 * mail, so the somebody else who delivers a recovery code is a **person** —
 * which makes recovery and an operator-initiated reset the same act reached two
 * ways, and makes a console the thing that reaches it. Roadmap.md's items 3
 * and 10, and D28 is the shape.
 *
 * # What it shows that nothing else could
 *
 * `HolderService.SignsIn`, which exists for this screen. `CredentialService` is
 * not registered anywhere, because its generated `Get` answers with the
 * verifier; and `IdentityService` narrows by the **tenant**, so a page that
 * listed one person's identities by reading and sifting would be reading every
 * customer's to draw one. One RPC, narrowed to one person by the server.
 *
 * # And what it deliberately does not offer
 *
 * A field to type a password into. `Vouch.Reset` generates one and answers with
 * it once, which is `IssueService`'s argument about a key unchanged: a secret
 * the caller chose is a secret the caller knows, and one generated in a browser
 * is only as good as that page's `crypto`.
 *
 * @module
 */

import { useState } from 'react'

import { useCall, useQuery } from '@lesomnus/payday/react'

import { create } from '@bufbuild/protobuf'

import type { ApiKey } from '../gen/app/apikey_pb.js'
import { SignInKeySchema } from '../gen/app/me_pb.js'
import type { SignInCredential, SignInIdentity, SignInKey } from '../gen/app/me_pb.js'
import type { Holder } from '../gen/roster/payday/holder_pb.js'
import { HolderService } from '../gen/roster/payday/holder_svc_pb.js'
import { EmailService } from '../gen/app/email_svc_pb.js'
import { IdentityService } from '../gen/app/identity_svc_pb.js'
import type { Admin } from '../lib/client.js'
import { expiries, expiresAt, until } from '../lib/expiry.js'

/** uuid is the bytes an identifier arrives as, written the way a person reads one. */
function uuid(v: Uint8Array | undefined): string {
	if (v === undefined || v.length !== 16) return ''

	const h = [...v].map((b) => b.toString(16).padStart(2, '0')).join('')

	return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

/**
 * keyOf is the row `IssueService` answers beside a token, as the table shows
 * one.
 *
 * Two shapes for one thing, and this is the seam between them: `ApiKey` is the
 * entity and `SignInKey` is what a narrow read answers with, because
 * `ApiKeyService` is unregistered everywhere. Converting here rather than
 * asking again is what keeps a freshly minted key on the screen that minted it.
 */
function keyOf(v: ApiKey): SignInKey {
	return create(SignInKeySchema, {
		id: v.id,
		alias: v.alias,
		methods: v.methods,
		dateExpires: v.dateExpires,
		dateUsed: v.dateUsed,
	})
}

function when(v: { seconds: bigint } | undefined): string {
	if (v === undefined) return ''

	return new Date(Number(v.seconds) * 1000).toISOString().replace('T', ' ').slice(0, 16)
}

/**
 * Person is one person's ways in, and what an operator may do about them.
 *
 * `may` is the control plane's answer about the operator, passed down rather
 * than asked again: it decides what is worth **drawing** and never what is
 * allowed. The server refuses either way, and a page that treated this as the
 * decision would be one an altered client could talk out of.
 */
export function Person(props: {
	holder: Holder | undefined
	admin: Admin
	may: (method: string) => boolean

	// Said when this person was erased here, so the list above can stop
	// drawing them: a soft erase leaves the row and hides it from every read,
	// and a table still showing them would read as an erase that did not take.
	onErased?: () => void
}): React.ReactNode {
	const id = props.holder?.id
	const vs = useQuery(HolderService.method.signsIn, {
		ref: id === undefined ? undefined : { key: { case: 'id', value: id } },
	})

	// What the last act answered with. A password is here for exactly as long
	// as the operator is looking at it: it is not stored, not re-readable, and
	// gone the moment they pick somebody else.
	const [said, say] = useState<{ kind: 'secret' | 'done' | 'bad'; text: string } | null>(null)

	if (props.holder === undefined) return null
	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	const ids = vs.data?.identities ?? []
	const creds = vs.data?.credentials ?? []

	const key = id ?? new Uint8Array()
	const who = { key: { case: 'id' as const, value: key } }
	const run = (p: Promise<unknown>, ok: string): void => {
		say(null)
		void p
			.then(() => say({ kind: 'done', text: ok }))
			.catch((e: unknown) => say({ kind: 'bad', text: e instanceof Error ? e.message : 'no' }))
	}

	const disabled = props.holder.dateDisabled !== undefined

	return (
		<section className="within person">
			<h4>{props.holder.alias}</h4>

			<Ways ids={ids} creds={creds} may={props.may} />

			<Emails holder={key} may={props.may} />

			<Reaches holder={key} may={props.may} />

			<Profile holder={props.holder} may={props.may} />

			<Keys
				keys={vs.data?.keys ?? []}
				holder={key}
				admin={props.admin}
				may={props.may}
				say={say}
			/>

			<div className="state">
				{disabled ? (
					<p className="bad">
						suspended since {when(props.holder.dateDisabled)} — they cannot sign
						in, and every credential they already held stopped working
					</p>
				) : (
					<p className="note">signing in is open</p>
				)}
				{props.holder.dateInvalidated !== undefined && (
					<p className="note">
						everything issued before {when(props.holder.dateInvalidated)} is void
					</p>
				)}
			</div>

			<div className="acts">
				{/* Suspending and reinstating are two grants on purpose: a role
				    is a list of methods, so a deployment can only hand out what
				    it can name. */}
				{disabled ? (
					<button
						disabled={!props.may('/roster.HolderService/Enable')}
						onClick={() => run(props.admin.holder.enable({ ref: who }), 'signing in is open again')}
					>
						reinstate
					</button>
				) : (
					<button
						disabled={!props.may('/roster.HolderService/Disable')}
						onClick={() => run(props.admin.holder.disable({ ref: who }), 'suspended')}
					>
						suspend
					</button>
				)}

				{/* One write, and there is no undo by construction: the server
				    stamps the moment and nothing can write an older one. */}
				<button
					disabled={!props.may('/roster.HolderService/Invalidate')}
					onClick={() =>
						run(
							props.admin.holder.invalidate({ ref: who }),
							'everything issued before now is void',
						)
					}
				>
					sign out everywhere
				</button>

				{/* Generated here and answered with once. There is no field to
				    type one into, and that is the point. */}
				<button
					disabled={!props.may('/roster.VouchService/Reset')}
					onClick={() => {
						say(null)
						void props.admin.vouch
							.reset({ who: { id: key } })
							.then((r) => say({ kind: 'secret', text: r.secret }))
							.catch((e: unknown) =>
								say({ kind: 'bad', text: e instanceof Error ? e.message : 'no' }),
							)
					}}
				>
					new password
				</button>

				{/* A lockout releases itself after fifteen minutes, so this is a
				    convenience — and it is also the answer to what locking by
				    name costs: an account can be held closed by somebody else,
				    and a person on site can simply open it. */}
				<button
					disabled={!props.may('/roster.CredentialService/Unlock')}
					onClick={() =>
						run(props.admin.credential.unlock({ ref: who }), 'the account is open')
					}
				>
					unlock
				</button>

				{/* A second factor made **for** somebody: the operator's half of
				    `Credential.Enrol`, for a hardware key issued in an air gap or
				    a phone set up at a desk. The seed is answered once, like a
				    password, and the factor does not count until one code proves
				    it. Held to `mayReach`, like every credential write. */}
				<EnrolFor holder={key} admin={props.admin} may={props.may} say={say} />

				{/* Soft: the row stays for the trail and vanishes from every
				    read. There is no undo drawn, because there is no undo. */}
				<button
					className="danger"
					disabled={!props.may('/roster.HolderService/Erase')}
					onClick={() => {
						if (!window.confirm(`erase ${props.holder?.alias ?? 'them'}? they vanish from every read; the trail keeps what they did`)) return
						say(null)
						void props.admin.holder
							.erase(who)
							.then(() => props.onErased?.())
							.catch((e: unknown) => say({ kind: 'bad', text: e instanceof Error ? e.message : 'no' }))
					}}
				>
					erase
				</button>
			</div>

			{said?.kind === 'secret' && (
				<div className="secret">
					<p>
						Read this out. It is shown <strong>once</strong> — what is stored is
						an argon2id hash, so this deployment cannot tell anybody what theirs
						was any more than it can tell them their key.
					</p>
					<code>{said.text}</code>
					<p className="note">
						Everything they had signed in with stopped working: a reset that
						left the old sessions alive would not be a reset.
					</p>
				</div>
			)}
			{said?.kind === 'done' && <p className="note">{said.text}</p>}
			{said?.kind === 'bad' && <p className="bad">{said.text}</p>}
		</section>
	)
}

/**
 * Reaches is what this person may call, as the gate decides it.
 *
 * `Holder.Reaches` answers the same union `MeService.Get` answers about the
 * caller -- bindings, groups, teams, added up by the policy's own function --
 * as patterns. Drawn beside the ways in because an operator asking "who is
 * this" wants both halves: how they get in, and what they may do once in.
 */
function Reaches(props: { holder: Uint8Array; may: (method: string) => boolean }): React.ReactNode {
	const allowed = props.may('/roster.HolderService/Reaches')
	const vs = useQuery(HolderService.method.reaches, {
		ref: allowed ? { key: { case: 'id', value: props.holder } } : undefined,
	})

	if (!allowed) {
		return (
			<section className="reaches">
				<h5>may call</h5>
				<p className="none">this needs /roster.HolderService/Reaches</p>
			</section>
		)
	}
	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	const ms = vs.data?.methods ?? []

	return (
		<section className="reaches">
			<h5>may call</h5>
			{ms.length === 0 ? (
				<p className="none">nothing — no role reaches them, by any path</p>
			) : (
				<ul className="methods">
					{ms.map((m) => (
						<li key={m}>
							<code>{m}</code>
						</li>
					))}
				</ul>
			)}
			<p className="note">
				{vs.data?.everySite === true
					? 'across the whole tenant'
					: (vs.data?.sites.length ?? 0) > 0
						? `in ${vs.data?.sites.length} site(s)`
						: 'nowhere in particular'}
				{' — '}patterns, as the roles wrote them, added up over bindings, groups
				and teams the way the gate adds them up.
			</p>
		</section>
	)
}

/**
 * Ways is the two lists, and the sentence that has to go with an empty one.
 *
 * Somebody with neither is somebody with no way in at all, which is a state the
 * schema permits — a `Holder` written by an operator before anything was
 * attached — and is not the same as a screen that failed to load.
 */
/**
 * Keys is what a machine of theirs holds, and the one act on it.
 *
 * # Why it is here and not on the customers screen
 *
 * A key **is** a way in: it resolves to its holder, so a call made with it is
 * made as them. That is why it sits beside the passwords and the providers
 * rather than in a table of its own -- and why revoking one is on the same
 * screen as suspending somebody, which is the other way to stop a credential
 * working.
 *
 * # What is not here
 *
 * The secret, and there is nowhere it could come from: what is stored is a
 * hash. A key is readable exactly once, at the moment it is minted, and this
 * page shows it then and never again.
 */
function Keys(props: {
	keys: SignInKey[]
	holder: Uint8Array
	admin: Admin
	may: (method: string) => boolean
	say: (v: { kind: 'secret' | 'done' | 'bad'; text: string }) => void
}): React.ReactNode {
	const [alias, setAlias] = useState('')
	const [methods, setMethods] = useState('')
	const [expires, setExpires] = useState<string>('never')

	// What this screen has done since it read, which is the ordinary shape for
	// a page that changes a list it is showing. `useQuery` has no refetch and
	// `SignsIn` is not a `List`, so nothing revalidates it -- and a table that
	// still showed a key somebody just revoked would be worse than one that is
	// a moment behind the server.
	const [gone, setGone] = useState<string[]>([])
	const [made, setMade] = useState<SignInKey[]>([])

	const keys = [...props.keys.filter((v) => !gone.includes(uuid(v.id))), ...made]

	const who = { key: { case: 'id' as const, value: props.holder } }
	const bad = (e: unknown): void =>
		props.say({ kind: 'bad', text: e instanceof Error ? e.message : 'no' })

	return (
		<section className="keys">
			<h5>keys</h5>

			{keys.length === 0 ? (
				<p className="none">none — nothing of theirs calls this deployment</p>
			) : (
				<table>
					<thead>
						<tr>
							<th>name</th>
							<th>may call</th>
							<th>until</th>
							<th>last used</th>
							<th />
						</tr>
					</thead>
					<tbody>
						{keys.map((v) => (
							<tr key={uuid(v.id)}>
								<td>{v.alias}</td>
								{/* What it may call, in full. A key is never wider
								    than the person it hangs off, so this is how
								    much narrower it was made -- which is the only
								    thing worth reading before revoking one. */}
								<td className="mono">{v.methods.join(', ')}</td>
								<td className={v.dateExpires === undefined ? 'none' : ''}>{until(v.dateExpires)}</td>
								<td>
									{v.dateUsed === undefined ? (
										<span className="none">never</span>
									) : (
										when(v.dateUsed)
									)}
								</td>
								<td>
									<button
										disabled={!props.may('/roster.ApiKeyService/Erase')}
										onClick={() => {
											void props.admin.apiKey
												.erase({ key: { case: 'id', value: v.id } })
												.then(() => {
													props.say({ kind: 'done', text: 'revoked' })
													setGone((was) => [...was, uuid(v.id)])
												})
												.catch(bad)
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

			<div className="acts">
				<input
					placeholder="what to call it"
					value={alias}
					onChange={(e) => setAlias(e.target.value)}
				/>
				{/* Written out and never defaulted, in either direction.
				    Everything hands out more than anybody asked for; nothing
				    mints a key that silently does not work -- which is the
				    refusal `IssueService` makes, said here so somebody meets it
				    before they have typed a name. */}
				<input
					placeholder="/roster.HolderService/List, …"
					value={methods}
					onChange={(e) => setMethods(e.target.value)}
				/>
				<select value={expires} onChange={(e) => setExpires(e.target.value)}>
					{expiries.map((v) => (
						<option key={v.value} value={v.value}>
							{v.name}
						</option>
					))}
				</select>
				<button
					disabled={
						!props.may('/roster.ApiKeyService/Issue') ||
						alias === '' ||
						methods.trim() === ''
					}
					onClick={() => {
						void props.admin.apiKey
							.issue({
								holder: who,
								alias,
								methods: methods
									.split(',')
									.map((v) => v.trim())
									.filter((v) => v !== ''),
								expires: expiresAt(expires),
							})
							.then((r) => {
								props.say({ kind: 'secret', text: r.token })
								setAlias('')
								setMethods('')
								const row = r.key
								if (row !== undefined) setMade((was) => [...was, keyOf(row)])
							})
							.catch(bad)
					}}
				>
					mint a key
				</button>
			</div>
		</section>
	)
}

function Ways(props: {
	ids: SignInIdentity[]
	creds: SignInCredential[]
	may: (method: string) => boolean
}): React.ReactNode {
	// Unlinking is `Identity.Erase`, the operator's side of what a person does
	// to themselves through `MeService.Unlink`. Taking a way in away is not
	// adding one, so no escalation rule stands in front of it -- only the wall,
	// and the role naming it.
	const unlink = useCall(IdentityService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [bad, setBad] = useState<string | null>(null)

	const ids = props.ids.filter((v) => !gone.includes(uuid(v.id)))

	if (ids.length === 0 && props.creds.length === 0) {
		return <p className="none">no way in at all — nothing to sign in with</p>
	}

	return (
		<table>
			<thead>
				<tr>
					<th>way in</th>
					<th>which</th>
					<th>since</th>
					<th />
				</tr>
			</thead>
			<tbody>
				{props.creds.map((v) => (
					<tr key={`c:${v.kind}:${v.name}`}>
						<td>{v.kind}</td>
						<td>{v.name === '' ? <span className="none">the only one</span> : v.name}</td>
						<td>{v.dateRotated === undefined ? 'never changed' : when(v.dateRotated)}</td>
						<td>
							{v.dateLocked !== undefined && (
								<span className="bad">closed until {when(v.dateLocked)}</span>
							)}
						</td>
					</tr>
				))}
				{ids.map((v) => (
					<tr key={`i:${uuid(v.id)}`}>
						<td>{v.provider}</td>
						{/* The subject as the provider gave it, and it is not a
						    name: somebody reading this is checking *which
						    account*, and the answer has to be the one the
						    provider would answer with. */}
						<td className="mono">{v.subject}</td>
						<td>{when(v.dateCreated)}</td>
						<td>
							<button
								disabled={!props.may('/roster.IdentityService/Erase')}
								onClick={() => {
									setBad(null)
									void unlink
										.call({ key: { case: 'id', value: v.id } })
										.then(() => setGone((was) => [...was, uuid(v.id)]))
										.catch((e: unknown) => setBad(e instanceof Error ? e.message : 'no'))
								}}
							>
								unlink
							</button>
						</td>
					</tr>
				))}
			</tbody>
			{bad !== null && (
				<tfoot>
					<tr>
						<td colSpan={4} className="bad">
							{bad}
						</td>
					</tr>
				</tfoot>
			)}
		</table>
	)
}

/**
 * Emails is the addresses roster holds for somebody, and whether anybody
 * checked each.
 *
 * `date_verified` is drawn and never written from here: no request may assert
 * it (`server/core` refuses an `Add` that tries), because an address is where a
 * recovery link goes and "verified" is what a link that came back proves. An
 * operator adding one is adding a **way in** -- the mailbox a reset goes to --
 * so `Email.Add` runs `mayWriteAWayIn` and refuses it for somebody wider than
 * the operator.
 */
function Emails(props: { holder: Uint8Array; may: (method: string) => boolean }): React.ReactNode {
	const who = { key: { case: 'id' as const, value: props.holder } }
	const vs = useQuery(EmailService.method.list, { filters: [{ holder: who }] })
	const add = useCall(EmailService.method.add)
	const erase = useCall(EmailService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [bad, setBad] = useState<string | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <Failed at={vs.error} />

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section className="emails">
			<h5>addresses</h5>
			{items.length === 0 ? (
				<p className="none">none — a recovery link has nowhere to go</p>
			) : (
				<table>
					<tbody>
						{items.map((v) => (
							<tr key={uuid(v.id)}>
								<td className="mono">{v.address}</td>
								<td>
									{v.dateVerified === undefined ? (
										<span className="none">never checked</span>
									) : (
										`checked ${when(v.dateVerified)}`
									)}
								</td>
								<td>
									<button
										disabled={!props.may('/roster.EmailService/Erase')}
										onClick={() => {
											setBad(null)
											void erase
												.call({ key: { case: 'id', value: v.id } })
												.then(() => setGone((was) => [...was, uuid(v.id)]))
												.catch((e: unknown) => setBad(e instanceof Error ? e.message : 'no'))
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

					setBad(null)
					void add
						.call({ holder: who, address })
						.then(() => form.reset())
						.catch((e: unknown) => setBad(e instanceof Error ? e.message : 'no'))
				}}
			>
				<input name="address" type="email" placeholder="somebody@contoso.com" required />
				<button type="submit" disabled={add.state === 'pending' || !props.may('/roster.EmailService/Add')}>
					add address
				</button>
			</form>
			{bad !== null && <p className="bad">{bad}</p>}
		</section>
	)
}

/**
 * Profile is what a holder carries about themselves, replaced whole.
 *
 * `Holder.Update` is the narrow write: the profile and the app's own data, and
 * nothing that moves somebody between tenants, renames them into another alias,
 * or changes what they may do. It takes the version this page read, so a form
 * left open while somebody else edited is refused rather than applied to
 * whatever the row became.
 */
function Profile(props: { holder: Holder; may: (method: string) => boolean }): React.ReactNode {
	const update = useCall(HolderService.method.update)
	const [said, say] = useState<{ kind: 'done' | 'bad'; text: string } | null>(null)
	const p = props.holder.profile

	return (
		<section className="profile">
			<h5>profile</h5>
			<form
				className="profile"
				onSubmit={(e) => {
					e.preventDefault()
					const f = new FormData(e.currentTarget)
					const s = (k: string) => String(f.get(k) ?? '').trim()

					say(null)
					void update
						.call({
							ref: { key: { case: 'id', value: props.holder.id } },
							dateUpdated: props.holder.dateUpdated,
							profile: {
								displayName: s('display_name'),
								picture: s('picture'),
								department: s('department'),
								employeeNo: s('employee_no'),
								locale: s('locale'),
							},
						})
						.then(() => say({ kind: 'done', text: 'saved' }))
						.catch((e: unknown) => say({ kind: 'bad', text: e instanceof Error ? e.message : 'no' }))
				}}
			>
				<input name="display_name" placeholder="display name" defaultValue={p?.displayName ?? ''} />
				<input name="department" placeholder="department" defaultValue={p?.department ?? ''} />
				<input name="employee_no" placeholder="employee no" defaultValue={p?.employeeNo ?? ''} />
				<input name="locale" placeholder="locale, e.g. ko-KR" defaultValue={p?.locale ?? ''} />
				<input name="picture" placeholder="picture url" defaultValue={p?.picture ?? ''} />
				<button type="submit" disabled={update.state === 'pending' || !props.may('/roster.HolderService/Update')}>
					save profile
				</button>
			</form>
			{said?.kind === 'done' && <p className="note">{said.text}</p>}
			{said?.kind === 'bad' && <p className="bad">{said.text}</p>}
		</section>
	)
}

/**
 * EnrolFor is the operator's half of a second factor: made for somebody, and
 * answered with once.
 */
function EnrolFor(props: {
	holder: Uint8Array
	admin: Admin
	may: (method: string) => boolean
	say: (v: { kind: 'secret' | 'done' | 'bad'; text: string } | null) => void
}): React.ReactNode {
	const [name, setName] = useState('')

	return (
		<span className="enrol">
			<input placeholder="factor name, e.g. phone" value={name} onChange={(e) => setName(e.target.value)} />
			<button
				disabled={!props.may('/roster.CredentialService/Enrol')}
				onClick={() => {
					props.say(null)
					void props.admin.credential
						.enrol({ ref: { key: { case: 'id', value: props.holder } }, kind: 'totp', name })
						.then((r) => {
							setName('')
							props.say({ kind: 'secret', text: r.uri })
						})
						.catch((e: unknown) => props.say({ kind: 'bad', text: e instanceof Error ? e.message : 'no' }))
				}}
			>
				enrol a factor
			</button>
		</span>
	)
}

function Failed(props: { at: unknown }): React.ReactNode {
	return <p className="bad">{props.at instanceof Error ? props.at.message : 'no'}</p>
}
