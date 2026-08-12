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

import { createClient } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { StrictMode, useState } from 'react'
import { createRoot } from 'react-dom/client'

import { Provider } from '@lesomnus/payday/react'

import { AuthService } from '../gen/app/auth_pb.js'
import { open } from './store.js'
import { Page } from './page.js'
import './style.css'

/**
 * Where the app answers.
 *
 * `npm run dev` is a different origin from the server, so the server has to say
 * this page may call it — `origins:` under `control.http`. A build served by
 * the app itself is same-origin and needs none of that.
 *
 * The **control** listener, which is what this console is: who runs the
 * deployment, which services call it, what each key may do. Not `server.http`,
 * which fronts the walled data plane where an operator's session names nobody;
 * and not `admin.http`, which reaches customers and is a later screen.
 */
const ADDR = import.meta.env['VITE_ADDR'] ?? 'http://localhost:8082'

const root = createRoot(document.getElementById('root') as HTMLElement)

/**
 * The transport, and the only thing that changes between a real server and a
 * sandbox.
 *
 * `credentials: 'include'` on every call, or the browser neither receives the
 * cookie a sign-in sets nor sends it back — a login that works in `curl` and
 * does nothing here, with no error anywhere.
 */
const transport = createConnectTransport({
	baseUrl: ADDR,
	fetch: (input, init) => fetch(input, { ...init, credentials: 'include' }),
})

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
const auth = createClient(AuthService, transport)

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
					.catch(() => setBad(true))
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
	// Opened per **session** rather than per credential, because this page holds
	// none: what it would key on is who the server says the caller is, and that
	// is a call away.
	const app = await open(transport, 'console')

	/**
	 * Signing out deletes the row, which is the part that matters: the key is
	 * dead in every tab that had it, immediately, which is the thing a
	 * self-contained token cannot do.
	 *
	 * And it drops this caller's copy — the rows, the answers, the mirror.
	 * Nothing there is a secret, since the server only ever sent what that
	 * caller could see, but it is *that caller's*.
	 */
	const out = (): void => {
		void auth.signOut({}).finally(() => {
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
