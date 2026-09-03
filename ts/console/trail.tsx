/**
 * The trail: what was done to one customer's rows, by whom, when.
 *
 * Read-only, and the cheapest screen with the most in it. `Audit` is walled
 * three ways -- the tenant the row is in, the tenant the actor is in, and a
 * counterpart's -- so an act across tenants appears to both; this lists by the
 * customer's own `tenant_id`, which is *what happened to their rows*, and says
 * so, because an operator reading it will otherwise look for the other half.
 *
 * What is drawn is what a person can read: who (by alias when the store has
 * them, by identifier when not), the action, and what it was done to, named by
 * the entity its identifier's domain says it is. The `patch` and `value` the
 * row carries are not drawn -- they are the rows' own bytes, secrets already
 * stripped by the recorder, and a table is the wrong place for them.
 */

import { pdid } from '@lesomnus/payday'
import { useQuery } from '@lesomnus/payday/react'

import { AuditService } from '../gen/roster/payday/audit_svc_pb.js'
import '../gen/domains.js'

import { Alias, said, uuid } from './organisation.js'

/** names is every domain this app registered, by number, so a row can say what it was about. */
const names = new Map<number, string>()
for (const [d, name] of pdid.domains()) names.set(Number(d), name)

function when(v: { seconds: bigint } | undefined): string {
	if (v === undefined) return ''

	return new Date(Number(v.seconds) * 1000).toISOString().replace('T', ' ').slice(0, 19)
}

export function Trail(props: { tenant: { id?: Uint8Array; alias?: string } | undefined }): React.ReactNode {
	const id = props.tenant?.id
	if (id === undefined) return null

	return (
		<section className="within trail">
			<h3>{props.tenant?.alias} — trail</h3>
			<Rows tenant={id} />
		</section>
	)
}

function Rows(props: { tenant: Uint8Array }): React.ReactNode {
	const vs = useQuery(AuditService.method.list, {
		filters: [{ tenantId: props.tenant }],
		size: 100,
	})

	if (vs.state === 'pending') return <p className="loading">…</p>
	if (vs.state === 'error') return <p className="bad">{said(vs.error)}</p>

	const items = vs.data?.items ?? []

	return (
		<section>
			<p className="note">
				What was done to this customer's rows, most recent first. An act by one of
				their people on another customer's rows is recorded under that customer;
				this is the half about <em>these</em> rows.
			</p>
			{items.length === 0 && <p className="none">nothing recorded yet</p>}
			{items.length > 0 && (
				<table>
					<thead>
						<tr>
							<th>when</th>
							<th>who</th>
							<th>did</th>
							<th>to</th>
						</tr>
					</thead>
					<tbody>
						{items.map((v) => (
							<tr key={uuid(v.id)}>
								<td className="mono dim">{when(v.dateCreated)}</td>
								<td>
									<Alias id={v.actorId.length === 16 ? v.actorId : undefined} />
								</td>
								<td className="mono">{v.action}</td>
								<td className="mono">
									{names.get(v.domain) ?? `domain ${v.domain}`}{' '}
									<span className="dim">{uuid(v.objectId).slice(0, 8)}</span>
								</td>
							</tr>
						))}
					</tbody>
				</table>
			)}
		</section>
	)
}
