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
 * The file a person picked, as it is. `Portray` renders it -- the crop, the
 * sizes, the format -- so a page never draws an image to send, and what is on
 * the row is roster's whichever page wrote it.
 *
 * @module
 */
import { useCall } from '@lesomnus/payday/react'
import { useState } from 'react'

import type { Holder } from '#gen/roster/payday/holder_pb.js'
import { HolderService } from '#gen/roster/payday/holder_svc_pb.js'

/** The most `Portray` takes, which is roster's limit and said before sending. */
const MAX_BYTES = 2 << 20

/**
 * pick is the smallest rendition sharp at `px` CSS pixels, at two device pixels
 * to one, or the largest the row carries. They are kept smallest first.
 */
export function pick(h: Holder, px: number): string | undefined {
	const vs = h.portrait?.renditions ?? []

	return (vs.find((v) => v.size >= 2 * px) ?? vs.at(-1))?.uri
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
								if (f.size > MAX_BYTES) {
									setBad(`${f.name} is larger than 2 MiB`)

									return
								}
								void f.arrayBuffer().then((b) => keep(new Uint8Array(b)))
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
