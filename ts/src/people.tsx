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
 * ways, and makes a console the thing that reaches it. PLAN.md's list, items 3
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

import { useQuery } from '@lesomnus/payday/react'

import { create } from '@bufbuild/protobuf'

import type { ApiKey } from '../gen/app/apikey_pb.js'
import { SignInKeySchema } from '../gen/app/me_pb.js'
import type { SignInCredential, SignInIdentity, SignInKey } from '../gen/app/me_pb.js'
import type { Holder } from '../gen/roster/payday/holder_pb.js'
import { HolderService } from '../gen/roster/payday/holder_svc_pb.js'
import type { Admin } from './client.js'

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

			<Ways ids={ids} creds={creds} />

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
					disabled={!props.may('/roster.VouchService/Unlock')}
					onClick={() =>
						run(props.admin.vouch.unlock({ who: { id: key } }), 'the account is open')
					}
				>
					unlock
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
								<td>
									{v.dateUsed === undefined ? (
										<span className="none">never</span>
									) : (
										when(v.dateUsed)
									)}
								</td>
								<td>
									<button
										disabled={!props.may('/roster.HolderService/RevokeKey')}
										onClick={() => {
											void props.admin.holder
												.revokeKey({ ref: who, id: v.id })
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
				<button
					disabled={
						!props.may('/roster.IssueService/IssueKey') ||
						alias === '' ||
						methods.trim() === ''
					}
					onClick={() => {
						void props.admin.issue
							.issueKey({
								holder: who,
								alias,
								methods: methods
									.split(',')
									.map((v) => v.trim())
									.filter((v) => v !== ''),
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

function Ways(props: { ids: SignInIdentity[]; creds: SignInCredential[] }): React.ReactNode {
	if (props.ids.length === 0 && props.creds.length === 0) {
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
				{props.ids.map((v) => (
					<tr key={`i:${uuid(v.id)}`}>
						<td>{v.provider}</td>
						{/* The subject as the provider gave it, and it is not a
						    name: somebody reading this is checking *which
						    account*, and the answer has to be the one the
						    provider would answer with. */}
						<td className="mono">{v.subject}</td>
						<td>{when(v.dateCreated)}</td>
						<td />
					</tr>
				))}
			</tbody>
		</table>
	)
}

function Failed(props: { at: unknown }): React.ReactNode {
	return <p className="bad">{props.at instanceof Error ? props.at.message : 'no'}</p>
}
