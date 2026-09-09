import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import type { Connect } from 'vite'

// The Login App's page: what `roster login serve` serves from `login.page.dir`.
//
// # `npm run dev:login` has no backend at all
//
// Not a proxy, unlike `vite.account.ts`. What this app is, is four screens and
// the order they come in -- the calls behind each are two lines -- so the thing
// worth having in front of you while you change one is the **screens**, and
// standing Hydra and roster and a customer up to see a form is four containers
// for a change to a label.
//
// So the middleware below is the app, made up: a password in a map, two people,
// one of them with a second factor. It answers the same three status codes
// `frontdoor` does, because that is the part a page can get wrong.
//
// It is the **opposite** of `dev:sandbox`, which compiles the real server into
// the page, and both are right for what they are: the console *is* calls, so a
// fake server there would be a fake answer to every question it exists to ask.
//
// # It is not a test double
//
// Nothing is asserted through it and nothing in a deployment imports it. A fake
// that tests pass against is a fake that has to stay true, and this one says
// `ok` to a password it has in a map. What keeps the real flow honest is
// `login/login_test.go` against a fake **Hydra** with a real roster, and
// `scripts/hydra.sh` against both.

/** Somebody to sign in as, and what it takes. */
const people = [
	{ alias: 'erin', password: 'correct horse battery staple', factor: '' },
	{ alias: 'frank', password: 'correct horse battery staple', factor: '123456' },
]

/** One browser's place in the walk, by the cookie this hands out. */
const flows = new Map<string, { who: string; proved: string[] }>()

function key(req: Connect.IncomingMessage, res: import('node:http').ServerResponse): string {
	const had = /(?:^|;\s*)sandbox=([^;]+)/.exec(req.headers.cookie ?? '')?.[1]
	if (had !== undefined) return had

	const made = Math.random().toString(36).slice(2)
	res.setHeader('set-cookie', `sandbox=${made}; Path=/; HttpOnly; SameSite=Lax`)

	return made
}

function body(req: Connect.IncomingMessage): Promise<string> {
	return new Promise((done) => {
		let v = ''
		req.on('data', (c: Buffer) => (v += c.toString()))
		req.on('end', () => done(v))
	})
}

function json(res: import('node:http').ServerResponse, code: number, v: unknown): void {
	res.statusCode = code
	res.setHeader('content-type', 'application/json')
	res.end(JSON.stringify(v))
}

const sandbox = (): Connect.NextHandleFunction => async (req, res, next) => {
	const url = new URL(req.url ?? '/', 'http://localhost')
	const at = flows.get(key(req, res)) ?? { who: '', proved: [] }
	flows.set(key(req, res), at)

	const whole = (): boolean => {
		const p = people.find((v) => v.alias === at.who)

		return p !== undefined && (p.factor === '' || at.proved.includes('totp'))
	}

	// The two screens are the same page: vite serves `index.html` at the root,
	// and the app reads which it is from the challenge in the URL.
	if (url.pathname === '/login' || url.pathname === '/consent') {
		if (req.method === 'GET') {
			req.url = '/'

			return next()
		}
	}

	switch (`${req.method} ${url.pathname}`) {
		case 'GET /flow':
			return json(res, 200, { brand: 'Contoso', client: 'the demo product', scope: ['openid', 'profile', 'email'] })

		case 'POST /session': {
			const v = JSON.parse((await body(req)) || '{}') as { alias?: string; password?: string }
			const p = people.find((x) => x.alias === (v.alias ?? '').trim().toLowerCase())
			if (p === undefined || p.password !== v.password) {
				// One answer for an unknown person and a wrong password, which
				// is the rule the real one keeps.
				res.statusCode = 401

				return res.end('no')
			}
			at.who = p.alias
			at.proved = ['password']
			if (p.factor === '') {
				res.statusCode = 204

				return res.end()
			}

			return json(res, 200, { satisfied: ['password'], factors: [{ kind: 'totp', name: '' }] })
		}

		case 'POST /session/continue': {
			const v = JSON.parse((await body(req)) || '{}') as { secret?: string }
			const p = people.find((x) => x.alias === at.who)
			if (p === undefined || p.factor !== (v.secret ?? '').trim()) {
				res.statusCode = 401

				return res.end('no')
			}
			at.proved.push('totp')
			res.statusCode = 204

			return res.end()
		}

		case 'POST /accept': {
			if (at.who === '' || !whole()) {
				res.statusCode = 401

				return res.end('no')
			}

			return json(res, 200, { redirect_to: '/consent?consent_challenge=sandbox' })
		}

		case 'POST /consent': {
			const v = new URLSearchParams(await body(req))
			flows.delete(key(req, res))
			res.statusCode = 303
			res.setHeader('location', v.get('allow') === null ? '/?denied' : '/?allowed')

			return res.end()
		}
	}

	return next()
}

export default defineConfig({
	root: 'login',
	publicDir: false,
	build: { outDir: '../dist/login', emptyOutDir: true },
	plugins: [
		react(),
		{
			name: 'roster-login-sandbox',
			configureServer(server) {
				server.middlewares.use(sandbox())
			},
		},
	],
	server: {
		// Every interface, for `vite.console.ts`'s reason: a container.
		host: true,
	},
})
