/**
 * Somebody's picture as a page draws it, and the two things a person may do
 * about it: keep another image, or take it away.
 *
 * # Which picture, and how large
 *
 * A page says how large it draws one, and gets the smallest rendition that is
 * sharp at that size on a display of two device pixels to one -- or the
 * largest the row carries, when none is. The row carries every size from a
 * `Get` and one, the 64, from a list (`server/core/portrait.go`), so a screen
 * drawing from a list draws at 32: a 64 drawn at 64 is a picture stretched to
 * twice its size on most displays a person owns. And the size drawn is the
 * page's rather than the row's, so a write answering with every size does not
 * make the picture jump until the list is read again.
 *
 * The profile's URL when roster has no copy, which is a picture somebody chose
 * and roster does not fetch.
 *
 * # What is sent
 *
 * The file a person picked, as it is, when it is no larger than `Portray`
 * takes on a side (`MAX_SIDE`). `Portray` renders it -- the crop, the sizes,
 * the format -- so what is on the row is roster's whichever page wrote it.
 *
 * One that is larger, which a phone's photograph and a screenshot of a dense
 * display are, is drawn to a canvas first, as large as roster takes on its
 * longer side and in the same proportions, and the drawing is sent: roster
 * refuses it otherwise, from its header, because what one image costs to
 * render is what its sides say (`server/portrait/portrait.go`, `MaxSide`). It
 * is drawn the way up the person sees it, its orientation tag honoured, over
 * white and as JPEG, as roster renders it. Either is weighed before it is sent,
 * so a drawing still over `MAX_BYTES` is refused here as a file is.
 *
 * @module
 */
import { useCall } from '@lesomnus/payday/react'
import { useState } from 'react'

import type { Holder } from '#gen/roster/payday/holder_pb.js'
import { HolderService } from '#gen/roster/payday/holder_svc_pb.js'
import { MAX_BYTES, smaller } from '#lib/portrait.js'

/**
 * pick is the smallest rendition sharp at `px` CSS pixels, at two device pixels
 * to one, or the largest the row carries. They are kept smallest first.
 */
export function pick(h: Holder, px: number): string | undefined {
	const vs = h.portrait?.renditions ?? []

	return (vs.find((v) => v.size >= 2 * px) ?? vs.at(-1))?.uri
}

/**
 * sending is what is sent for the file `f` somebody picked: the file, or a
 * drawing of it no larger than roster takes (see *What is sent*).
 *
 * A file this browser cannot draw is sent as it is, and roster's refusal says
 * what it is not.
 */
async function sending(f: File): Promise<Blob> {
	let image: ImageBitmap
	try {
		image = await createImageBitmap(f, { imageOrientation: 'from-image' })
	} catch {
		return f
	}

	try {
		const to = smaller(image.width, image.height)
		if (to === undefined) {
			return f
		}

		const canvas = document.createElement('canvas')
		canvas.width = to.width
		canvas.height = to.height
		const g = canvas.getContext('2d')
		if (g === null) {
			return f
		}
		// White under it, because JPEG has no transparency, as roster puts it
		// under what it renders.
		g.fillStyle = '#fff'
		g.fillRect(0, 0, to.width, to.height)
		g.imageSmoothingQuality = 'high'
		g.drawImage(image, 0, 0, to.width, to.height)

		// High, because roster draws it once more before anybody sees it.
		const drawn = await new Promise<Blob | null>((done) => canvas.toBlob(done, 'image/jpeg', 0.92))

		return drawn ?? f
	} finally {
		image.close()
	}
}

/** Picture draws somebody's picture `px` CSS pixels square; 64 unless said. */
export function Picture(props: { holder: Holder; may: (method: string) => boolean; px?: number }): React.ReactNode {
	const portray = useCall(HolderService.method.portray)
	const [bad, setBad] = useState<string | null>(null)
	const h = props.holder
	const px = props.px ?? 64
	const src = pick(h, px) ?? (h.profile?.picture || undefined)
	const may = props.may('/roster.HolderService/Portray')

	const keep = (image: Uint8Array): void => {
		setBad(null)
		void portray
			.call({ ref: { key: { case: 'id', value: h.id } }, dateUpdated: h.dateUpdated, image })
			.catch((e: unknown) => setBad(e instanceof Error ? e.message : 'no'))
	}

	return (
		<div className="picture">
			{src === undefined ? <span className="none">no picture</span> : <img src={src} alt="" width={px} height={px} />}
			{may && (
				<span className="actions">
					<label>
						another
						<input
							type="file"
							accept="image/jpeg,image/png,image/gif,image/webp"
							disabled={portray.state === 'pending'}
							onChange={(e) => {
								const f = e.currentTarget.files?.[0]
								e.currentTarget.value = ''
								if (f === undefined) {
									return
								}
								void sending(f)
									.then(async (b) => {
										if (b.size > MAX_BYTES) {
											setBad(`${f.name} is larger than 2 MiB`)

											return
										}
										keep(new Uint8Array(await b.arrayBuffer()))
									})
									.catch((e: unknown) => setBad(e instanceof Error ? e.message : 'no'))
							}}
						/>
					</label>
					{src !== undefined && (
						<button disabled={portray.state === 'pending'} onClick={() => keep(new Uint8Array())}>
							take it away
						</button>
					)}
				</span>
			)}
			{bad !== null && <p className="bad">{bad}</p>}
		</div>
	)
}
