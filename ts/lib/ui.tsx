/**
 * The pieces a screen is built from, so that the six rules both consoles are drawn
 * by are held in one place rather than in nine.
 *
 * # What the rules are
 *
 * **A click does not need a box.** A button is where a person expects to be able
 * to press, and the border that used to say so is noise once there are six of
 * them on a row. What is left is position and, on the primary act, colour.
 *
 * **A container is one container.** A screen is a card, and what is inside it is
 * inside it -- a bordered region within a bordered region says there are two
 * things when there is one. Nesting is said with space and with a rule, not with
 * another box.
 *
 * **Things at the same level are close, but not touching.** A list reads as a
 * list because the gap between rows is smaller than the gap around the list, and
 * it reads as separate rows because the gap is not zero.
 *
 * **A value that changes does not move what is beside it.** A date, an alias, a
 * count: the space is sized for what will arrive, so the column does not jump
 * when a shorter one does. Where the range is known, the unused part is drawn
 * rather than left blank -- [Pad] writes `0001` with the zeros dimmed, which says
 * *four digits go here* without saying anything a person has to read.
 *
 * **The top of a screen is what you came to do**: find something, filter it, add
 * one. What is rarer, and what cannot be undone, is behind [Menu].
 *
 * **A list is cards that line up**, not a grid of text. A row may take more than
 * one line, and often should -- the name a person reads and the identifier they
 * copy are not the same size and do not belong side by side.
 *
 * # Why the rows are still a `<table>`
 *
 * Because they are a table: parallel fields across records, read down as columns.
 * The card look is `border-spacing` and a background, not `display: block`, so
 * nothing is taken away from a screen reader to get it. A row that spans two
 * lines does it inside a cell, which is where the second line belongs anyway.
 *
 * @module
 */

import { useEffect, useRef, useState } from 'react'

/**
 * Bar is the top of a screen: what is used often, on the outside.
 *
 * Search first because it is what somebody does most, then filters, then the act
 * that adds one -- pushed to the end so it is always in the same place whatever
 * is in between.
 */
export function Bar(props: { children: React.ReactNode }): React.ReactNode {
	return <div className="bar">{props.children}</div>
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
export function Menu(props: { label?: string; children: React.ReactNode }): React.ReactNode {
	const [at, go] = useState(false)
	const box = useRef<HTMLDivElement>(null)

	useEffect(() => {
		if (!at) return

		const away = (e: MouseEvent): void => {
			if (box.current?.contains(e.target as Node) !== true) go(false)
		}
		const key = (e: KeyboardEvent): void => {
			if (e.key === 'Escape') go(false)
		}

		document.addEventListener('mousedown', away)
		document.addEventListener('keydown', key)

		return () => {
			document.removeEventListener('mousedown', away)
			document.removeEventListener('keydown', key)
		}
	}, [at])

	return (
		<div className="menu" ref={box}>
			<button
				type="button"
				aria-label={props.label ?? 'more'}
				aria-expanded={at}
				onClick={() => go((was) => !was)}
			>
				⋯
			</button>
			{/* Closed on any press inside, because every item in here is an act:
			    there is nothing to pick without also doing it. */}
			{at && (
				<div className="items" onClick={() => go(false)}>
					{props.children}
				</div>
			)}
		</div>
	)
}

/**
 * Sheet is the form that adds one, arriving from the bottom.
 *
 * Not a form under the list, which is where these were: on a screen with rows to
 * scroll, *add* was somewhere a person had to go and find, and it took space from
 * the list on every screen where nobody was adding anything. Coming up over the
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

	useEffect(() => {
		if (!props.at) return

		const key = (e: KeyboardEvent): void => {
			if (e.key === 'Escape') shut()
		}

		document.addEventListener('keydown', key)

		return () => document.removeEventListener('keydown', key)
	}, [props.at, shut])

	if (!props.at) return null

	return (
		<div className="scrim" onClick={shut}>
			{/* The press that opened this one lands here too; it must not close it. */}
			<div
				className="sheet"
				role="dialog"
				aria-label={props.title}
				onClick={(e) => e.stopPropagation()}
			>
				<div className="handle" />
				<h5>{props.title}</h5>
				{props.children}
				{props.bad !== undefined && props.bad !== null && <p className="bad">{props.bad}</p>}
			</div>
		</div>
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
	const v = String(props.value)
	const lead = '0'.repeat(Math.max(0, props.width - v.length))

	return (
		<span className="pad">
			{lead !== '' && <span className="lead">{lead}</span>}
			{v}
		</span>
	)
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
