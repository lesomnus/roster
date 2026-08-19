// The types for `frontdoor.js`, which is plain JavaScript so that a page with
// no toolchain can import it. See the note at the top of that file.

export type Answer =
	| { state: 'in' }
	| { state: 'more'; satisfied: string[]; available: string[] }
	| { state: 'no' }
	| { state: 'broken'; status: number }

export interface Options {
	path?: string
	signal?: AbortSignal
}

export function signIn(
	who: { alias?: string; address?: string; password: string },
	opts?: Options,
): Promise<Answer>

export function proceed(
	step: { kind: string; name?: string; secret: string },
	opts?: Options,
): Promise<Answer>

export function signOut(opts?: Options): Promise<void>
