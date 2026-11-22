/**
 * Shared Nox building blocks for the two consoles: work toolbars, secondary
 * actions, focused add/edit drawers and fixed-width counts. Screens retain
 * their own requests, permission checks and local form state.
 *
 * @module
 */

import { useRef } from 'react'
import { ActionMenu, Button, CompactNumber, Drawer, PageToolbar, Section, type ActionMenuItem } from '@lesomnus/nox'

/**
 * Bar is the top of a screen: what is used often, on the outside.
 *
 * Search first because it is what somebody does most, then filters, then the act
 * that adds one -- pushed to the end so it is always in the same place whatever
 * is in between.
 */
export function Bar(props: { children: React.ReactNode }): React.ReactNode {
	return <PageToolbar className="bar">{props.children}</PageToolbar>
}

/** Fill is the gap in a [Bar] that pushes what follows it to the end. */
export function Fill(): React.ReactNode {
	return <span className="fill" />
}

/**
 * Menu is the acts that are not what somebody came to do.
 *
 * Removing a row is one press away on a screen that lists forty of them, and the
 * press before it is the one that says *this row*. That is the whole reason this
 * exists: not to tidy the row, but to put a deliberate step in front of the act
 * that cannot be undone.
 *
 * It closes on `Escape` and on a press anywhere else, because a menu that stays
 * open is a menu somebody clicks through.
 */
export function Menu(props: { label?: string; items: ActionMenuItem[] }): React.ReactNode {
	return <ActionMenu label={props.label ?? 'more'} items={props.items} />
}

/**
 * Sheet is the focused form that adds one, in a Nox drawer.
 *
 * Not a form under the list, which is where these were: on a screen with rows to
 * scroll, *add* was somewhere a person had to go and find, and it took space from
 * the list on every screen where nobody was adding anything. Opening over the
 * page instead means the button that opens it is at the top, where it is looked
 * for, and the form is the only thing to look at once it is open.
 *
 * `Escape` closes it and so does the scrim. It draws nothing when it is shut, so
 * a form inside keeps no state between openings -- which is right for *add*: the
 * second one is not a correction of the first.
 */
export function Sheet(props: {
	at: boolean
	onClose: () => void
	title: string
	children: React.ReactNode

	/**
	 * What the form in here was refused with, drawn in here.
	 *
	 * The screens keep one error for the whole screen, and drawing it where the
	 * screen is put it **behind the scrim**: the form stayed open, the server had
	 * said why, and the only thing on top of the page was the form with nothing
	 * on it. A refusal belongs where the thing that was refused is.
	 */
	bad?: string | null
}): React.ReactNode {
	const shut = props.onClose
	const firstControl = useRef<HTMLElement>(null)

	return (
		<Drawer initialFocus={firstControl} open={props.at} onClose={shut} side="right" width="min(560px, calc(100vw - 24px))" inset={12} className="sheet" aria-label={props.title}>
			<Section className="sheet-content" ref={node => { firstControl.current = node?.querySelector<HTMLElement>('input:not([type="hidden"]), select, textarea') ?? null }}>
				<header className="sheet-heading"><h2>{props.title}</h2><Button aria-label="Close" onClick={shut}>×</Button></header>
				{props.children}
				{props.bad !== undefined && props.bad !== null && <p className="bad" role="alert">{props.bad}</p>}
			</Section>
		</Drawer>
	)

}

/**
 * Pad is a number in a space the size of the largest one that can arrive.
 *
 * `0001` rather than `1`, with the zeros dimmed almost to the background: the
 * column is the width it will always be, and a person reads the `1` without the
 * rest asking to be read. It says *up to four digits belong here*, which is a
 * thing worth knowing before the number that needs them turns up.
 *
 * A number wider than `width` is drawn whole. Cutting it would be answering a
 * question about layout with a lie about the value.
 */
export function Pad(props: { value: number; width: number }): React.ReactNode {
	return <CompactNumber mode="integer" digits={props.width} value={props.value} />

}

/**
 * Slot is a space kept for a value whose width is not known until it arrives.
 *
 * An alias, a name, a date. `ch` is what to keep room for, in characters of the
 * font the value is drawn in -- so a column of dates is the width of a date
 * whether or not the row below has one, and a list does not shuffle sideways as
 * it loads.
 *
 * It does not cut anything off: a longer value takes the room it needs. What the
 * width buys is the common case, where the difference between values is a few
 * characters and the column would otherwise twitch on every render.
 */
export function Slot(props: { ch: number; children: React.ReactNode }): React.ReactNode {
	return (
		<span className="slot" style={{ minWidth: `${props.ch}ch` }}>
			{props.children}
		</span>
	)
}
