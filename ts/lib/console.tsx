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

import { Button, NavigationPanelRoot, NavigationPanelHeader, NavigationPanelBody, PageToolbar, Section, SectionLayout, Skeleton } from '@lesomnus/nox'
import { RosterMark, ThemeChoice } from './presentation.js'

/** Tab is one entry in the sidebar. */
export interface Tab<T extends string> {
	/** The value the page switches on, and what the address bar carries. */
	at: T

	/** What a person reads. */
	name: string

	/** Whether this caller holds enough to open it. */
	ok: boolean

	/**
	 * Where this tab sits in the sidebar's shape, and nothing else if it is
	 * ordinary.
	 *
	 * `'group'` draws a rule above it, so a long sidebar can be read in parts: a
	 * person who cannot see where one ends is a person reading a list.
	 *
	 * `'head'` draws the rule and draws the tab **large**, as the name of what is
	 * under it. There is one, and it is the tenant: every tab below it is that
	 * tenant's rows, so the thing that says which tenant is the thing you open to
	 * see the tenant itself. That is a screen like any other -- it has an `at`, it
	 * can be refused, it takes the selection -- so it is a `Tab` and not a second
	 * argument to [Console]. Only its drawing differs.
	 *
	 * One field rather than two booleans: a head is the start of a group, so
	 * `{head: true, group: false}` would be a state with nothing to mean.
	 */
	lead?: 'group' | 'head'
}

/**
 * Console is the frame: the sidebar, and whatever the page put in the main
 * column.
 *
 * `title` is the heading over the sidebar, and says which **deployment** this is
 * rather than which tenant: the tenant is named by its own tab now, and a name in
 * both places is a name somebody has to keep in step.
 *
 * It is optional because the user console has nothing to put there. That page is
 * reached at the tenant's own host, so the tenant is the whole of what it is --
 * the head tab is the top of its sidebar, and a `roster` above it would be the
 * product's name where the reader wanted theirs.
 */
export function Console<T extends string>(props: {
	title?: string
	tabs: Tab<T>[]
	at: T
	onGo: (at: T) => void
	who: string
	onSignOut: () => void
	children?: React.ReactNode
}): React.ReactNode {
	const selected = props.tabs.find(tab => tab.at === props.at)
	const tenant = props.tabs.find(tab => tab.lead === 'head')?.name
	return (
		<div className="console">
			<NavigationPanelRoot className="console-navigation">
				<NavigationPanelHeader className="console-brand">
					<RosterMark />
					<div><h1>{props.title ?? tenant ?? 'roster'}</h1><small>{props.title ? 'Administration' : 'Workspace'}</small></div>
				</NavigationPanelHeader>
				<NavigationPanelBody>
					<nav aria-label="Main navigation">
						<div className="console-links">
							{props.tabs.map(tab => <Button key={tab.at} disabled={!tab.ok}
								aria-current={tab.at === props.at ? 'page' : undefined}
								className={[tab.at === props.at ? 'at' : '', tab.lead ?? ''].filter(Boolean).join(' ')}
								onClick={() => props.onGo(tab.at)}><NavIcon name={tab.at} /><span>{tab.name}</span></Button>)}
						</div>
						<div className="console-person"><span className="console-avatar" aria-hidden="true">{props.who.slice(0, 1).toUpperCase()}</span><span className="who">{props.who}</span><Button aria-label="sign out" title="sign out" onClick={props.onSignOut}><svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true"><path d="M10 5H5v14h5m5-12 5 5-5 5M9 12h11" /></svg></Button></div>
					</nav>
				</NavigationPanelBody>
			</NavigationPanelRoot>
			<main className="console-workspace">
				<PageToolbar className="console-toolbar" actions={<ThemeChoice />}>
					<div className="console-location"><span>{props.title ? 'roster' : 'Workspace'}<span aria-hidden="true"> / </span>{tenant ?? 'Overview'}</span><strong>{selected?.name ?? 'Overview'}</strong></div>
				</PageToolbar>
				<div className="console-content"><SectionLayout maxWidth="1400px">{props.children}</SectionLayout></div>
			</main>
		</div>
	)

}

/** Loading is what a page shows while it is finding out who is asking. */
export function Loading(): React.ReactNode {
	return <main className="loading"><Skeleton width="100%" height={96} /><p>Loading your workspace</p></main>
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
			<Button onClick={props.onSignOut}>sign out</Button>
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
		<Section>
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
		</Section>
	)
}

/** Failed is a read that did not answer, drawn where the read was. */
export function Failed(props: { at: unknown }): React.ReactNode {
	return <p className="error">{props.at instanceof Error ? props.at.message : 'no'}</p>
}

function NavIcon({ name }: { name: string }): React.ReactNode {
	const paths: Record<string, string> = {
		tenants: 'M3 3h7v7H3zm11 0h7v7h-7zM3 14h7v7H3zm11 0h7v7h-7z',
		tenant: 'M4 21V7l8-4 8 4v14M9 21v-5h6v5M8 8h1m6 0h1M8 12h1m6 0h1',
		holders: 'M15 20v-2a5 5 0 0 0-10 0v2m15 0v-2a5 5 0 0 0-3-4M10 4a4 4 0 1 0 0 8 4 4 0 0 0 0-8m7 0a4 4 0 0 1 0 8',
		hosts: 'M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0M3 12h18M12 3c-5 5-5 13 0 18 5-5 5-13 0-18',
		connections: 'm9 15 6-6m-8 4-2 2a4 4 0 0 0 6 6l3-3m-4-12 3-3a4 4 0 0 1 6 6l-2 2',
		maildomains: 'M3 6h18v13H3zM3 6l9 7 9-7',
		apps: 'M4 4h6v6H4zm10 0h6v6h-6zM4 14h6v6H4zm10 0h6v6h-6z',
		sites: 'M4 21V5h10v16m0-11h6v11M7 9h4m-4 4h4m-4 4h4M2 21h20',
		groups: 'M3 8h7l2 3h9v10H3zM3 8V4h7l2 3h9v4',
		roles: 'M12 3 3 7v6c0 5 9 9 9 9s9-4 9-9V7zM8 12l3 3 5-6',
		trail: 'M5 3h14v18H5zM8 8h8m-8 4h8m-8 4h5',
		you: 'M19 21a7 7 0 0 0-14 0M12 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8',
	}
	return <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={paths[name] ?? paths.apps} /></svg>
}
