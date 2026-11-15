import React from 'react'

/**
 * The second form of roster's own sign-in, for the two consoles.
 *
 * `AuthService.SignIn` answers a session, or -- for somebody who has more to
 * prove than a password -- what is left and a `continuation`, which a second
 * call carries with the next proof (`proto/app/auth.proto`). This is that
 * second call's form. It is not `#lib/signin.tsx`, which draws the same two
 * forms against a front door's own protocol and its three status codes.
 *
 * One attempt per first form: a wrong code spends the continuation, so what
 * follows it is the first form again, where the lockout counts.
 */

/** Left is what a first answer said is still to prove. */
export interface Left {
	continuation: string
	available: { kind: string; name: string }[]
}

/** left is `AuthSignInResponse` as a page reads it: more to prove, or null for signed in. */
export function left(v: { continuation: string; available: { kind: string; name: string }[] }): Left | null {
	if (v.continuation === '') return null

	return { continuation: v.continuation, available: v.available.map((f) => ({ kind: f.kind, name: f.name })) }
}

export function Second(props: {
	left: Left

	/** proceed makes the second call; the code as the bytes `secret` carries. */
	proceed: (kind: string, name: string, secret: Uint8Array) => void

	/** back is the first form again. */
	back: () => void
}): React.ReactNode {
	const totp = props.left.available.find((f) => f.kind === 'totp')

	// A security key is asked for by the page it was registered at, which is
	// the front door's and not this one: a key answers for one name.
	if (totp === undefined) {
		return (
			<section>
				<p className="bad" role="alert">
					This account asks for a second proof this page cannot ask for. Sign in
					through your organisation's own sign-in page.
				</p>
				<button type="button" onClick={props.back}>
					back
				</button>
			</section>
		)
	}

	return (
		<form
			onSubmit={(e) => {
				e.preventDefault()

				const f = new FormData(e.currentTarget)
				props.proceed(totp.kind, totp.name, new TextEncoder().encode(String(f.get('code') ?? '').trim()))
			}}
		>
			<p className="note">one more: a code from your authenticator app</p>
			<label>
				code
				<input name="code" inputMode="numeric" autoComplete="one-time-code" autoFocus />
			</label>
			<button type="submit">sign in</button>
		</form>
	)
}
