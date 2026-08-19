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

import type { SignInCredential, SignInIdentity } from '../gen/app/me_pb.js'
import type { Holder } from '../gen/roster/payday/holder_pb.js'
import { HolderService } from '../gen/roster/payday/holder_svc_pb.js'
import type { Admin } from './client.js'

/** uuid is the bytes an identifier arrives as, written the way a person reads one. */
function uuid(v: Uint8Array | undefined): string {
	if (v === undefined || v.length !== 16) return ''

	const h = [...v].map((b) => b.toString(16).padStart(2, '0')).join('')

	return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
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
