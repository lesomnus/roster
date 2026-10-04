/**
 * The apps acting in a tenant: which roster-hosted app is answered as whom.
 *
 * A `Nomination` is one app's deployment key answered as one of this tenant's
 * holders (`proto/app/nomination.proto`, `docs/apps.md`). This screen shows them
 * and ends them, and does not make them: putting an app into a tenant is a
 * roster operator's `roster app install`, because nobody inside a tenant holds
 * the app's methods to give its holder. Ending one is the tenant's -- the app's
 * key is refused here from the next request -- and needs nobody's help.
 *
 * `borrower_id` names a control-plane holder, which no tenant can read, so a row
 * is drawn by its `name`, the app's name `install` wrote, and by the holder it
 * acts as, which is this tenant's own.
 *
 * What ending one does not do is said on the screen rather than left to be
 * found: roster's own apps are nominated again at every start (`cli/login.go`,
 * `nominateAs`), and an app's own tenant key outlives its nomination.
 *
 * @module
 */

import { useState } from 'react'
import { useCall, useQuery } from '@lesomnus/payday/react'

import { NominationService } from '#gen/app/nomination_svc_pb.js'

import { Alias, by, said, uuid, when } from './parts.js'
import { Menu } from '#lib/ui.js'

export function Apps(props: {
	tenant: { id?: Uint8Array; alias?: string } | undefined
	may: (m: string) => boolean
}): React.ReactNode {
	const id = props.tenant?.id
	if (id === undefined) return null

	return (
		<section className="within">
			<h3>{props.tenant?.alias}</h3>
			<AppList tenant={id} may={props.may} />
		</section>
	)
}

function AppList(props: { tenant: Uint8Array; may: (m: string) => boolean }): React.ReactNode {
	const vs = useQuery(NominationService.method.list, by(props.tenant))
	const erase = useCall(NominationService.method.erase)
	const [gone, setGone] = useState<string[]>([])
	const [bad, setBad] = useState<string | null>(null)

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = (vs.data?.items ?? []).filter((v) => !gone.includes(uuid(v.id)))

	return (
		<section>
			<h4>apps</h4>
			<p className="note">
				An app run for many tenants acts in this one as one of your own holders,
				with what you bind to that holder and nothing else, and everything it does
				here is in your trail under that holder&apos;s name. Ending it stops the app
				acting here from its next request.
			</p>
			<p className="note">
				Roster&apos;s own apps -- the sign-in page, the account page, a directory -- are
				put back the next time roster starts, because signing in here goes through
				them; to keep one off, ask whoever runs roster. And ending a nomination ends
				only that: a key an app holds in this tenant itself is revoked on its own.
			</p>

			{items.length === 0 && <p className="none">no apps act in this tenant</p>}
			{items.length > 0 && (
				<table>
					<thead>
						<tr>
							<th>app</th>
							<th>acts as</th>
							<th>since</th>
							<th />
						</tr>
					</thead>
					<tbody>
						{items.map((v) => (
							<tr key={uuid(v.id)}>
								<td>
									{v.name === '' ? <span className="mono">{uuid(v.borrowerId)}</span> : v.name}
								</td>
								<td>
									<Alias id={v.actsAs?.id} />
								</td>
								<td>{when(v.dateCreated)}</td>
								<td className="acts">
									<Menu label={`more for ${v.name || uuid(v.borrowerId)}`}>
										<button
											className="danger"
											disabled={!props.may('/roster.NominationService/Erase')}
											onClick={() => {
												setBad(null)
												void erase
													.call({ key: { case: 'id', value: v.id } })
													.then(() => setGone((was) => [...was, uuid(v.id)]))
													.catch((e: unknown) => setBad(said(e)))
											}}
										>
											stop it acting here
										</button>
									</Menu>
								</td>
							</tr>
						))}
					</tbody>
				</table>
			)}

			{bad !== null && <p className="bad">{bad}</p>}
		</section>
	)
}
