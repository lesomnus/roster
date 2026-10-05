/**
 * Somebody's picture as a page draws it, and the two things a person may do
 * about it: keep another image, or take it away.
 *
 * # Which picture
 *
 * The largest rendition the row carries -- every size from a `Get`, one from a
 * list (`server/core/portrait.go`) -- and the profile's URL when roster has no
 * copy, which is a picture somebody chose and roster does not fetch. A page
 * draws what it has rather than asking again for a larger one.
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

/** largest is the biggest rendition a row carries; they are kept smallest first. */
export function largest(h: Holder): string | undefined {
	return h.portrait?.renditions.at(-1)?.uri
}

export function Picture(props: { holder: Holder; may: (method: string) => boolean }): React.ReactNode {
	const portray = useCall(HolderService.method.portray)
	const [bad, setBad] = useState<string | null>(null)
	const h = props.holder
	const src = largest(h) ?? (h.profile?.picture || undefined)
	const may = props.may('/roster.HolderService/Portray')

	const keep = (image: Uint8Array): void => {
		setBad(null)
		void portray
			.call({ ref: { key: { case: 'id', value: h.id } }, dateUpdated: h.dateUpdated, image })
			.catch((e: unknown) => setBad(e instanceof Error ? e.message : 'no'))
	}

	return (
		<div className="picture">
			{src === undefined ? <span className="none">no picture</span> : <img src={src} alt="" width={64} height={64} />}
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
