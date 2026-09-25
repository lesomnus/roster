/**
 * Where the user console starts.
 *
 * The same two things happen before React does as on the admin console, and
 * both have to: the transport is decided, and the store is opened and filled
 * from its mirror. What differs is which server is at the other end, and that
 * is the whole of the difference between the two pages.
 *
 * @module
 */

import { createClient, type Client, type Transport } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { StrictMode, useState } from 'react'
import { createRoot } from 'react-dom/client'

import { Provider } from '@lesomnus/payday/react'
import type { App } from '@lesomnus/payday/react'

import { AuthService } from '../gen/app/auth_pb.js'
import { MeService } from '../gen/app/me_pb.js'
import { writes, type Writes } from '../lib/client.js'
import { Devtools } from '../lib/devtools.js'
import { go } from '../lib/route.js'
import type { Progress, Sandbox } from '../lib/sandbox.js'
import { open } from '../lib/store.js'
import { Page } from './page.js'
import '../lib/style.css'

/**
 * Where the app answers, which is **this page's own origin** and cannot be
 * anything else.
 *
 * Two things pin it there, and they are the same two that put the admin console
 * on `admin.http`. A session is a `__Host-` cookie, host-only, so the page and
 * the listener it signs in at have to be one host. And which tenant a sign-in
 * is about is the name the browser arrived at (`cmd.Hosted`) -- so the address
 * in the bar is not merely where the calls go, it is *who this page is for*.
 * A configured address would be a page at one name asking about a tenant at
 * another.
 *
 * `npm run dev:user` is a different origin from the server, so a development
 * deployment has to say this page may call it -- `origins:` under `server.http`
 * -- and the tenant it resolves is whoever claims the **server's** name, not
 * the dev server's.
 */
const ADDR: string =
	import.meta.env['VITE_ADDR'] ?? (import.meta.env.DEV ? 'http://localhost:8080' : location.origin)

const root = createRoot(document.getElementById('root') as HTMLElement)

/**
 * The transport, and the only thing that changes between a real server and a
 * sandbox.
 *
 * `npm run dev:user:sandbox` compiles the whole server into the page: a reload
 * is a fresh deployment, no backend to start, nothing to migrate. Everything
 * above this line is transport-blind, which is what makes the sandbox worth
 * having -- code that only ever ran against a fake is code that has never run.
 *
 * What the sandbox fakes is two things and neither is on this side of the line:
 * the cookie, because a message port has no cookie jar, and the name this page
 * arrived at, because a message port has no `Host` either. Both are answered by
 * the instance (`wasm/sandbox`), so nothing here has a branch for them.
 *
 * `credentials: 'include'` on every real call, or the browser neither receives
 * the cookie a sign-in sets nor sends it back — a login that works in `curl`
 * and does nothing here, with no error anywhere.
 */
async function connect(): Promise<Transport> {
	if (import.meta.env['VITE_SANDBOX'] === undefined) {
		return createConnectTransport({
			baseUrl: ADDR,
			fetch: (input, init) => fetch(input, { ...init, credentials: 'include' }),
		})
	}

	const { start } = await import('../lib/sandbox.js')
	sandbox = await start(booting)

	// The **walled** data plane, which is what a deployment serves on
	// `server.http` and the only server this page talks to. The default entry
	// point is the control plane, where a roster user has no row at all.
	return sandbox.dial('drpcUser')
}

/**
 * booting draws where the sandbox has got to, because a hundred megabytes
 * take a while and a blank page for that while reads as a broken one.
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

// The sandbox, once started, for the unwalled transport the panel offers.
let sandbox: Sandbox | null = null

/**
 * Signing in is an **RPC**, like everything else this app offers, and what
 * comes back is empty: the credential is a cookie the browser stores, sends and
 * never shows this page.
 *
 * The form asks for an alias and no tenant. There is nowhere for a tenant field
 * to be answered from that would be better than the name already in the address
 * bar, and one added would be a caller naming the tenant it would like to be
 * checked against — see `proto/app/auth.proto`.
 */
let auth: Client<typeof AuthService>

function SignIn(props: { onDone: () => void }): React.ReactNode {
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
			<label>
				who
				<input name="alias" autoFocus />
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
				The organisation is the name this page is at, so there is nothing
				to pick.
			</p>
		</form>
	)
}

/**
 * ungated is this plane with no wall, which only the sandbox has: the server is
 * inside the page there, so "past the wall" is the page looking at its own
 * memory (`wasm/main.go`). Against a served deployment there is no such
 * transport to dial, the panel is never handed one, and the switch is not
 * offered -- which is the guard, and the whole of it.
 */
function ungatedTransport(): Transport | undefined {
	if (import.meta.env['VITE_SANDBOX'] === undefined || sandbox === null) return undefined

	return sandbox.dial('drpcUserUngated')
}

async function run(transport: Transport): Promise<void> {
	// Opened once, for the page's lifetime, and before anybody has signed in.
	// A store is a local thing: what it would key on is who the server says the
	// caller is, and that is answered call by call.
	//
	// `user` and not `console`, which is not tidiness: the two pages are two
	// origins in a deployment and one origin in a sandbox, and a `roster.Holder`
	// means a roster operator on one and a roster user on the other. One store
	// name would have them overwrite each other by identifier, which is the
	// shape of a bug rather than a cache -- the same sentence `customers.tsx`
	// makes about its second store.
	const app = await open(transport, 'user')
	const ungated = ungatedTransport()

	const render = (signedIn: boolean): void => {
		root.render(
			<StrictMode>
				<Provider app={app}>
					<Shell
						signedIn={signedIn}
						onSignIn={() => render(true)}
						onSignOut={out}
						writes={writes(transport)}
						{...(ungated !== undefined ? { ungated } : {})}
					/>
				</Provider>
			</StrictMode>,
		)
	}

	/**
	 * Signing out deletes the row, which is the part that matters: the key is
	 * dead in every tab that had it, immediately.
	 *
	 * And it drops this caller's copy — the rows, the answers, the mirror.
	 * Nothing there is a secret, since the server only ever sent what that
	 * caller could see, but it is *that caller's*.
	 */
	const out = (): void => {
		void auth.signOut({}).finally(() => {
			app.store.forget()
			go([], true)
			render(false)
		})
	}

	// Whether there is a session is `Me.Get`'s to say: it answers who, or is
	// refused, and the cookie is the browser's to send. So a reload keeps the
	// place the address bar names rather than asking somebody who is still
	// signed in to sign in again.
	void createClient(MeService, transport)
		.get({})
		.then(() => render(true))
		.catch(() => render(false))
}

/**
 * Shell is what is on the screen either way -- the sign-in form or the page --
 * and, in a development build, payday's window over this store beneath both.
 * Beneath both on purpose: the window is for looking at what the page is doing,
 * and a page that is refusing a sign-in is doing something worth looking at.
 */
function Shell(props: {
	signedIn: boolean
	onSignIn: () => void
	onSignOut: () => void
	writes: Writes
	ungated?: Transport | undefined
}): React.ReactNode {
	return (
		<>
			{props.signedIn ? (
				<Page onSignOut={props.onSignOut} writes={props.writes} />
			) : (
				<SignIn onDone={props.onSignIn} />
			)}
			<Devtools {...(props.ungated !== undefined ? { ungated: props.ungated } : {})} />
		</>
	)
}

void connect().then((transport) => {
	auth = createClient(AuthService, transport)
	void run(transport)
})
