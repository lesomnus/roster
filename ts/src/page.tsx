/**
 * The console's first screen: who you are and what you may do.
 *
 * It is one call, and that is the point. Which roles somebody effectively holds
 * is a union over bindings, group memberships and team memberships, and a page
 * that worked it out from the parts would be a second implementation of
 * `gate.Policy` that drifts from the one enforcing it. `MeService` answers with
 * what roster itself would decide.
 *
 * What it draws the menu from is the same list -- so what a page offers and
 * what the server allows cannot disagree. That is a claim about drawing and not
 * about safety: the server decides on every call, and a client that treated
 * this as the decision would be one an altered client could talk out of.
 *
 * @module
 */

import { useQuery } from '@lesomnus/payday/react'

import { MeService } from '../gen/app/me_pb.js'

/**
 * covers is `frame.Covers` in the browser: three parts, each `*` or a name.
 *
 * The same three comparisons the server makes, because the patterns it answers
 * with are patterns rather than an expansion -- an expansion would be the
 * methods that exist in whichever replica answered, and during a rolling deploy
 * two of them would tell this page two different things about one person.
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

export function Page(props: { onSignOut: () => void }): React.ReactNode {
	// A query like any other, so the store holds the answer and anything else
	// naming those rows draws the same copy.
	const me = useQuery(MeService.method.get, {})

	if (me.state === 'pending') return <main className="loading">…</main>
	if (me.state === 'error') {
		return (
			<main className="error">
				<p>{me.error instanceof Error ? me.error.message : 'no'}</p>
				<button onClick={props.onSignOut}>sign out</button>
			</main>
		)
	}

	const v = me.data
	const may = (method: string): boolean =>
		(v?.methods ?? []).some((held) => covers(held, method))

	return (
		<main className="console">
			<header>
				<h1>{v?.alias}</h1>
				<button onClick={props.onSignOut}>sign out</button>
			</header>

			<section>
				<h2>what you may do</h2>
				<ul className="methods">
					{(v?.methods ?? []).map((m) => (
						<li key={m}>
							<code>{m}</code>
						</li>
					))}
				</ul>
				<p className="note">
					Patterns, not a list of every RPC. <code>/roster.*/*</code> is
					everything roster serves, now and after an upgrade.
				</p>
			</section>

			<section>
				<h2>what this console would show</h2>
				<ul className="menu">
					<Item name="people" ok={may('/roster.HolderService/List')} />
					<Item name="customers" ok={may('/roster.TenantService/List')} />
					<Item name="sites" ok={may('/roster.SiteService/List')} />
					<Item name="roles" ok={may('/roster.RoleService/List')} />
					<Item name="keys" ok={may('/roster.ApiKeyService/List')} />
				</ul>
				<p className="note">
					Greyed out is a screen this operator may not open. The server
					refuses it either way — this only decides what is worth drawing.
				</p>
			</section>

			{(v?.sites ?? []).length > 0 && (
				<section>
					<h2>narrowed to</h2>
					<p>{v?.everySite === true ? 'every site' : `${v?.sites.length} site(s)`}</p>
				</section>
			)}
		</main>
	)
}

function Item(props: { name: string; ok: boolean }): React.ReactNode {
	return <li className={props.ok ? 'may' : 'may-not'}>{props.name}</li>
}
