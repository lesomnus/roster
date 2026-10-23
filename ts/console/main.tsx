/**
 * Where the page starts.
 *
 * Two things happen before React does, and both have to: the transport is
 * decided, and the store is opened and filled from its mirror. Rendering over a
 * store that has not been hydrated is rendering a spinner for something the tab
 * already had.
 *
 * @module
 */

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { StrictMode, useState } from 'react'
import { createRoot } from 'react-dom/client'

import { Provider } from '@lesomnus/payday/react'
import type { App } from '@lesomnus/payday/react'
import { identityOf } from '@lesomnus/payday/store'

import { AuthService } from '#gen/app/auth_pb.js'
import { MeService } from '#gen/app/me_pb.js'
import { events } from '#gen/events.js'
import { caller, type Caller } from '#lib/caller.js'
import { writes, type Writes } from '#lib/client.js'
import { letGo, nobody, open, stillHere } from '#lib/store.js'
import { Page } from './page.js'
import type { Progress, Sandbox } from '#lib/sandbox.js'
import { go, useRoute } from '#lib/route.js'
// payday's panel, where this build has one; see `devtools.tsx`.
import { Devtools } from '#lib/devtools.js'
import { entities } from '#gen/entities.js'
import '#lib/style.css'

/**
 * Where the app answers.
 *
 * `npm run dev` is a different origin from the server, so the server has to say
 * this page may call it — `origins:` under `control.http`. A build served by
 * the app itself is same-origin and needs none of that.
 *
 * The **control** listener, which is what this console is: who runs the
 * customers, and where an operator signs in. Not `server.http`, which fronts
 * the walled data plane where an operator's session names nobody; and not
 * `control.http`, which serves the RPCs a shell makes and no page at all.
 *
 * **One origin for the whole page**, which #27 forced: the session is a
 * `__Host-` cookie, host-only, so a page and every listener it calls have to be
 * one host. The page is served by `admin.http` and calls it, and there is
 * nothing to configure.
 */
// Served by roster itself, the RPCs are on this page's own origin; under `npm
// run dev` they are wherever `VITE_ADDR` says, or the admin listener's usual
// port.
//
// `import.meta.env.DEV` and not the path. It used to ask whether the path began
// with `/console/`, which answered the question by accident: the admin console is at
// `/` now and there is nothing in an address to read. What was ever being asked
// is whether this was built for a deployment, and that is a constant the
// bundler substitutes rather than a guess about a URL.
const ADDR: string =
	import.meta.env['VITE_ADDR'] ?? (import.meta.env.DEV ? 'http://localhost:8081' : location.origin)

const root = createRoot(document.getElementById('root') as HTMLElement)

/**
 * The transport, and the only thing that changes between a real server and a
 * sandbox.
 *
 * `npm run dev:sandbox` compiles the whole server into the page: a reload is a
 * fresh deployment, no backend to start, nothing to migrate. Everything above
 * this line is transport-blind, which is what makes the sandbox worth having --
 * code that only ever ran against a fake is code that has never run.
 *
 * `credentials: 'include'` on every real call, or the browser neither receives
 * the cookie a sign-in sets nor sends it back — a login that works in `curl`
 * and does nothing here, with no error anywhere. The sandbox needs none of it;
 * see `sandbox.ts` for why the cookie cannot work over a message port and why
 * nothing above notices.
 */
async function connect(): Promise<Transport> {
	if (import.meta.env['VITE_SANDBOX'] === undefined) {
		calls = caller('console', held)

		return createConnectTransport({
			baseUrl: ADDR,
			fetch: (input, init) => fetch(input, { ...init, credentials: 'include' }),
			// What an event's `Any` holds, which Connect's JSON reads only with its
			// type registered (`ts/gen/events.ts`): empty while roster keeps no past.
			jsonOptions: { registry: events },
			interceptors: [calls.interceptor],
		})
	}

	const { start } = await import('#lib/sandbox.js')
	sandbox = await start(booting)

	return sandbox.transport
}

/**
 * booting draws where the sandbox has got to, because a hundred megabytes
 * take a while and a blank page for that while reads as a broken one. Drawn
 * straight into the root, since there is no app to provide yet.
 */
function booting(p: Progress): void {
	root.render(
		<StrictMode>
			<Booting at={p} />
		</StrictMode>,
	)
}

function Booting(props: { at: Progress }): React.ReactNode {
	const { stage, at } = props.at
	const mb = (n: number): string => (n / 1_000_000).toFixed(0)

	// Where the bytes are coming from, said out loud. Reading seventy megabytes
	// off a disk fills a bar exactly as downloading it does, and somebody
	// watching that a second time concludes the caching is broken.
	const from = at?.from === 'cache' ? 'the server, from the last visit' : 'downloading the server'
	const some = at === undefined ? '' : at.total > 0 ? `, ${mb(at.loaded)} of ${mb(at.total)} MB` : `, ${mb(at.loaded)} MB`
	const rate = at !== undefined && at.rate > 0 ? ` at ${mb(at.rate)} MB/s` : ''
	const line = {
		fetching: from + some + rate,
		starting: 'compiling, and starting the server in the page',
		ready: 'ready',
	}[stage]

	return (
		<main className="booting" aria-live="polite">
			<h1>roster</h1>
			<p>{line}</p>
			{stage === 'fetching' && at !== undefined && at.total > 0 ? <progress max={at.total} value={at.loaded} /> : <progress />}
			<p className="note">
				the sandbox: the whole server, compiled into this page. Nothing here leaves the browser, and a reload
				starts it over.
			</p>
			{at?.keeping === false && (
				<p className="note">
					this module is not being kept for the next visit, so every reload fetches it again. The Cache API
					needs a secure context — open this page at <code>localhost</code> rather than by address.
				</p>
			)}
		</main>
	)
}

// The sandbox, once started, for `tenants()` to dial its second server on.
let sandbox: Sandbox | null = null

/**
 * Whose store the page draws, and when it lets go of it (`caller.ts`): a
 * served page's, whose session is a cookie every tab shares. Null in the
 * sandbox, which has no cookie and whose server believes the page.
 */
let calls: Caller | null = null

/**
 * which is who the page draws for: the name the server gives the store of the
 * session this browser holds (`AuthService.Which`), or null for none.
 *
 * The sandbox has no session to name: its server believes whoever the page
 * says (`sandbox.ts`), so whether anybody is signed in is `Me.Get`'s to say,
 * and the store is named for the page, as it was before stores were named for
 * sessions.
 */
async function which(transport: Transport): Promise<string | null> {
	if (calls === null) {
		return createClient(MeService, transport)
			.get({})
			.then(
				() => identityOf('console'),
				() => null,
			)
	}

	return held()
}

/** held is `AuthService.Which` as the page reads it: a name, or null for nobody. */
async function held(): Promise<string | null> {
	const v = await auth.which({})

	return v.identity === '' ? null : v.identity
}

/**
 * Signing in is an **RPC**, like everything else this app offers.
 *
 * It was an HTTP endpoint, which made it the one call every client implemented
 * by reading a document rather than generating from the schema. It is
 * `AuthService` now and this file calls it with a generated client.
 *
 * What comes back is empty. The credential is a cookie the browser stores,
 * sends and never shows this page — script that can read one is script that can
 * send it somewhere else.
 */
let auth: Client<typeof AuthService>

function SignIn(props: { onDone: () => void; said?: string | undefined }): React.ReactNode {
	const [bad, setBad] = useState(false)

	return (
		<form
			className="sign-in"
			onSubmit={(e) => {
				e.preventDefault()

				const f = new FormData(e.currentTarget)
				void auth
					.signIn({
						alias: String(f.get('alias') ?? ''),
						password: String(f.get('password') ?? ''),
					})
					.then(() => props.onDone())
					.catch((e: unknown) => {
						// The form says "no" and nothing else, on purpose; the
						// reason goes where somebody developing this looks.
						console.error('sign-in refused:', e)
						setBad(true)
					})
			}}
		>
			<h1>roster</h1>
			{props.said !== undefined && (
				<p className="bad" role="alert">
					{props.said}
				</p>
			)}
			<label>
				operator
				<input name="alias" defaultValue="admin" autoFocus />
			</label>
			<label>
				password
				<input name="password" type="password" />
			</label>
			<button type="submit">sign in</button>

			{/* One answer however it was wrong. Which of "no such person",
			    "wrong password" and "locked" it was is an oracle, and the
			    lockout in `server/vouch` is what makes guessing expensive. */}
			{bad && <p className="bad">no</p>}

			<p>
				Run <code>roster init</code> first; it prints the operator and their
				password, once.
			</p>
		</form>
	)
}

/**
 * tenants is the store and the clients the tenants screen draws from.
 *
 * The **same** transport the rest of the page uses, because there is one
 * listener now: `admin.http` serves the page, signs the operator in, and
 * answers about tenants. It was a second transport to a second origin, which
 * a `__Host-` cookie could never have reached (#27).
 *
 * In the sandbox it is the second server the one instance publishes, dialed by
 * name -- the same databases, the same signed-in operator (`wasm/main.go`).
 *
 * The clients come back beside the store because not everything is a read of a
 * row: a reset answers with a secret that is never written down, so there is
 * nothing for the store to hold and nothing for it to redraw.
 */
async function tenants(transport: Transport, identity: string): Promise<{ app: App; writes: Writes } | null> {
	const at =
		import.meta.env['VITE_SANDBOX'] !== undefined ? (sandbox?.dial('drpcAdmin') ?? null) : transport
	if (at === null) return null

	// A store of its own beside the page's, for the same caller: its rows are
	// another plane's, and one store holding both would have them overwrite
	// each other by identifier. The same session names both, so the same
	// name for whose they are -- and the sandbox's, named for the page.
	const whose = calls === null ? await identityOf('console:admin') : identity

	return { app: await open(at, { name: 'roster-admin', identity: whose }, calls !== null), writes: writes(at) }
}

/**
 * Shell is what is on the screen either way -- the sign-in form or the page --
 * and, in a development build, payday's window over this store beneath both.
 * Beneath both on purpose: the window is for looking at what the page is
 * doing, and a page that is refusing a sign-in is doing something worth
 * looking at. The tenants screen mounts its own over its own store, so
 * this one steps aside there.
 */
function Shell(props: {
	signedIn: boolean
	onSignIn: () => void
	onSignOut: () => void
	tenants: App | null
	writes: Writes | null
	ungated: { control?: Transport; admin?: Transport }
	said?: string | undefined
}): React.ReactNode {
	const route = useRoute()
	const here = !(props.signedIn && route[0] === 'tenants')

	return (
		<>
			{props.signedIn ? (
				<Page
					onSignOut={props.onSignOut}
					tenants={props.tenants}
					writes={props.writes}
					ungated={props.ungated.admin}
				/>
			) : (
				<SignIn onDone={props.onSignIn} said={props.said} />
			)}
			{here && <Devtools {...(props.ungated.control !== undefined ? { ungated: props.ungated.control } : {})} />}
		</>
	)
}

/**
 * ungated is the two stacks with no wall, which only the sandbox has: the
 * server is inside the page there, so "past the wall" is the page looking at
 * its own memory (`wasm/main.go`). Against a served deployment there is no
 * such transport to dial, the panel is never handed one, and the switch is
 * not offered -- which is the guard, and the whole of it.
 */
function ungatedTransports(): { control?: Transport; admin?: Transport } {
	if (import.meta.env['VITE_SANDBOX'] === undefined || sandbox === null) return {}

	return { control: sandbox.dial('drpcUngated'), admin: sandbox.dial('drpcAdminUngated') }
}

async function run(transport: Transport): Promise<void> {
	const ungated = ungatedTransports()

	const draw = (
		app: App,
		signedIn: boolean,
		theirs: { app: App; writes: Writes } | null,
		out: () => void,
		said?: string,
	): void => {
		root.render(
			<StrictMode>
				<Provider app={app}>
					<Shell
						signedIn={signedIn}
						onSignIn={() => void boot()}
						onSignOut={out}
						tenants={theirs?.app ?? null}
						writes={theirs?.writes ?? null}
						ungated={ungated}
						said={said}
					/>
				</Provider>
			</StrictMode>,
		)
	}

	/**
	 * boot draws the page for whoever the browser's session is: the form for
	 * nobody, and otherwise their stores, opened and filled from the mirror
	 * before anything is drawn over them.
	 *
	 * A store is the caller's now, so it is opened here, once there is a
	 * caller, and let go of when there stops being one -- not opened once for
	 * the page's lifetime before anybody signed in, which made everybody who
	 * signed in on this browser one store. Asked again on a sign-in, on a
	 * sign-out in any tab, and when a call is refused for a session that is
	 * not this store's (`caller.ts`).
	 *
	 * `said` is what the form says first, when there is one to draw: that the
	 * sign-out before it could not clear this browser (`stillHere`).
	 */
	async function boot(said?: string): Promise<void> {
		let identity: string | null
		try {
			identity = await which(transport)
		} catch (err) {
			// A server that could not say is a sign-in like any other: what to
			// do about it is the person's.
			console.error('cannot tell who is signed in:', err)
			identity = null
		}
		if (identity === null) {
			calls?.drop()
			// The window a development build draws beneath the form has a
			// store to look at, and it is nobody's.
			return draw(nobody(transport), false, null, () => {}, said)
		}

		const app = await open(transport, { name: 'roster', identity }, calls !== null)
		// Opened beside it rather than inside the screen, so that a page which
		// never opens the tenants tab still pays for it once and a page that
		// does draws immediately. It is a store, not a call.
		const theirs = await tenants(transport, identity)

		/**
		 * Letting go of both stores: forgetting them first when their caller
		 * signed out -- the rows, the answers, the mirror, and the writes never
		 * sent, which are that caller's -- and in either case asking again whom
		 * to draw for, once both are closed. A forget that did not reach this
		 * browser's copy is said on the form, rather than left for the person
		 * to believe nothing of theirs is here.
		 */
		const leave = (forget: boolean): void => {
			const stores = theirs === null ? [app] : [app, theirs.app]
			void Promise.allSettled(stores.map((v) => letGo(v, forget))).then((vs) =>
				boot(vs.some((v) => v.status === 'rejected') ? stillHere : undefined),
			)
		}
		const hold = calls?.hold(identity, leave)

		/**
		 * Signing out deletes the row, which is the part that matters: the key
		 * is dead in every tab that had it, immediately, which is the thing a
		 * self-contained token cannot do -- and every other tab is told, and
		 * forgets what it held for this caller as this one does.
		 */
		const out = (): void => {
			void auth.signOut({}).finally(() => {
				// Back to the first screen, replacing rather than pushing: the
				// screens this session was on are not somewhere the next sign-in
				// should be able to step back into.
				go([], true)
				if (hold !== undefined) hold.out()
				else leave(true)
			})
		}

		draw(app, true, theirs, out)
	}

	// Who is signed in is the server's to say, and the cookie is the browser's
	// to send -- so a reload keeps the place the address bar names rather than
	// asking an operator who is still signed in to sign in again.
	void boot()
}

void connect().then((transport) => {
	auth = createClient(AuthService, transport)
	void run(transport)
})
