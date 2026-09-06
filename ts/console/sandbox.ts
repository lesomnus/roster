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

/** Progress is where starting the sandbox has got to, for a page to draw. */
export interface Progress {
	stage: 'downloading' | 'cached' | 'compiling' | 'starting' | 'ready'
	/** Bytes so far and in all, while downloading; `total` is 0 when the server did not say. */
	loaded: number
	total: number
}

/** Where the module is kept between visits; one entry, keyed by its URL. */
const cacheName = 'roster-sandbox'

/**
 * load fetches the module and compiles it as it arrives, saying how far the
 * download has got -- and keeps it, so the next visit does not download it.
 *
 * The build is a hundred megabytes -- a whole server, its ORM and SQLite --
 * and `open(url)` would fetch it in silence, which on the first visit reads
 * as a page that does not work. So the page fetches it itself: the body is
 * split, one half counted, the other handed to the compiler while it is
 * still arriving, and what `open` is given is the compiled module rather
 * than the address. A module crosses to the worker by structured clone, so
 * nothing is downloaded twice.
 *
 * # Why the browser's own cache is not enough
 *
 * The dev server answers a revalidation with 304 and the browser never asks:
 * Chrome will not keep an entry this large in its HTTP cache (a single entry
 * is capped at a fraction of the cache), so every reload was the whole
 * download again. The Cache API has the origin's quota instead of that cap,
 * so the module goes there, and the next visit sends what it holds as
 * `If-None-Match` (or `If-Modified-Since`, for a server that gives no ETag)
 * and takes the 304 as "use what you have". A rebuilt module changes both,
 * and is fetched. Where there is no Cache API -- a page opened over plain
 * http by IP rather than `localhost` is not a secure context -- it is the
 * download every time, as before.
 */
async function load(url: string, onProgress: (p: Progress) => void): Promise<WebAssembly.Module> {
	const store = await opened()
	const had = store !== undefined ? await store.match(url) : undefined

	const ask = new Headers()
	const etag = had?.headers.get('etag')
	const since = had?.headers.get('last-modified')
	if (etag !== null && etag !== undefined) ask.set('if-none-match', etag)
	else if (since !== null && since !== undefined) ask.set('if-modified-since', since)

	// `no-store`: the browser's cache is not asked to hold this, since it
	// would not, and the revalidation is this code's rather than its.
	let res = await fetch(url, { headers: ask, cache: 'no-store' })
	let counted: ReadableStream<Uint8Array>
	let compiled: ReadableStream<Uint8Array>
	let total: number

	if (res.status === 304 && had !== undefined) {
		if (had.body === null) throw new Error(`${url}: the kept copy has no body`)
		total = Number(had.headers.get('content-length') ?? 0)
		onProgress({ stage: 'cached', loaded: 0, total })
		;[counted, compiled] = had.body.tee()
	} else {
		if (!res.ok || res.body === null) throw new Error(`${url}: ${res.status} ${res.statusText}`)
		total = Number(res.headers.get('content-length') ?? 0)
		if (store !== undefined) {
			// A third reader, for the copy kept: written as it arrives, and
			// not awaited -- a visit is not slower for keeping it.
			const [keep, rest] = res.body.tee()
			void store.put(url, new Response(keep, { status: 200, headers: res.headers })).catch(() => {})
			res = new Response(rest, { status: 200, headers: res.headers })
		}
		onProgress({ stage: 'downloading', loaded: 0, total })
		;[counted, compiled] = res.body!.tee()
	}

	let loaded = 0
	const stage = res.status === 304 ? 'cached' : 'downloading'
	const counting = (async (): Promise<void> => {
		const reader = counted.getReader()
		for (;;) {
			const { done, value } = await reader.read()
			if (done) return
			loaded += value.byteLength
			onProgress({ stage, loaded, total })
		}
	})()

	// `compileStreaming` insists on the content type, and a Response built
	// from a stream has none until told.
	const mod = await WebAssembly.compileStreaming(
		new Response(compiled, { headers: { 'content-type': 'application/wasm' } }),
	)
	await counting
	onProgress({ stage: 'compiling', loaded, total })

	return mod
}

// opened is the Cache API's store for this, or nothing where there is none.
async function opened(): Promise<Cache | undefined> {
	if (!('caches' in globalThis)) return undefined
	try {
		return await caches.open(cacheName)
	} catch {
		return undefined
	}
}

/**
 * start compiles the app into the page and answers with a transport for it.
 *
 * The build is `public/app.wasm`, which `npm run wasm` writes:
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
	onProgress: (p: Progress) => void = () => {},
	workerUrl: URL | string = new URL('./sandbox-worker.ts', import.meta.url),
): Promise<Sandbox> {
	const name = 'app.wasm'
	// Under the page's base rather than at the root: `vite.console.ts` serves
	// this page at `/console/`, and `public/` with it, so `/app.wasm` is a
	// 404 that reads as "the sandbox never comes up". The package's default
	// for `wasm_exec.js` is the root too, so it is said here as well.
	const base = import.meta.env.BASE_URL
	const app = await load(base + name, onProgress)
	onProgress({ stage: 'starting', loaded: 0, total: 0 })
	const sock = await open(app, {
		workerUrl: new URL(workerUrl, location.href),
		wasmExec: base + 'wasm_exec.js',
	})
	onProgress({ stage: 'ready', loaded: 0, total: 0 })

	return {
		transport: createDrpcTransport(sock.dial()),
		dial: (entryPoint) => createDrpcTransport(sock.dial({ entryPoint })),
		sock,
		close: () => sock.close(),
	}
}
