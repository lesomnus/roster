/**
 * The client, which is this small because nothing here is generated per
 * service.
 *
 * A page does not read through this -- it reads through `store.ts`, so that a
 * row it drew redraws when the row changes. This is for what does not want
 * that: several writes as one transaction, a one-off call, a script.
 *
 * protobuf-es emits the service descriptors beside the messages and Connect's
 * `createClient` takes a descriptor, so adding an entity to the schema is one
 * line here and nothing to keep in step.
 *
 * The transport is the only thing that changes between a real server and the
 * sandbox, and nothing above this file knows which it got.
 *
 * @module
 */

import { createClient, type Client, type Transport } from '@connectrpc/connect'

import { ApiKeyService } from '../gen/app/apikey_svc_pb.js'
import { RoleService } from '../gen/app/role_svc_pb.js'
import { BindingService } from '../gen/app/role_svc_pb.js'
import { SiteService } from '../gen/app/site_svc_pb.js'
import { MeService } from '../gen/app/me_pb.js'
import { TenantService } from '../gen/roster/payday/tenant_svc_pb.js'
import { HolderService } from '../gen/roster/payday/holder_svc_pb.js'
import { CredentialService } from '../gen/app/credential_svc_pb.js'
import { BatchService } from '@lesomnus/payday/pdpb'

export interface App {
	readonly tenant: Client<typeof TenantService>
	readonly holder: Client<typeof HolderService>
	readonly site: Client<typeof SiteService>
	readonly role: Client<typeof RoleService>
	readonly binding: Client<typeof BindingService>

	/** The deployment's own keys, served on the control plane's port only. */
	readonly apiKey: Client<typeof ApiKeyService>

	/** What the caller is, in one round trip; see `server/me`. */
	readonly me: Client<typeof MeService>

	/** Several writes as one transaction; see `payday/batch`. */
	readonly batch: Client<typeof BatchService>
}

export function app(transport: Transport): App {
	return {
		tenant: createClient(TenantService, transport),
		holder: createClient(HolderService, transport),
		site: createClient(SiteService, transport),
		role: createClient(RoleService, transport),
		binding: createClient(BindingService, transport),
		apiKey: createClient(ApiKeyService, transport),
		me: createClient(MeService, transport),
		batch: createClient(BatchService, transport),
	}
}

/**
 * Writes is what a page calls directly, where the store cannot answer.
 *
 * Not the whole of `App`, and not a second copy of either console: it is the
 * calls whose answer is **not a row** -- `Credential.Issue` hands back a
 * password that is shown once and stored nowhere, so there is nothing for the
 * store to hold and nothing for it to redraw -- plus the writes that stand
 * something up. Everything else a page reads goes through the store.
 *
 * Both consoles have one, over their own listener, which is why this is not
 * called `Admin` any more. The admin console's is the **admin** listener, where
 * a roster operator reaches their customers; the user console's is the walled
 * **data plane**, where a roster user reaches their own tenant (#34). The
 * services are the same because the rows are the same rows -- what differs is
 * who is calling and what the wall lets them touch, and neither of those is a
 * decision a client makes.
 *
 * `CredentialService` is on the admin port for the reason roadmap.md's item 10
 * gives -- an air gap has an operator instead of a mail server, so `Issue`
 * hands them a password to read out -- and `cmd/admin.go` says what it costs
 * and what bounds it. It was `VouchService` until `Vouch.Reset` became
 * `Credential.Issue`; the page called nothing else there, so the client went
 * with the method.
 *
 * `tenant`, `role` and `binding` are the writes that make a customer, and they
 * are here because `roster init` stopped making one. What creates a customer is
 * an operator, on the admin port, through the rules -- `mayGrant` compares
 * methods and site rather than tenants, so the whole-package pattern an
 * operator holds in the **control** plane reaches a tenant that did not exist a
 * moment ago. A user console holds the same client and never calls them: the
 * wall refuses a tenant that is not theirs, which is the answer rather than a
 * shorter interface.
 */
export interface Writes {
	readonly holder: Client<typeof HolderService>
	readonly credential: Client<typeof CredentialService>

	/** The four writes that stand a customer up; see `customers.tsx`. */
	readonly tenant: Client<typeof TenantService>
	readonly role: Client<typeof RoleService>
	readonly binding: Client<typeof BindingService>

	/**
	 * A key for one of a tenant's people, answered once.
	 *
	 * The same service on both listeners, on the same rows, minting the same
	 * `rt_`. It is on the admin port too because that is the one the admin
	 * console reaches, and a screen that lists somebody's keys and cannot add
	 * one is a screen that sends an operator to a shell.
	 */
	readonly apiKey: Client<typeof ApiKeyService>
}

export function writes(transport: Transport): Writes {
	return {
		holder: createClient(HolderService, transport),
		credential: createClient(CredentialService, transport),
		tenant: createClient(TenantService, transport),
		role: createClient(RoleService, transport),
		binding: createClient(BindingService, transport),
		apiKey: createClient(ApiKeyService, transport),
	}
}
