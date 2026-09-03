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
import { open, type WasmSock } from '@lesomnus/grpc-dgram/wasm'

/** Sandbox is a transport, and the instance answering it. */
export interface Sandbox {
	readonly transport: Transport

	/** The wasm instance, for a page that wants to take it down. */
	readonly sock: WasmSock

	/** close stops the server. A reload does the same thing more thoroughly. */
	close(): void
}

/**
 * start compiles the app into the page and answers with a transport for it.
 *
 * `url` is where the build landed:
 *
 *     GOOS=js GOARCH=wasm go build -o ts/public/app.wasm ./wasm
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
	url = '/app.wasm',
	workerUrl: URL | string = new URL('./sandbox-worker.ts', import.meta.url),
): Promise<Sandbox> {
	const sock = await open(url, { workerUrl: new URL(workerUrl, location.href) })

	return {
		transport: createDrpcTransport(sock.dial()),
		sock,
		close: () => sock.close(),
	}
}
