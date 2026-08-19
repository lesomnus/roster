// The browser half of `frontdoor`, for a page that draws its own sign-in.
//
// # What this is, and what D22 expected instead
//
// D22 asked for *headless components and a default theme*, written before
// either screen existed. Two now do -- `examples/sso/account.html`, which is a
// person's own page in plain HTML served by a Go app, and the console's
// `people.tsx`, which is an operator's in React over Connect -- and what they
// share turns out to be **none of their markup**. Different framework,
// different transport, different subject, and a component that fits both would
// fit by having no opinion left.
//
// What they do share is the part that is easy to get wrong: three answers where
// a page expects two, and a second form that must not be drawn from anything
// the server said to call it. So that is what is extracted -- and it is why
// this is one module and not a library. D24 §6's own reason held, it just
// answered smaller than it guessed: *extracting first means guessing what to
// extract.*
//
// # No build, on purpose
//
// Plain JavaScript with a `.d.ts` beside it, so the page that needs it most --
// one file of HTML with no toolchain, which `account.go` explains -- can import
// it as it is. A React app imports the same file.
//
// # It holds nothing
//
// No token, no continuation, no idea how many steps there are. The cookie the
// server set is the whole of the state, which is D21's split: *which browser is
// mid-sign-in* is the app's, in the app's process, and never in a variable
// here where script on the page could reach it.

/**
 * Sign in with a first factor.
 *
 * @param {{alias?: string, address?: string, password: string}} who
 * @param {{path?: string, signal?: AbortSignal}} [opts]
 * @returns {Promise<Answer>}
 */
export async function signIn(who, opts = {}) {
	const res = await fetch(opts.path ?? '/session', {
		method: 'POST',
		headers: { 'content-type': 'application/json' },
		body: JSON.stringify(who),
		signal: opts.signal,
	})

	return await read(res)
}

/**
 * Answer a second form.
 *
 * The kind comes from what {@link signIn} said was available, and the page
 * chose which -- never from a label the server sent, because the server does
 * not send one. Roster answers what is satisfied and what is available; what to
 * call it and which to offer are the app's, and D22 says to refuse the field
 * that would decide it here however small it looks.
 *
 * @param {{kind: string, name?: string, secret: string}} step
 * @param {{path?: string, signal?: AbortSignal}} [opts]
 * @returns {Promise<Answer>}
 */
export async function proceed(step, opts = {}) {
	const res = await fetch(opts.path ?? '/session/continue', {
		method: 'POST',
		headers: { 'content-type': 'application/json' },
		body: JSON.stringify(step),
		signal: opts.signal,
	})

	return await read(res)
}

/**
 * End the session, here and at roster.
 *
 * @param {{path?: string, signal?: AbortSignal}} [opts]
 * @returns {Promise<void>}
 */
export async function signOut(opts = {}) {
	await fetch(opts.path ?? '/session', { method: 'DELETE', signal: opts.signal })
}

/**
 * What a form got back.
 *
 * Three and not two, which is the whole reason this module exists. A page that
 * reads a boolean has already lost the third: `no` and `more` are different
 * events and the second is not a failure.
 *
 * @typedef {(
 *   | {state: 'in'}
 *   | {state: 'more', satisfied: string[], available: string[]}
 *   | {state: 'no'}
 *   | {state: 'broken', status: number}
 * )} Answer
 */

/**
 * read turns a status into one of the three, and it is deliberately not a
 * `res.ok` check.
 *
 * **204** signed in. **200** one factor proved and more to prove. **401**
 * everything else -- a wrong password, an unknown person, somebody with no
 * password at all -- which the server took care to make one answer, and a page
 * that told them apart would undo it.
 *
 * **Anything else is `broken` and not `no`.** A proxy answering 502 is not a
 * wrong password, and a page that said "no" to it sends somebody to type their
 * password again at a server that is down.
 *
 * @param {Response} res
 * @returns {Promise<Answer>}
 */
async function read(res) {
	if (res.status === 204) return { state: 'in' }
	if (res.status === 401) return { state: 'no' }

	if (res.status === 200) {
		const v = await res.json()

		return {
			state: 'more',
			satisfied: v.satisfied ?? [],
			available: v.available ?? [],
		}
	}

	return { state: 'broken', status: res.status }
}
