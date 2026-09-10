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
// one of them with a second factor, two providers. It answers the same three
// status codes `frontdoor` does, because that is the part a page can get wrong.
//
// The one hop it cannot have is the trip to a directory, which leaves this
// origin -- see `away`, and the shape it exists to stop teaching.
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

/**
 * The operator's `Connection` rows, as the app would have read them.
 *
 * The round trip behind a button is a real one -- the browser leaves for a
 * directory and comes back to `/callback` -- so there is nothing here to
 * pretend with short of standing an OIDC provider up. What stands in is [away]:
 * a page that says which hop is missing, and comes back. What it cannot show is
 * the directory's own screen, and nothing here claims to.
 */
const providers = [{ name: 'entra' }, { name: 'github' }]

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

/**
 * away is the trip to a directory, drawn rather than taken.
 *
 * Deliberately not a screen of this app's: no shared stylesheet, no component,
 * nothing that could be mistaken for something a deployment serves. What it has
 * to be is honest about which hop is missing -- and unattended, because
 * somebody opening `dev:login` to look at a form should not have to read a
 * button to get past the one screen that is not a form.
 */
function away(res: import('node:http').ServerResponse, connection: string): void {
	res.statusCode = 200
	res.setHeader('content-type', 'text/html; charset=utf-8')
	res.end(`<!doctype html>
<meta charset="utf-8">
<title>${connection} \u2014 the sandbox</title>
<style>
  body { font: 15px/1.6 system-ui, sans-serif; margin: 0; display: grid; place-items: center;
         min-height: 100vh; background: #f6f6f7; color: #222 }
  main { max-width: 30rem; padding: 2rem; text-align: center }
  code { background: #e9e9ec; padding: .1em .35em; border-radius: .2em }
  p.note { color: #666 }
  button { font: inherit; padding: .5em 1.2em; margin-top: 1rem; cursor: pointer }
</style>
<main>
  <h1>off to <code>${connection}</code></h1>
  <p>A real deployment leaves this origin here, for the operator\u2019s directory,
     and comes back to <code>/callback</code> with a code. This is where you
     would type your work account.</p>
  <p class="note">There is no directory behind the sandbox, so it comes back on
     its own \u2014 as whoever the fake server says signed in.</p>
  <p><strong>back in <span id="n">3</span></strong></p>
  <button type="button" id="now">back now</button>
</main>
<script>
  const go = () => location.assign('/callback')
  document.getElementById('now').addEventListener('click', go)
  let n = 3
  const t = setInterval(() => {
    n -= 1
    if (n <= 0) { clearInterval(t); go(); return }
    document.getElementById('n').textContent = String(n)
  }, 1000)
</script>
`)
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
			return json(res, 200, {
				brand: 'Contoso',
				client: 'the demo product',
				scope: ['openid', 'profile', 'email'],
				providers: url.searchParams.has('consent_challenge') ? [] : providers,
				password: !url.searchParams.has('consent_challenge'),
			})

		case 'GET /provider': {
			// The hop the sandbox cannot have. A real one leaves this origin
			// for the operator's directory, and there is none behind a made-up
			// server -- so what stands in is a page that **says so** and comes
			// back, rather than a redirect that lands on the next screen so
			// fast the trip looks like it never happened.
			//
			// It was that redirect for a day, and it taught the wrong shape:
			// the provider button appeared to lead straight to the consent
			// screen, so the consent screen appeared to come *before* the
			// directory. It comes after. Nobody is named until the directory
			// has answered.
			return away(res, url.searchParams.get('connection') ?? 'the directory')
		}

		case 'GET /callback': {
			// Where the directory sends the browser back, which in a real
			// deployment carries `code` and `state`. The fake carries neither
			// and does what the real one ends with: this is somebody now.
			//
			// `erin` because a provider sign-in has no second form -- whatever
			// the directory asked for, it asked for.
			at.who = 'erin'
			at.proved = ['password']
			res.statusCode = 302
			res.setHeader('location', '/consent?consent_challenge=sandbox')

			return res.end()
		}

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
	// Its own optimizer cache, for the reason `vite.console.ts` gives: the
	// default is one directory for all three configs, and `scripts/e2e.sh`
	// runs two dev servers at once.
	cacheDir: '../node_modules/.vite-login',
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
