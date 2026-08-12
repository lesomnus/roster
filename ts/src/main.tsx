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

import { createConnectTransport } from '@connectrpc/connect-web'
import { StrictMode, useState } from 'react'
import { createRoot } from 'react-dom/client'

import { Provider } from '@lesomnus/payday/react'

import { open } from './store.js'
import { Page } from './page.js'
import './style.css'

/**
 * Where the app answers.
 *
 * `npm run dev` is a different origin from the server, so the server has to say
 * this page may call it -- `origins:` under `server.http`. A build served by
 * the app itself is same-origin and needs none of that.
 *
 * The **admin** listener's HTTP, which is `admin.http` and not `server.http`.
 * A console administers customers through the data plane with no wall, and the
 * transcoder in front of the walled data plane would answer its session with
 * nobody -- an operator is a holder of the control plane, and the two are
 * separate databases with no query between them.
 */
const ADDR = import.meta.env['VITE_ADDR'] ?? 'http://localhost:8081'

const root = createRoot(document.getElementById('root') as HTMLElement)

/**
 * Signing in, which is a **cookie** and not something this page can hold.
 *
 * A browser has nowhere safe to keep a credential -- script that can read one
 * is script that can send it somewhere else -- so what it gets is an opaque
 * cookie naming a session the server keeps. This page never sees it: the
 * browser stores it, sends it, and `credentials: 'include'` is the whole of
 * what this file has to say about it.
 *
 * Which is why there is no `localStorage` here any more. The scaffold this
 * replaced kept `@acme/admin` there and sent it as `auth.Plain`, which believes
 * whatever a caller writes -- right for a sandbox and not something to serve
 * where anyone can reach it.
 */
async function signIn(alias: string, password: string): Promise<boolean> {
	const res = await fetch(`${ADDR}/session`, {
		method: 'POST',
		headers: { 'content-type': 'application/json' },
		credentials: 'include',
		body: JSON.stringify({ alias, password }),
	})

	return res.status === 204
}

/**
 * Signing out deletes the row, which is the part that matters: the key is dead
 * in every tab that had it, immediately, which is the thing a self-contained
 * token cannot do.
 */
async function signOut(): Promise<void> {
	await fetch(`${ADDR}/session`, { method: 'DELETE', credentials: 'include' })
}

function SignIn(props: { onDone: () => void }): React.ReactNode {
	const [bad, setBad] = useState(false)

	return (
		<form
			className="sign-in"
			onSubmit={(e) => {
				e.preventDefault()

				const f = new FormData(e.currentTarget)
				const alias = String(f.get('alias') ?? '')
				const password = String(f.get('password') ?? '')

				void signIn(alias, password).then((ok) => {
					if (ok) props.onDone()
					else setBad(true)
				})
			}}
		>
			<h1>roster</h1>
			<label>
				operator
				<input name="alias" defaultValue="ops" autoFocus />
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

async function boot(): Promise<void> {
	const transport = createConnectTransport({
		baseUrl: ADDR,

		// On every call, or the browser neither receives the cookie a sign-in
		// sets nor sends it back -- a login that works in `curl` and does
		// nothing here, with no error anywhere.
		fetch: (input, init) => fetch(input, { ...init, credentials: 'include' }),
	})

	// Opened per **session** rather than per credential, because this page no
	// longer holds one: what it would key on is who the server says the caller
	// is, and that is a call away.
	const app = await open(transport, 'console')

	/**
	 * Signing out ends the session and drops this caller's copy -- the rows,
	 * the answers and the mirror. Nothing there is a secret, since the server
	 * only ever sent what that caller could see, but it is *that caller's*, and
	 * leaving it where the next one opens the same page is the kind of thing
	 * that looks like a leak whether or not it is one.
	 */
	const out = (): void => {
		void signOut().then(() => {
			app.store.forget()
			app.store.close()
			start()
		})
	}

	root.render(
		<StrictMode>
			<Provider app={app}>
				<Page onSignOut={out} />
			</Provider>
		</StrictMode>,
	)
}

function start(): void {
	root.render(
		<StrictMode>
			<SignIn onDone={() => void boot()} />
		</StrictMode>,
	)
}

start()
