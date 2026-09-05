import { create } from '@bufbuild/protobuf'
import { type Timestamp, TimestampSchema } from '@bufbuild/protobuf/wkt'

/**
 * How long a key lasts, as the two key forms offer it.
 *
 * A handful of choices rather than a date field, because what somebody
 * decides here is a policy ("a quarter", "a year") and not a day, and the
 * wire wants an instant (`ApiKeyIssueRequest.expires`). `never` is the
 * default for the CLI's reason -- `--expires` empty is forever -- and because
 * a key that silently stops working is an outage nobody can name.
 */
export const expiries = [
	{ value: 'never', name: 'never expires' },
	{ value: '30d', name: 'for 30 days' },
	{ value: '90d', name: 'for 90 days' },
	{ value: '1y', name: 'for a year' },
] as const

export type Expiry = (typeof expiries)[number]['value']

/** expiresAt turns a choice into the instant the wire takes, or nothing for `never`. */
export function expiresAt(v: string, now = new Date()): Timestamp | undefined {
	const days = ({ '30d': 30, '90d': 90, '1y': 365 } as Record<string, number>)[v]
	if (days === undefined) return undefined

	return create(TimestampSchema, { seconds: BigInt(Math.floor(now.getTime() / 1000) + days * 86400), nanos: 0 })
}

/** until is how a list says when a key stops, and that it does not. */
export function until(v: { seconds: bigint } | undefined): string {
	if (v === undefined) return 'never'

	return new Date(Number(v.seconds) * 1000).toISOString().slice(0, 10)
}
