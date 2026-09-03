/**
 * What both UIs share about permissions: the three comparisons the server
 * makes, so a page can decide what is worth drawing.
 *
 * @module
 */

/**
 * covers is `frame.Covers` in the browser: three parts, each `*` or a name.
 *
 * The same three comparisons the server makes, because `MeService` answers with
 * **patterns** rather than an expansion — an expansion would be the methods
 * that exist in whichever replica answered, and during a rolling deploy two of
 * them would tell this page two different things about one person.
 */
export function covers(held: string, want: string): boolean {
	if (held === want) return true

	const h = parts(held)
	const w = parts(want)
	if (h === null || w === null) return false

	return h.every((v, i) => v === '*' || v === w[i])
}

function parts(v: string): [string, string, string] | null {
	if (!v.startsWith('/')) return null

	const i = v.indexOf('/', 1)
	if (i < 0) return null

	const full = v.slice(1, i)
	const method = v.slice(i + 1)
	if (full === '' || method === '' || method.includes('/')) return null

	// The **last** dot, because a package has dots in it: split at the first,
	// `/google.protobuf.Any/Pack` is package "google".
	const j = full.lastIndexOf('.')
	if (j < 0) return null

	const pkg = full.slice(0, j)
	const service = full.slice(j + 1)
	if (pkg === '' || service === '') return null

	return [pkg, service, method]
}
