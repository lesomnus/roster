/**
 * The shell both consoles are drawn in: a sidebar, a main column, and the three
 * states a page is in before it has a screen to show.
 *
 * # Why this is here rather than twice
 *
 * `ts/lib/tenant/` already held the **screens** -- one tenant's rows drawn once,
 * because the two pages differ in who is calling and not in what a table of
 * holders looks like. The shell around them was the part that was still written
 * twice, and it is the part somebody changing the design touches first: the
 * sidebar, what a selected tab looks like, where the sign-out goes, what a page
 * says while it is loading.
 *
 * So a change to the admin console's chrome is a change to the user console's,
 * and there is no version of "I fixed it in one and not the other" left in the
 * layout. What is **not** here is what the two genuinely do not share:
 *
 *	the admin console	a customer picker, and standing one up
 *	the user console	nothing -- its tenant is the caller's, settled before it renders
 *
 * # What it does not decide
 *
 * Which tabs there are, whether each is worth drawing, and what is under them.
 * Those are the page's, because they are the questions the two pages answer
 * differently -- and a shell that took a list of screens *and* decided which to
 * offer would be one place holding two pages' permissions.
 *
 * `ok: false` draws a tab disabled rather than hiding it, which both pages did
 * already and is deliberate: a person who cannot open a screen is better told it
 * exists than left wondering. It is never the decision -- the server refuses
 * either way, and a client that treated this as the rule would be one an altered
 * client could talk out of.
 *
 * @module
 */

/** Tab is one entry in the sidebar. */
export interface Tab<T extends string> {
	/** The value the page switches on, and what the address bar carries. */
	at: T

	/** What a person reads. */
	name: string

	/** Whether this caller holds enough to open it. */
	ok: boolean

	/**
	 * Draws a rule above this tab, so a sidebar can be read in groups.
	 *
	 * Presentation and nothing else. The admin console has two groups that mean
	 * something -- the deployment's own screens, and the customer one of them
	 * selected -- and a person who cannot see where one ends is a person reading
	 * a list of nine buttons.
	 */
	group?: boolean
}

/**
 * Console is the frame: the sidebar, and whatever the page put in the main
 * column.
 *
 * `title` is the heading over the sidebar and is the one string that says which
 * page this is -- `roster` for the deployment, the tenant's own alias for a
 * customer's screens. The user console falls back to `roster` where the caller's
 * role does not cover `Tenant.Get`, because a page that refused to draw without
 * a name would be a page that needs a permission to show a heading.
 */
export function Console<T extends string>(props: {
	title: string
	tabs: Tab<T>[]
	at: T
	onGo: (at: T) => void
	who: string
	onSignOut: () => void
	children?: React.ReactNode
}): React.ReactNode {
	return (
		<div className="console">
			<nav>
				<h1>{props.title}</h1>
				{props.tabs.map((s) => (
					<button
						key={s.at}
						disabled={!s.ok}
						className={[s.at === props.at ? 'at' : '', s.group === true ? 'group' : '']
							.filter((v) => v !== '')
							.join(' ')}
						onClick={() => props.onGo(s.at)}
					>
						{s.name}
					</button>
				))}
				<span className="who">{props.who}</span>
				<button onClick={props.onSignOut}>sign out</button>
			</nav>

			<main>{props.children}</main>
		</div>
	)
}

/** Loading is what a page shows while it is finding out who is asking. */
export function Loading(): React.ReactNode {
	return <main className="loading">…</main>
}

/**
 * Broken is a page that could not find out who is asking, and the way out.
 *
 * The sign-out is the whole point of it: the usual reason to be here is a session
 * that is no longer one, and a page with the error and no button is a person
 * reloading forever.
 */
export function Broken(props: { at: unknown; onSignOut: () => void }): React.ReactNode {
	return (
		<main className="error">
			<p>{props.at instanceof Error ? props.at.message : 'no'}</p>
			<button onClick={props.onSignOut}>sign out</button>
		</main>
	)
}

/**
 * You is what this caller may call, which is the union the server enforces.
 *
 * One screen for both pages, and the sentence that differs between them is not
 * about the page: the second note is where the user console said that a credential
 * can call less than its holder, which is true of an operator's too and had only
 * been written on one of them.
 */
export function You(props: { methods: string[]; note?: React.ReactNode }): React.ReactNode {
	return (
		<section>
			<h2>you</h2>
			<ul className="methods">
				{props.methods.map((m) => (
					<li key={m}>
						<code>{m}</code>
					</li>
				))}
			</ul>
			<p className="note">
				Patterns, not every RPC written out. <code>/roster.*/*</code> is
				everything roster serves, now and after an upgrade — which is why it is
				a pattern and not a list somebody has to keep in step.
			</p>
			<p className="note">
				Narrowed to what this <em>credential</em> may do, not only to what you
				may: a key or a delegation can call less than its holder.
			</p>
			{props.note}
		</section>
	)
}

/** Failed is a read that did not answer, drawn where the read was. */
export function Failed(props: { at: unknown }): React.ReactNode {
	return <p className="error">{props.at instanceof Error ? props.at.message : 'no'}</p>
}
