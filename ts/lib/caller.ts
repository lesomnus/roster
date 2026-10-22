/**
 * Whose store a page draws, and when it lets go of it.
 *
 * A page signed in by a cookie keeps what it reads in a store named for its
 * caller (`store.ts`), and every call the store's queries make says that name.
 * The cookie is every tab's, so whoever signed in last, in any tab, is who
 * every page of the origin sends as -- and the server refuses a call the
 * cookie's session is not that store's caller for, so a page left holding one
 * person's store never reads the next person's rows into it, or sends what it
 * kept for them as somebody else. What is left to the page is noticing, and
 * this is that, for every page that has a session:
 *
 *   - A call refused `Unauthenticated` may be a session that ended, or one
 *     that is somebody else's now. The page asks again who is signed in, and
 *     if it is not this store's caller it lets go of the store -- and does not
 *     forget it: a session that idled out is the same person once they sign in
 *     again, the same store, and what they wrote meanwhile goes then.
 *   - Signing out, in any tab, is said to every other on a `BroadcastChannel`,
 *     and each lets go -- forgetting what it held when it held that person's:
 *     the rows, the answers, and the writes they never sent.
 *
 * payday's `docs/migrating.md`, *A page's store is named by what it sees*, and
 * the page `pd new` writes (`ts/src/session.tsx`), are where the shape is from.
 *
 * @module
 */

import { Code, ConnectError, type Interceptor } from '@connectrpc/connect'

/** Held is what a page draws a store for: its caller, and how it lets go. */
export interface Held {
	/** The store's caller, as the server names it. */
	readonly identity: string

	/** out is a sign-out, once the server has ended the session: said to every other tab, and forgotten here. */
	out(): void
}

/** Caller is one page's hold on whose store it draws. */
export interface Caller {
	/**
	 * interceptor is what the page's transport is built with, so that a call
	 * refused for its session asks who is signed in now.
	 */
	readonly interceptor: Interceptor

	/**
	 * hold says the page draws `identity`'s store now, and how it lets go of it:
	 * `leave(true)` forgets the store first, on a sign-out; `leave(false)` only
	 * closes it. Either way the page then asks again who it is drawing for.
	 * Once, however many times it is asked to.
	 */
	hold(identity: string, leave: (forget: boolean) => void): Held

	/** drop says the page draws nobody's store, which a sign-in form is. */
	drop(): void
}

/**
 * caller is the hold for one page, by its name -- `console`, `user`,
 * `account` -- and `which`, how it asks the server which session the browser
 * holds: the name of its store, or null for none.
 */
export function caller(page: string, which: () => Promise<string | null>): Caller {
	const signedOut = new BroadcastChannel(`roster/${page}/signed-out`)
	let drawn: { identity: string; leave: (forget: boolean) => void } | undefined

	signedOut.onmessage = (e: MessageEvent<string>): void => {
		// Signed out somewhere, which ends the session every tab shares: let go,
		// and forget only what was that person's.
		drawn?.leave(e.data === drawn.identity)
	}

	// A call refused for its session: let go if the browser's session is not
	// this store's caller's any more. One that still is was refused for
	// something else, and drawing the page again would only lose what is on it.
	// The store asked about is the one drawn when the refusal came back, so a
	// late answer to a store already let go of costs one question.
	//
	// And one question at a time. Refusals come in bunches -- every query a
	// screen draws -- and the question is itself a call through this
	// interceptor, so a server refusing everything would otherwise be asked
	// again by the refusal of each answer, for as long as it went on refusing.
	let asking = false
	const lost = (): void => {
		const at = drawn
		if (at === undefined || asking) return

		asking = true
		void which()
			.then(
				(held) => {
					if (held !== at.identity) at.leave(false)
				},
				() => {},
			)
			.finally(() => {
				asking = false
			})
	}

	return {
		interceptor: (next) => async (req) => {
			try {
				return await next(req)
			} catch (err) {
				if (ConnectError.from(err).code === Code.Unauthenticated) lost()
				throw err
			}
		},

		hold(identity, leave) {
			let left = false
			const once = (forget: boolean): void => {
				if (left) return
				left = true
				if (drawn?.leave === once) drawn = undefined
				leave(forget)
			}
			drawn = { identity, leave: once }

			return {
				identity,
				out: () => {
					signedOut.postMessage(identity)
					once(true)
				},
			}
		},

		drop() {
			drawn = undefined
		},
	}
}
