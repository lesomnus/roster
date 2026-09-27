/**
 * Holds the one rule about import paths: nothing reaches upward.
 *
 * A specifier starting with `./` is fine -- a file next to you is a file you may
 * name that way. One starting with `../` is not: it says where the importer
 * happens to sit rather than what it wants, so moving either file rewrites a path
 * that was never about either of them. `#lib/...` and `#gen/...` are the whole of
 * what crosses a directory here, declared once in `package.json`'s `imports` and
 * read from there by `tsc`, by `vite build` and by the dev server alike.
 *
 * Why a check and not a convention: a `../..` that creeps back in still compiles,
 * still builds and still runs. There would be nothing to notice until the two
 * spellings were mixed and the rule was no longer true of anything.
 *
 * `gen/` is not read. It is `pd gen`'s output -- it reaches upward nowhere today,
 * but its shape is the generator's to decide, and a gate that cannot be fixed from
 * here is a gate that teaches people to ignore it.
 */

import { readdir, readFile } from 'node:fs/promises'
import { join } from 'node:path'

// Every directory here, found rather than listed.
//
// Listed is how `tsconfig.json` came to leave `user` and `login` out of its
// `include` with nothing to say so, and a check that had to be remembered when a
// page is added is a check that is one page behind. `gen/` is the generator's;
// the other three hold nothing to read.
const never = new Set(['gen', 'node_modules', 'dist', 'public'])

// All three spellings that name a module: `from '...'`, the bare `import '...'`
// that a stylesheet arrives as, and `import('...')`.
//
// The third is why this reads the text rather than only the import block at the
// top of a file: `main.tsx` reaches the sandbox with an `await import()` halfway
// down, so a check that read declarations would have passed the two files that
// most needed reading. `import.meta` does not match -- there is no `(`.
const specifier = /(?:\bfrom\s+|^\s*import\s+|\bimport\s*\(\s*)'([^']+)'/gm

async function walk(dir) {
	const out = []
	for (const v of await readdir(dir, { withFileTypes: true })) {
		const at = join(dir, v.name)
		if (v.isDirectory()) out.push(...(await walk(at)))
		else if (/\.tsx?$/.test(v.name)) out.push(at)
	}
	return out
}

const roots = (await readdir('.', { withFileTypes: true }))
	.filter((v) => v.isDirectory() && !v.name.startsWith('.') && !never.has(v.name))
	.map((v) => v.name)

const found = []
for (const root of roots.sort()) {
	for (const file of await walk(root)) {
		const body = await readFile(file, 'utf8')
		for (const m of body.matchAll(specifier)) {
			if (m[1].startsWith('../')) found.push([file, m[1]])
		}
	}
}

if (found.length > 0) {
	console.error(`${found.length} import(s) reach upward; say '#lib/...' or '#gen/...' instead:`)
	for (const [file, v] of found) console.error(`   ${file}: ${v}`)
	process.exit(1)
}
