/**
 * The console's server, in the page.
 *
 * A reload is a fresh deployment: two new databases, `roster init` run again,
 * nothing left over. Somebody working on the console starts no backend,
 * migrates nothing, and does not have to remember what state they left it in.
 *
 * # It answers with a transport and nothing else
 *
 * Which is the whole reason a sandbox is worth having: everything above is
 * transport-blind, so the sandbox and a real server are the same code with a
 * different argument. Code that only ever ran against a fake is code that has
 * never run.
 *
 *	createDrpcTransport(sock.dial())        // sandbox
 *	createConnectTransport({ baseUrl })     // a real server
 *
 * # Signing in works, and the cookie does not
 *
 * `AuthService` is served for real and the password is checked by the same
 * `vouch`, so a wrong one is refused on screen. What cannot work is the cookie:
 * a message port has no browser cookie jar, so `set-cookie` reaches nobody.
 *
 * The server behind it is `auth.Plain`, so every call after the sign-in is
 * vouched for whether or not the cookie stuck — which is what lets this file
 * answer a transport and nothing above it know the difference. The cost is that
 * a wrong password refuses the sign-in and does not lock the rest of the page.
 * That is a sandbox being a sandbox; see `wasm/main.go`.
 *
 * # What used to be here
 *
 * The download, counted; the module compiled from one half of the split body
 * while the other half was counted; and the Cache API entry with its
 * `If-None-Match` revalidation, because a browser will not keep a
 * seventy-megabyte entry in its own HTTP cache whatever the server answers.
 * All of it is `@lesomnus/payday/sandbox` now, which is where it belonged —
 * every payday app's page has the same problem and would solve it the same
 * way. What is left here is the three things that are roster's: where the
 * build is served from under this page's base, the second entry point the
 * customers screen dials, and the name of the cache.
 *
 * # Two things will bite whoever serves this
 *
 * Neither is payday's to fix and both fail confusingly.
 *
 *   - **`Cross-Origin-Opener-Policy: same-origin` and
 *     `Cross-Origin-Embedder-Policy: require-corp`.** SQLite in a Worker
 *     cancels work with a `SharedArrayBuffer`, which does not exist without
 *     cross-origin isolation. The symptom is "it works on the other dev
 *     server". `vite.config.ts` sets both.
 *   - **`wasm_exec.js` is the toolchain's.** It is the JS half of the Go
 *     runtime and is version-coupled to the compiler that built the module, so
 *     a vendored copy pins the wrong one:
 *
 *         cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" ./public/
 *
 * @module
 */

import type { Transport } from '@connectrpc/connect'
import { createDrpcTransport } from '@lesomnus/grpc-dgram/transport/connect'
import type { WasmSock } from '@lesomnus/grpc-dgram/wasm'
import { start as opened, type Load } from '@lesomnus/payday/sandbox'

export type { Load }

/** Sandbox is a transport, and the instance answering it. */
export interface Sandbox {
	readonly transport: Transport

	/**
	 * dial is a transport to another server the same instance publishes, by
	 * the name it was published under -- `drpcAdmin` for the admin server,
	 * which the customers screen reaches beside the control one the way
	 * `admin.http` is dialed beside `control.http`. One instance, one pair of
	 * databases; see `wasm/main.go`.
	 */
	dial(entryPoint: string): Transport

	/** The wasm instance, for a page that wants to take it down. */
	readonly sock: WasmSock

	/** close stops the server. A reload does the same thing more thoroughly. */
	close(): void
}

/**
 * Progress is where starting the sandbox has got to, for a page to draw.
 *
 * `fetching` carries payday's own [Load], which is more than a fraction: where
 * the bytes are coming from, how fast, and whether they are being kept for the
 * next visit. `starting` is the tail payday cannot report -- the compile and
 * the instance coming up are one awaited call from out here -- and it begins
 * where the bar fills, which is what a full bar actually means.
 */
export interface Progress {
	stage: 'fetching' | 'starting' | 'ready'
	at?: Load
}

/**
 * start compiles the app into the page and answers with a transport for it.
 *
 * The build is `public/app.wasm`, which `npm run wasm` writes:
 *
 *     GOOS=js GOARCH=wasm go build -tags grpcnotrace -o ts/public/app.wasm ./wasm
 *
 * Everything is said under the page's **base**, because `vite.console.ts`
 * serves this page and `public/` with it: the package's defaults are the
 * origin's root, which is right while the base is `/` and is a 404 that reads
 * as "the sandbox never comes up" the moment a deployment moves it.
 *
 * # The worker is yours, and it has to be
 *
 * Two things must be in the **same realm** and neither package can put them
 * there for the other. `sqlite3-wasm-go` installs the global the Go driver
 * looks for; `@lesomnus/grpc-dgram` runs the module that looks for it.
 * Importing the driver on the main thread installs it in the wrong realm, and
 * what you get is the instance exiting with
 *
 *     sqlite3-wasm: globalThis["sqlite3-wasm-go"] is not installed
 *
 * which names the problem exactly and does not say that the answer is two lines
 * in a file of your own — `sandbox-worker.ts`, beside this one.
 */
export async function start(
	onProgress: (p: Progress) => void = () => {},
	workerUrl: URL | string = new URL('./sandbox-worker.ts', import.meta.url),
): Promise<Sandbox> {
	const base = import.meta.env.BASE_URL
	const box = await opened({
		url: base + 'app.wasm',
		worker: workerUrl,
		wasmExec: base + 'wasm_exec.js',

		// Named rather than defaulted, so the entry is this app's and reads as
		// this app's to somebody looking at storage.
		cache: 'roster-sandbox',

		onProgress: (at) => onProgress({ stage: at.total > 0 && at.loaded >= at.total ? 'starting' : 'fetching', at }),
	})
	onProgress({ stage: 'ready' })

	return {
		transport: box.transport,
		dial: (entryPoint) => createDrpcTransport(box.sock.dial({ entryPoint })),
		sock: box.sock,
		close: () => box.close(),
	}
}
