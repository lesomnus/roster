/**
 * The local store and the reads that keep themselves up to date.
 *
 * `entities` is generated -- one declaration per entity, and no behaviour. The
 * runtime reads those together with the protobuf descriptors and implements
 * normalizing, reconciling and applying a `Watch` once, for every entity there
 * is.
 *
 * `Queries` is the half above it. A read that goes through it is a read the
 * framework knows about, so when a row it answered with changes, everything
 * drawing that row is told -- and every place it appears changes at once, with
 * nothing declared anywhere. `@lesomnus/payday/react` is thirty lines over
 * this; Vue or Svelte is the same file, the same length.
 *
 * @module
 */

import type { Transport } from '@connectrpc/connect'

import { Queries } from '@lesomnus/payday/query'
import type { App } from '@lesomnus/payday/react'
import { Store } from '@lesomnus/payday/store'
import { deleteOtherDisks, openDisk } from '@lesomnus/payday/store/idb'

import { entities } from '#gen/entities.js'

/**
 * Where a store is: which of this app's stores, and whose.
 *
 * `name` is the store -- `roster` for a page's own, another for a second store
 * a page keeps beside it, which the admin console's tenants screen does -- and
 * `identity` is whose: the caller's, as the server names a store for their
 * session (`AuthService.Which`, the account app's `GET /session`). Every call
 * a store's queries make says that name, and a session handler serves it only
 * as a session it is the name of, so a store is never read into or sent from
 * as anybody else.
 */
export interface At {
	name: string
	identity: string
}

/**
 * open answers with one of this app's stores and its queries, for one caller.
 *
 * The identity is what the caller may see, as the server names it rather than
 * as the person -- see payday's `authsession.IdentityOf`. What a caller may see
 * is the actor and the scope together, so the same person under a narrowed
 * grant is a different store; keyed on the person, the narrowed session would
 * draw the wide session's rows. And it is not the session's key, so a session
 * that idled out is the same store once its person signs in again, with what
 * they wrote meanwhile still waiting to be sent.
 *
 * The mirror is what makes a reload draw the page it had rather than a spinner
 * for it. Leave out `disk` and everything still works -- the store is a store
 * either way, and it lives as long as the tab does.
 *
 * It expires after a week; `openDisk(entities, at, { keep })` is the knob, and
 * `Infinity` turns it off. What it bounds is both how large a mirror gets and
 * how old the answer a reload draws may be.
 *
 * And once this caller's mirror is open, every other caller's mirror of the
 * store is deleted (`deleteOtherDisks`): whoever used this browser before is
 * not owed what they left on it, and whoever uses it now is owed not finding
 * it. Not waited on, as payday asks -- a database another page holds open
 * answers late, and the page draws meanwhile.
 *
 * `sweep` is false where there is nobody else's to find: the sandbox, whose
 * server is the page and whose stores are named for the page and not for a
 * session -- and whose two pages share one origin, so a sweep by one would
 * delete the other's.
 */
export async function open(transport: Transport, at: At, sweep = true): Promise<App> {
	const disk = await openDisk(entities, at)
	if (sweep) void deleteOtherDisks(at)

	const store = Store.open(entities, { ...at, disk })
	await store.hydrate()

	return { store, queries: new Queries(store, transport, entities) }
}

/**
 * letGo closes one of this app's stores, forgetting it first when `forget` is
 * set: the rows, the answers, the writes it kept, and its mirror.
 *
 * Nothing waits between the two: `close` closes the mirror once what it was
 * told has reached it, the forget above all (lesomnus/payday#148). And it
 * rejects when the forget did not -- the mirror is closed all the same, and
 * what was forgotten is still on this browser -- so this does too, and what a
 * page does then is say so to the person ([stillHere]). Rows that did not
 * reach the mirror on the way are no part of it.
 */
export async function letGo(app: App, forget: boolean): Promise<void> {
	if (forget) void app.store.forget()
	await app.store.close()
}

/**
 * stillHere is what a page tells somebody whose sign-out did not reach this
 * browser's copy of what they saw ([letGo]): it is theirs, it stays until
 * somebody else signs in here and the sweep in [open] takes it, and they are
 * the one who can take it off sooner.
 */
export const stillHere =
	"you are signed out, but this browser still holds what you saw here: clear this site's data in your browser to remove it"

/**
 * nobody is a store for a page nobody is signed in to: in memory and nowhere
 * else, named for no session -- so the window a development build draws beneath
 * a sign-in form has something to look at, and anything it asks of a server
 * holding somebody's session in another tab is refused rather than answered as
 * them.
 */
export function nobody(transport: Transport): App {
	const store = Store.open(entities, { name: 'roster', identity: '' })

	return { store, queries: new Queries(store, transport, entities) }
}
