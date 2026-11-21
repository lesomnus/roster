export const BADGE_ARRIVAL_SECONDS = 2.4

/** Rise with zero speed/acceleration at both ends, without card overshoot. */
export function badgeArrival(time: number, distance: number) {
	const s = Math.max(0, Math.min(time / BADGE_ARRIVAL_SECONDS, 1))
	if (s === 1) return 0
	const progress = s * s * s * (10 + s * (-15 + 6 * s))
	return -distance * (1 - progress)
}
