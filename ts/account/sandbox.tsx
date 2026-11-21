/**
 * The account UI, with its other half made up inside the browser.
 *
 * This is a preview, like the Login App's development server, not a second
 * implementation of roster. The generated service descriptors keep the shapes
 * honest; none of the permissions, proofs or tokens here are security checks.
 * No request leaves this page. Reloading starts with the selected fixture again.
 * Only VITE_ACCOUNT_SANDBOX loads this module; the shipped page never does.
 */
import { create } from '@bufbuild/protobuf'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { Code, ConnectError, createRouterTransport, type Interceptor } from '@connectrpc/connect'
import { Queries } from '@lesomnus/payday/query'
import type { App } from '@lesomnus/payday/react'
import { Store } from '@lesomnus/payday/store'
import { Button, PageToolbar } from '@lesomnus/nox'
import { ChoiceField } from './ui.js'

import { ApiKeySchema } from '#gen/app/apikey_pb.js'
import { ApiKeyService } from '#gen/app/apikey_svc_pb.js'
import { CredentialService } from '#gen/app/credential_svc_pb.js'
import { DelegationSchema } from '#gen/app/delegation_pb.js'
import { DelegationService } from '#gen/app/delegation_svc_pb.js'
import { EmailSchema } from '#gen/app/email_pb.js'
import { EmailService } from '#gen/app/email_svc_pb.js'
import { MeGetResponseSchema, MeService } from '#gen/app/me_pb.js'
import { entities } from '#gen/entities.js'
import { HolderSchema, ProfileSchema, PortraitSchema } from '#gen/roster/payday/holder_pb.js'
import { HolderService } from '#gen/roster/payday/holder_svc_pb.js'
import { b64url } from '#lib/signin.js'
import './sandbox.css'

const scenarios = [
	['account', 'account'],
	['signed-out', 'signed out'],
	['empty', 'empty account'],
	['mfa', 'second factor sign-in'],
	['directory', 'directory-managed account'],
	['limited', 'limited permissions'],
] as const

const selected = new URLSearchParams(location.search).get('sandbox') ?? 'account'
const scenario = scenarios.some(([s]) => s === selected) ? selected : 'account'
const password = 'correct horse battery staple'
const methods = [
	'/roster.HolderService/Get', '/roster.HolderService/Update', '/roster.HolderService/Portray',
	'/roster.IdentityService/Add', '/roster.EmailService/List', '/roster.EmailService/Get',
	'/roster.EmailService/Add', '/roster.EmailService/Erase', '/roster.EmailService/Verify',
	'/roster.CredentialService/Set', '/roster.CredentialService/Enrol', '/roster.CredentialService/Erase',
	'/roster.DelegationService/List', '/roster.DelegationService/Erase', '/roster.ApiKeyService/Issue', '/roster.ApiKeyService/Erase',
]
const id = (): Uint8Array => crypto.getRandomValues(new Uint8Array(16))
const now = () => timestampFromDate(new Date())
const later = (ms: number) => timestampFromDate(new Date(Date.now() + ms))
const same = (a: Uint8Array, b: Uint8Array): boolean => a.length === b.length && a.every((v, i) => v === b[i])
const refuse = (message: string, code = Code.InvalidArgument): never => {
	throw new ConnectError(message, code)
}

/** Each call owns a fresh set of fixtures, session and store, all in memory. */
export function start() {
	const empty = scenario === 'empty'
	const tenant = id()
	const alias = scenario === 'mfa' ? 'frank' : scenario === 'directory' ? 'grace' : 'erin'
	const displayName = { frank: 'Frank Park', grace: 'Grace Kim', erin: 'Erin Chen' }[alias]
	const holder = create(HolderSchema, {
		id: id(), tenant: { id: tenant }, alias,
		dateCreated: now(), dateUpdated: now(),
		profile: create(ProfileSchema, empty ? {} : { displayName, department: 'Design', employeeNo: 'E-1042', locale: 'en' }),
	})
	const me = create(MeGetResponseSchema, {
		id: holder.id, tenant, alias: holder.alias,
		own: scenario === 'limited' ? ['/roster.HolderService/Get'] : methods,
		provisionedThrough: scenario === 'directory' ? 'entra' : '',
		credentials: [{ kind: 'password' }, ...(scenario === 'mfa' ? [{ kind: 'totp', name: 'phone' }] : [])],
		identities: empty ? [] : [{ id: id(), provider: 'entra', subject: `${alias}@contoso.example`, dateCreated: now() }],
	})
	let emails = empty ? [] : [
		create(EmailSchema, { id: id(), holder, address: `${alias}@contoso.example`, dateVerified: now(), dateCreated: now(), dateUpdated: now() }),
		create(EmailSchema, { id: id(), holder, address: `${alias}.personal@example.com`, dateCreated: now(), dateUpdated: now() }),
	]
	let keys = empty ? [] : [create(ApiKeySchema, {
		id: id(), holder, alias: 'laptop', methods: ['/roster.MeService/Get'], dateCreated: now(), dateExpires: later(7 * 86400_000),
	})]
	let sessions = empty ? [] : [create(DelegationSchema, {
		id: id(), holder, methods, dateCreated: now(), dateExpires: later(3600_000),
	})]
	let currentPassword = password
	let signedIn = !['signed-out', 'mfa'].includes(scenario)
	let pending = false
	let erased = false
	let fresh = true
	let enrolment: string | null = null
	let device = true
	const identity = `account-sandbox:${crypto.randomUUID()}`
	const check = (): void => {
		if (!signedIn || erased) refuse('sign in first', Code.Unauthenticated)
	}
	const ref = (key: { case: string | undefined; value?: unknown }): Uint8Array => key.case === 'id' ? key.value as Uint8Array : new Uint8Array()

	const provider = async (name: string): Promise<void> => {
		if (!['entra', 'github'].includes(name)) refuse('unknown provider')
		if (!me.identities.some((i) => i.provider === name)) {
			me.identities.push({ $typeName: 'roster.SignInIdentity', id: id(), provider: name, subject: `${holder.alias}@${name}.example`, dateCreated: now() })
		}
		signedIn = true
		fresh = false
	}

	const transport = (interceptor: Interceptor) => createRouterTransport((router) => {
		router.service(MeService, {
			get() {
				me.emails = emails.map((e) => ({ $typeName: 'roster.MeEmail', address: e.address, dateVerified: e.dateVerified }))
				me.keys = keys.map((k) => ({ $typeName: 'roster.SignInKey', id: k.id, alias: k.alias, methods: k.methods, dateExpires: k.dateExpires, dateUsed: k.dateUsed }))
				return me
			},
			unlink(req) {
				me.identities = me.identities.filter((i) => !same(i.id, req.id))
				return {}
			},
			signOutEverywhere() {
				signedIn = false
				// The real page reloads after this call. Keep that reload signed out.
				const url = new URL(location.href)
				url.searchParams.set('sandbox', 'signed-out')
				history.replaceState(null, '', url)
				return { dateInvalidated: now() }
			},
			leave(req) {
				if (req.confirm !== holder.alias) refuse('type your alias to confirm')
				if (!fresh) refuse('sign in again to delete your account', Code.FailedPrecondition)
				erased = true
				return { dateErased: now(), dateForgotten: later(30 * 86400_000) }
			},
		})
		router.service(HolderService, {
			get: () => holder,
			update(req) {
				holder.profile = create(ProfileSchema, req.profile)
				holder.dateUpdated = now()
				return holder
			},
			portray(req) {
				// Preview the file locally. Rendering production renditions belongs to roster.
				const uri = req.image.length === 0 ? '' : URL.createObjectURL(new Blob([new Uint8Array(req.image)]))
				holder.portrait = create(PortraitSchema, { renditions: uri === '' ? [] : [{ size: 128, uri }] })
				if (uri === '' && holder.profile !== undefined) holder.profile.picture = ''
				holder.dateUpdated = now()
				return holder
			},
		})
		router.service(EmailService, {
			list: () => ({ items: emails }),
			add(req) {
				const address = req.address.trim().toLowerCase()
				if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(address)) refuse('enter an email address')
				if (emails.some((e) => e.address === address)) refuse('that address is already here', Code.AlreadyExists)
				const row = create(EmailSchema, { id: id(), holder, address, dateCreated: now(), dateUpdated: now() })
				emails.push(row)
				return row
			},
			erase(req) {
				emails = emails.filter((e) => !same(e.id, ref(req.key)))
				return { erased: true }
			},
		})
		router.service(CredentialService, {
			set(req) {
				if (new TextDecoder().decode(req.current) !== currentPassword) refuse('the current password did not match')
				const next = new TextDecoder().decode(req.secret)
				if (next.length < 8) refuse('use at least 8 characters')
				currentPassword = next
				return {}
			},
			enrol(req) {
				if (me.credentials.some((c) => c.kind === req.kind && c.name === req.name)) refuse('that factor is already here', Code.AlreadyExists)
				if (req.kind === 'webauthn') {
					me.credentials.push({ $typeName: 'roster.SignInCredential', kind: req.kind, name: req.name })
					return {}
				}
				enrolment = req.name
				const seed = 'JBSWY3DPEHPK3PXP'
				return { seed, uri: `otpauth://totp/Contoso:${holder.alias}?secret=${seed}&issuer=Contoso` }
			},
			erase(req) {
				if (req.key.case === 'kind') {
					const { kind, name } = req.key.value
					me.credentials = me.credentials.filter((c) => c.kind !== kind || c.name !== name)
				}
				return { erased: true }
			},
		})
		router.service(ApiKeyService, {
			issue(req) {
				if (keys.some((k) => k.alias === req.alias)) refuse('that key name is already here', Code.AlreadyExists)
				const key = create(ApiKeySchema, { id: id(), holder, alias: req.alias, methods: req.methods, dateExpires: req.dateExpires, dateCreated: now() })
				keys.push(key)
				return { key, token: `sandbox_only_${b64url(id())}` }
			},
			erase(req) {
				keys = keys.filter((k) => !same(k.id, ref(req.key)))
				return { erased: true }
			},
		})
		router.service(DelegationService, {
			list: () => ({ items: sessions }),
			erase(req) {
				sessions = sessions.filter((s) => !same(s.id, ref(req.key)))
				return { erased: true }
			},
		})
	}, {
		transport: { interceptors: [interceptor] },
		router: { interceptors: [(next) => async (req) => {
			check()
			return next(req)
		}] },
	})

	const response = (status: number, value?: unknown): Response => new Response(value === undefined ? null : JSON.stringify(value), {
		status, headers: { 'content-type': 'application/json' },
	})
	const request: typeof fetch = async (input, init) => {
		const req = input instanceof Request ? new Request(input, init) : new Request(new URL(String(input), location.origin), init)
		const url = new URL(req.url)
		const path = url.pathname
		if (url.origin !== location.origin) return response(501, 'the sandbox has no external services')
		if (path === '/providers') return response(200, {
			tenant: { alias: 'contoso', name: 'Contoso', labels: {} },
			providers: [{ name: 'entra', issuer: 'https://login.microsoftonline.com/common/v2.0' }, { name: 'github', issuer: 'https://github.com' }],
			password: true, terminal: true, leave: true, recover: true, leaveLink: true,
		})
		if (path === '/session') {
			if (req.method === 'GET') return response(signedIn && !erased ? 200 : 401, { identity })
			if (req.method === 'DELETE') { signedIn = false; pending = false; return response(204) }
			if (req.method === 'POST') {
				const body = await req.json() as { alias: string; password: string }
				pending = false
				if (erased || body.alias !== holder.alias || body.password !== currentPassword) return response(401)
				const factors = me.credentials.filter((c) => c.kind !== 'password').map((c) => ({ kind: c.kind, name: c.name, id: b64url(holder.id) }))
				if (factors.length > 0) { pending = true; return response(200, { factors }) }
				signedIn = true; fresh = true
				return response(204)
			}
		}
		if (path === '/session/continue' && req.method === 'POST') {
			const body = await req.json() as { kind: string; name: string; secret: string }
			const accepted = pending && me.credentials.some((c) => c.kind === body.kind && c.name === body.name) &&
				(body.kind === 'webauthn' || body.secret === '123456')
			pending = false
			if (!accepted) return response(401)
			signedIn = true; fresh = true
			return response(204)
		}
		if (path === '/recover' && req.method === 'POST') return response(202)
		if (!signedIn || erased) return response(401)
		if (path === '/prove' && req.method === 'POST') {
			const body = await req.json() as { name: string; secret: string }
			if (enrolment === null || body.name !== enrolment || body.secret !== '123456') return response(401)
			me.credentials.push({ $typeName: 'roster.SignInCredential', kind: 'totp', name: enrolment })
			enrolment = null
			return response(204)
		}
		if (['/verify', '/claim'].includes(path) && req.method === 'POST') return response(202)
		if (path === '/leave/link' && req.method === 'POST') {
			const body = await req.json() as { address: string }
			return response(202, { address: body.address, date_expires: new Date(Date.now() + 900_000).toISOString() })
		}
		if (path === '/device/pending') return device && url.searchParams.get('user_code') === 'DEMO-CODE'
			? response(200, { name: 'demo laptop', methods: ['/roster.MeService/Get'] }) : response(404, 'that code is not waiting')
		if (path === '/device/approve' && req.method === 'POST') { device = false; return response(204) }
		return response(501, `the sandbox does not implement ${req.method} ${path}`)
	}

	// A made-up authenticator keeps UI work independent of security-key hardware.
	const credential = (): Credential => ({
		id: b64url(holder.id), type: 'public-key', rawId: new Uint8Array(holder.id).buffer,
		response: {}, toJSON: () => ({ id: b64url(holder.id), type: 'public-key', response: {} }),
	} as unknown as Credential)
	const credentials: Pick<CredentialsContainer, 'create' | 'get'> = {
		create: async () => credential(), get: async () => credential(),
	}

	return {
		request, transport, credentials, provider,
		app(transport: ReturnType<typeof createRouterTransport>): App {
			const store = Store.open(entities, { name: 'account-sandbox', identity })
			return { store, queries: new Queries(store, transport, entities) }
		},
		panel: <aside className="account-sandbox" aria-label="account sandbox">
			<PageToolbar>
			<strong>Account sandbox</strong>
			<ChoiceField label="Preview state" aria-label="preview state" value={scenario} onChange={(e) => {
				const url = new URL(location.href)
				url.searchParams.set('sandbox', e.target.value)
				url.hash = ''
				location.assign(url)
			}}>{scenarios.map(([value, name]) => <option key={value} value={value}>{name}</option>)}</ChoiceField>
			<Button type="button" onClick={() => location.reload()}>reset preview</Button>
			<span>Browser only · sample data · reload resets changes</span>
			</PageToolbar>
			<small>Sign in: {holder.alias} / {password} · MFA: 123456 · terminal: DEMO-CODE. Providers and security keys are simulated; mail is not sent.</small>
		</aside>,
	}
}
