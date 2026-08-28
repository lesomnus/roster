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
import { VouchService } from '../gen/app/vouch_pb.js'
import { IssueService } from '../gen/app/issue_pb.js'
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
 * Admin is what a page calls on the **admin** listener, which is where a
 * deployment's operator reaches its customers.
 *
 * Not the whole of `App`, because this is not a second copy of the console: it
 * is the writes an operator makes -- about one person, and about standing a
 * customer up -- and a page reads through the store for everything else.
 *
 * `VouchService` is on that port for the reason roadmap.md's item 10 gives --
 * an air
 * gap has an operator instead of a mail server -- and `cmd/admin.go` says what
 * it costs and what bounds it.
 *
 * `tenant`, `role` and `binding` are the four writes that make a customer, and
 * they are here because `roster init` stopped making one. What creates a
 * customer is an operator, on this port, through the rules -- `mayGrant`
 * compares methods and site rather than tenants, so the whole-package pattern
 * an operator holds in the **control** plane reaches a tenant that did not
 * exist a moment ago.
 */
export interface Admin {
	readonly holder: Client<typeof HolderService>
	readonly vouch: Client<typeof VouchService>

	/** The four writes that stand a customer up; see `customers.tsx`. */
	readonly tenant: Client<typeof TenantService>
	readonly role: Client<typeof RoleService>
	readonly binding: Client<typeof BindingService>

	/**
	 * A key for one of a customer's people, answered once.
	 *
	 * The same service the data plane serves, on the same rows, minting the
	 * same `rt_`. It is on this port too because this is the one a console
	 * reaches, and a screen that lists somebody's keys and cannot add one is a
	 * screen that sends an operator to a shell.
	 */
	readonly issue: Client<typeof IssueService>
}

export function admin(transport: Transport): Admin {
	return {
		holder: createClient(HolderService, transport),
		vouch: createClient(VouchService, transport),
		tenant: createClient(TenantService, transport),
		role: createClient(RoleService, transport),
		binding: createClient(BindingService, transport),
		issue: createClient(IssueService, transport),
	}
}
