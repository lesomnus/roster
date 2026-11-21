import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@lesomnus/payday/react'

import type { Holder } from '#gen/roster/payday/holder_pb.js'
import { HolderService } from '#gen/roster/payday/holder_svc_pb.js'
import { pick } from '#lib/picture.js'
import type { BadgeIdentity, BadgeModel } from './badge-model.js'
import { BadgeFront } from './badge-front.js'
import './badge.css'

type BadgeProps = { own: Uint8Array; alias: string; brand: string; mayRead: boolean; mayUpdate: boolean; mayPortray: boolean; provisioned: string }

/** The live HTML face uses the editor's same query and write permissions. */
export function EmployeeBadge(props: BadgeProps) {
	return props.mayRead ? <ProfileBadge {...props} /> : <BadgeScene alias={props.alias} brand={props.brand} mayUpdate={false} mayPortray={false} provisioned={props.provisioned} />
}

function ProfileBadge(props: BadgeProps) {
	const row = useQuery(HolderService.method.get, { ref: { key: { case: 'id', value: props.own } } })
	return <BadgeScene alias={props.alias} brand={props.brand} holder={row.data} mayUpdate={props.mayUpdate} mayPortray={props.mayPortray} provisioned={props.provisioned} />
}

function BadgeScene({ alias, brand, holder, mayUpdate, mayPortray, provisioned }: { alias: string; brand: string; holder?: Holder | undefined; mayUpdate: boolean; mayPortray: boolean; provisioned: string }) {
	const stage = useRef<HTMLElement>(null)
	const viewport = useRef<HTMLDivElement>(null)
	const canvas = useRef<HTMLCanvasElement>(null)
	const bounds = useRef<HTMLElement>(null)
	const runtime = useRef<BadgeModel | null>(null)
	const [editing, setEditing] = useState(false)
	const [focused, setFocused] = useState(false)
	const identity: BadgeIdentity = {
		alias, brand,
		name: holder?.profile?.displayName || holder?.name || alias,
		department: holder?.profile?.department || '',
		employeeNo: holder?.profile?.employeeNo || '',
		portrait: holder === undefined ? undefined : pick(holder, 160) ?? (holder.profile?.picture || undefined),
	}
	const latest = useRef(identity)
	latest.current = identity

	useEffect(() => {
		const container = stage.current
		const view = viewport.current
		const surface = canvas.current
		const target = bounds.current
		if (container === null || view === null || surface === null || target === null) return
		let canceled = false
		let model: BadgeModel | undefined
		container.dataset.renderer = 'pending'
		// Settings and other pages do not pay for Three until this scene mounts.
		// No external model or HDR assets are fetched.
		void import('./badge-model.js').then(({ mountBadge }) => {
			if (canceled) return
			model = mountBadge(container, view, surface, target, latest.current)
			runtime.current = model
		}).catch((error: unknown) => {
			if (!canceled) {
				container.dataset.renderer = 'fallback'
				if (import.meta.env.DEV) console.warn('The 3D badge could not start:', error)
			}
		})
		return () => {
			canceled = true
			model?.dispose()
			if (runtime.current === model) runtime.current = null
		}
	}, [])

	useEffect(() => {
		runtime.current?.update(latest.current)
	}, [identity.name, identity.alias, identity.brand, identity.department, identity.employeeNo, identity.portrait])
	useEffect(() => {
		// Removing the finished input need not dispatch a browser blur event.
		const inside = bounds.current?.contains(document.activeElement) ?? false
		if (inside !== focused) setFocused(inside)
		runtime.current?.hold(editing || inside)
	}, [editing, focused])

	return (
		<section className="badge-scene" ref={stage} data-renderer="pending" aria-label="Your employee ID">
			<div className="badge-viewport" ref={viewport}>
				<div className="badge-wordmark" aria-hidden="true">{brand}</div>
				<div className="badge-arrival">
					<canvas ref={canvas} className="badge-canvas" aria-hidden="true" />
					<article ref={bounds} className="employee-badge badge-turn" aria-label={`Employee ID for ${identity.name}`}
						onFocusCapture={() => setFocused(true)} onBlurCapture={event => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setFocused(false) }}>
						<BadgeFront identity={identity} holder={holder} mayUpdate={mayUpdate} mayPortray={mayPortray} provisioned={provisioned} onEditing={setEditing} />
					</article>
				</div>
			</div>
		</section>
	)
}
