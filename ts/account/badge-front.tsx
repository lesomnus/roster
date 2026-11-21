import { useEffect, useRef, useState, type ReactNode } from 'react'
import { clone, create } from '@bufbuild/protobuf'
import { useCall } from '@lesomnus/payday/react'
import { CompactNumber, Form } from '@lesomnus/nox'
import { ProfileSchema, type Holder } from '#gen/roster/payday/holder_pb.js'
import { HolderService } from '#gen/roster/payday/holder_svc_pb.js'
import { MAX_PORTRAIT_BYTES } from '#lib/picture.js'
import type { BadgeIdentity } from './badge-model.js'

type Field = 'displayName' | 'employeeNo'
const labels: Record<Field, string> = { displayName: 'display name', employeeNo: 'employee number' }

/** Actual HTML controls on the same projected plane as the WebGL front plate. */
export function BadgeFront({ identity, holder, mayUpdate, mayPortray, provisioned, onEditing }: {
	identity: BadgeIdentity
	holder?: Holder | undefined
	mayUpdate: boolean
	mayPortray: boolean
	provisioned: string
	onEditing(active: boolean): void
}) {
	const update = useCall(HolderService.method.update)
	const portray = useCall(HolderService.method.portray)
	const file = useRef<HTMLInputElement>(null)
	const input = useRef<HTMLInputElement>(null)
	const finishing = useRef(false)
	const composing = useRef(false)
	const [field, setField] = useState<Field | null>(null)
	const [draft, setDraft] = useState('')
	const [note, setNote] = useState<{ ok: boolean; text: string } | null>(null)
	const [failedPortrait, setFailedPortrait] = useState<string | null>(null)
	const pending = update.state === 'pending' || portray.state === 'pending'
	const editable = mayUpdate && holder !== undefined
	const photoEditable = mayPortray && holder !== undefined
	// Format the numeric part of an existing prefixed ID without rewriting it.
	const employeeDigits = identity.employeeNo.trim().match(/^(?:[a-z]+[- ]?)?(\d+)$/i)?.[1]
	useEffect(() => { onEditing(field !== null); return () => onEditing(false) }, [field, onEditing])
	useEffect(() => { if (field !== null) input.current?.focus({ preventScroll: true }) }, [field])
	const fail = (error: unknown) => setNote({ ok: false, text: error instanceof Error ? error.message : 'Could not save' })
	const begin = (next: Field) => {
		if (!editable || pending) return
		finishing.current = false
		composing.current = false
		setNote(null)
		setDraft(holder.profile?.[next] ?? '')
		setField(next)
	}
	const save = (text = draft) => {
		if (!editable || field === null || pending || finishing.current) return
		// Enter can cause blur as the pending input disables. Commit only once;
		// Escape also sets this guard before unmounting the input.
		finishing.current = true
		setNote(null)
		if (text.trim() === (holder.profile?.[field] ?? '')) { setField(null); return }
		const profile = clone(ProfileSchema, holder.profile ?? create(ProfileSchema))
		profile[field] = text.trim()
		void update.call({
			ref: { key: { case: 'id', value: holder.id } },
			dateUpdated: holder.dateUpdated,
			// Update replaces the whole profile: preserve its URL, locale and
			// other fields instead of blanking them during a one-field edit.
			profile,
		}).then(() => { setField(null); setNote({ ok: true, text: 'Saved' }) }).catch(error => { finishing.current = false; fail(error) })
	}
	const keep = (image: Uint8Array) => {
		if (!photoEditable || pending) return
		setNote(null)
		void portray.call({ ref: { key: { case: 'id', value: holder.id } }, dateUpdated: holder.dateUpdated, image })
			.then(() => setNote({ ok: true, text: image.length === 0 ? 'Photo removed' : 'Photo updated' })).catch(fail)
	}
	const value = (key: Field, text: ReactNode, description = typeof text === 'string' ? text : identity.employeeNo) => field === key ? (
		<input ref={input} aria-label={`Employee ID ${labels[key]}`} aria-description={provisioned !== '' ? 'Your directory may overwrite this change.' : undefined} value={draft} disabled={pending}
			onChange={e => setDraft(e.currentTarget.value)} onBlur={e => save(e.currentTarget.value)}
			onCompositionStart={() => { composing.current = true }} onCompositionEnd={() => { composing.current = false }} onKeyDown={e => {
				if (e.nativeEvent.isComposing || composing.current) return
				if (e.key === 'Enter') { e.preventDefault(); save(e.currentTarget.value) }
				if (e.key === 'Escape' && !pending) { e.preventDefault(); finishing.current = true; setField(null); setNote(null) }
			}} />
	) : editable ? (
		<button type="button" className="badge-value" aria-label={`Edit employee ID ${labels[key]}`} aria-description={description} title={description} disabled={pending || field !== null} onClick={() => begin(key)}>{text}</button>
	) : text
	return (
		<Form columns={1} className="badge-print" onSubmit={e => { e.preventDefault(); if (!composing.current) save() }}>
			<div className="badge-portrait">
				<button type="button" className="badge-photo" aria-label="Change employee ID photo" disabled={!photoEditable || pending || field !== null} onClick={() => file.current?.click()}>
					{identity.portrait === undefined || failedPortrait === identity.portrait
						? <span className="badge-initials">{identity.name.trim().split(/\s+/).slice(0, 2).map(part => Array.from(part)[0] ?? '').join('').toLocaleUpperCase()}</span>
						: <img src={identity.portrait} alt={`${identity.name}'s portrait`} onError={() => setFailedPortrait(identity.portrait ?? null)} />}
					{photoEditable && <span className="badge-photo-caption">Change photo</span>}
				</button>
				{photoEditable && identity.portrait !== undefined && <button type="button" className="badge-photo-remove" aria-label="Remove employee ID photo" disabled={pending || field !== null} onClick={() => keep(new Uint8Array())}>Remove</button>}
			</div>
			<input ref={file} type="file" hidden aria-label="Employee ID photo" accept="image/jpeg,image/png,image/gif,image/webp" disabled={!photoEditable || pending || field !== null} onChange={e => {
				const picked = e.currentTarget.files?.[0]
				e.currentTarget.value = ''
				if (picked === undefined) return
				if (picked.size > MAX_PORTRAIT_BYTES) { setNote({ ok: false, text: `${picked.name} is larger than 2 MiB` }); return }
				void picked.arrayBuffer().then(bytes => keep(new Uint8Array(bytes))).catch(fail)
			}} />
			<h3 className="badge-name">{value('displayName', identity.name)}</h3>
			<p className="badge-alias">@{identity.alias}</p>
			<div className="badge-bottom">
				<div className="badge-employee-number">{value('employeeNo', <CompactNumber mode="integer" digits={5} value={employeeDigits === undefined ? Number.NaN : Number(employeeDigits)} label={`Employee number ${identity.employeeNo || 'not set'}`} />)}</div>
				<svg className="badge-company-logo" viewBox="0 0 24 24" role="img" aria-label={identity.brand}><path d="M3 3h7v7H3zm11 0h7v7h-7zM3 14h7v7h-7zM14 14h7v7h-7z" fill="currentColor" /></svg>
			</div>
			{note !== null && <p className={note.ok ? 'badge-announcement' : 'badge-feedback'} role={note.ok ? 'status' : 'alert'}>{note.text}</p>}
		</Form>
	)
}
