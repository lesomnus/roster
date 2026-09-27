/**
 * The small pieces every tenant screen draws with: an identifier written the way
 * a person reads one, a holder or a role by name, and a picker.
 *
 * They lived in `organisation.tsx` and were imported from there by two other
 * screens, which was the shape that made the split obvious: a screen file holding
 * everything else's helpers is a file whose name says the wrong thing about what
 * is in it.
 *
 * Nothing here reads a tenant of its own. Each takes what it needs, because on the
 * admin listener there is no wall and a component that decided its own scope would
 * be one place the narrowing could go missing.
 *
 * @module
 */

import { useQuery, useRow } from '@lesomnus/payday/react'

import type { Role } from '../../gen/app/role_pb.js'
import type { Holder } from '../../gen/roster/payday/holder_pb.js'
import { HolderService } from '../../gen/roster/payday/holder_svc_pb.js'

/** May is whether this caller holds a method, as the pages work it out. */
export type May = (method: string) => boolean

/** uuid is the bytes an identifier arrives as, written the way a person reads one. */
export function uuid(v: Uint8Array | undefined): string {
	if (v === undefined || v.length !== 16) return ''

	const h = [...v].map((b) => b.toString(16).padStart(2, '0')).join('')

	return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

/** when is a stamp as a date, which is all any of these screens shows of one. */
export function when(v: { seconds: bigint } | undefined): string {
	if (v === undefined) return ''

	return new Date(Number(v.seconds) * 1000).toISOString().slice(0, 10)
}

export function said(e: unknown): string {
	return e instanceof Error ? e.message : 'no'
}

export const ref = (id: Uint8Array) => ({ key: { case: 'id' as const, value: id } })

/**
 * by is a list request narrowed to one tenant, which is every list on these
 * screens.
 *
 * Written once because forgetting it is the failure that does not look like one:
 * on the admin listener there is no wall, so an unfiltered list is every tenant's
 * rows drawn under one tenant's heading.
 */
export const by = (tenant: Uint8Array) => ({ filters: [{ tenant: ref(tenant) }] })

/**
 * Alias draws a holder by name, from the store if the person is there and by
 * identifier if not -- a listed row names its holder and does not carry it.
 */
export function Alias(props: { id: Uint8Array | undefined }): React.ReactNode {
	const v = useRow<Holder>('roster.Holder', props.id)
	if (props.id === undefined) return <span className="none">nobody</span>

	return v?.alias !== undefined && v.alias !== '' ? (
		<span>{v.alias}</span>
	) : (
		<span className="mono dim">{uuid(props.id).slice(0, 8)}</span>
	)
}

/** RoleName draws a role by alias, from the store, or by identifier until it is there. */
export function RoleName(props: { id: Uint8Array | undefined }): React.ReactNode {
	const v = useRow<Role>('roster.Role', props.id)
	if (props.id === undefined) return <span className="none">no role</span>

	return v?.alias !== undefined && v.alias !== '' ? (
		<span>{v.alias}</span>
	) : (
		<span className="mono dim">{uuid(props.id).slice(0, 8)}</span>
	)
}

/**
 * PickHolder is a choice among the tenant's people, by alias, answering the
 * identifier -- so a form never takes a name the server would have to resolve.
 */
export function PickHolder(props: { tenant: Uint8Array; name: string }): React.ReactNode {
	const vs = useQuery(HolderService.method.list, {
		filters: [{ tenant: ref(props.tenant) }],
	})
	const items = vs.data?.items ?? []

	return (
		<select name={props.name} defaultValue="" required>
			<option value="" disabled>
				who
			</option>
			{items.map((v) => (
				<option key={uuid(v.id)} value={uuid(v.id)}>
					{v.alias}
				</option>
			))}
		</select>
	)
}

/** bytesOf is a uuid string back to the sixteen bytes the wire carries. */
export function bytesOf(v: string): Uint8Array | undefined {
	const h = v.replaceAll('-', '')
	if (!/^[0-9a-fA-F]{32}$/.test(h)) return undefined

	const b = new Uint8Array(16)
	for (let i = 0; i < 16; i++) b[i] = Number.parseInt(h.slice(i * 2, i * 2 + 2), 16)

	return b
}
