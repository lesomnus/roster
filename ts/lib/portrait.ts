/**
 * What `Portray` takes, as a page holds it before sending: how much an image
 * may weigh, how many pixels it may have on a side, and how large a page draws
 * one that has more.
 *
 * Nothing here names the DOM, so that the arithmetic is tested where a page is
 * not (`ts/e2e/picture.spec.ts`); the drawing is `picture.tsx`'s.
 *
 * @module
 */

/** The most `Portray` takes in bytes: roster's `portrait.MaxBytes`. */
export const MAX_BYTES = 2 << 20

/**
 * The most pixels `Portray` takes on a side: roster's `portrait.MaxSide`,
 * which a larger image is refused at, from its header and before anything is
 * decoded. Said here as a number, because roster tells a page none of its
 * limits; the two are the same number by being written so.
 */
export const MAX_SIDE = 2048

/**
 * smaller is the size an image of `width` by `height` pixels is drawn at to be
 * sent: [MAX_SIDE] on its longer side and the other in the same proportion,
 * rounded and never below a pixel. Undefined for an image within the bound,
 * which is sent as it was picked.
 */
export function smaller(width: number, height: number): { width: number; height: number } | undefined {
	const long = Math.max(width, height)
	if (long <= MAX_SIDE) {
		return undefined
	}

	const short = Math.max(1, Math.round((Math.min(width, height) * MAX_SIDE) / long))

	return width >= height ? { width: MAX_SIDE, height: short } : { width: short, height: MAX_SIDE }
}
