# A directory provisioning people, over SCIM

Without this, roster learns about a person in two ways only: they sign in
(`enrol`, a tenant's `config.profile`), or an operator writes them down. Nothing
tells it about somebody who has not signed in yet, about what changed about
somebody, or about somebody who **left** -- the one that matters, because a
leaver is refused at their directory and goes on holding whatever they held here
until somebody remembers to suspend them.

SCIM 2.0 (RFC 7643, RFC 7644) is the directory telling it. roster is the
*service provider*: the directory pushes, roster never polls. Entra, Okta,
OneLogin and JumpCloud all push to an endpoint of their customer's naming.
Google Workspace does not, to an app that is not in its own catalogue -- a tenant
on Google is still a sign-in, and a directory read is a separate plan.

What this is for is the people. Groups are the next step (#86), and the endpoint
answers `501` for them so a directory skips rather than retries.

## The shape

`scim/` is a **consumer**, like `ldap/`: it reaches roster over the wire, as the
caller that presented the token. Every request carries the directory's tenant
key, and that key -- forwarded, and nothing else with it, never a cookie -- is
what roster answers: its wall, its methods, its role, the trail naming it. The
endpoint decides nothing the key could not.

```
directory ─ HTTPS /scim/v2, Bearer rt_… ─▶ proxy ─▶ scim.addr ─ gRPC, the same rt_ ─▶ roster
```

**A listener of its own.** `scim.addr`, in `roster serve` or `roster scim serve`,
serves `/scim/v2` and nothing else. Not a path on `server.http`: that listener
answers every RPC over Connect, and the one a directory's cloud can reach should
answer SCIM alone -- a proxy rule that forwards a little too much then publishes
SCIM, not everything. A deployment on a private network puts a proxy in front that
exposes this path on a public name, and nothing else of it; limiting that path to
the directory's address ranges is a second fence worth having.

**One tenant per key.** A tenant key is one tenant by being minted in it, so the
endpoint never says which tenant a request is about and refuses a deployment key,
which would have to.

## Setting a tenant up

```sh
roster scim provision --tenant contoso --connection entra
```

marks `entra` as the connection contoso's directory provisions through
(`Connection.provisions`; one a tenant), makes the directory's holder (`scim`), a
role holding exactly what the endpoint calls, its binding, and a tenant key --
printed once. Running it again is a rotation: the key is replaced, not added to. A
connection a file declares is the file's to mark, `provisions: true` beside it in
`resources.yaml`, or the next start takes it back.

The connection should name people by the identifier the directory sends as
`externalId`. For Entra that is the object id, which is `oid` in a token and
`subject_claim: oid` on the connection ([login.md](login.md) § *Which claim is the
person*): Entra's `sub` is one per app, so a directory could never have named
anybody by it.

### Entra

On the enterprise application, *Provisioning*, *Automatic*:

- **Tenant URL**: the public name the proxy answers at, with `/scim/v2`.
- **Secret token**: the key `provision` printed.
- **Mappings**: `externalId` from **`objectId`** -- the default is `mailNickname`,
  which is a name and not the identifier a sign-in carries, so a person
  provisioned under it would never be the person who signs in. `userName` from
  `userPrincipalName` is how somebody already here is found. Leave `displayName`
  mapped only if names come from the directory: a tenant that keeps names in Slack
  takes it out, and the directory then writes none.
- **Scope**: the users assigned to the application.

*Provision on demand* with one person is the check before turning it on. Microsoft
publishes a SCIM validator that walks the same requests against a URL.

## What it does with a person

| SCIM | roster |
| --- | --- |
| `id` | `Holder.id` |
| `userName`, `emails` | an `Email` row, written **unverified**; a lookup by `userName` finds a person by any address of theirs |
| `externalId` | their `Identity` at the provisioning connection |
| `displayName`, `name.formatted` | `Holder.name` when made, `profile.display_name` after |
| enterprise `department`, `employeeNumber` | `profile.department`, `profile.employee_no` |
| `preferredLanguage`, `locale` | `profile.locale` |
| `active` | a suspension of the directory's, and its lifting |

**The directory owns what it sends.** A value it sends is written over what was
there; a value it does not send is left, because a tenant that keeps names
somewhere else takes them out of the mapping and must not have them wiped every
cycle. Taking a value away is a `remove`. What it sends that is not kept here --
a title, a phone number -- is answered as accepted and written to the log, and a
changed address or `externalId` is not followed: an address and an identity are a
person's ways in, and they move by the person or an operator.

**Somebody already here is matched, not made again.** A directory looks a person
up before it creates them, and the people a tenant had -- signed in already, or
entered by an operator -- are found by their address. A create for an address or
an `externalId` somebody has is refused as `uniqueness`, which is what tells a
directory to match instead.

**Leaving.** `active: false` suspends them -- the same suspension `Disable`
writes, refused everywhere -- and `active: true` lifts it. `DELETE` suspends them
and keeps the row: what becomes of it is an operator's decision, and the endpoint
speaks of them no more. A re-hire with the same address is refused as taken until
an operator frees it.

**The directory lifts its own suspensions and no other.** It is one way: it never
hears that an operator suspended somebody, and restarting its provisioning says
`active` for everybody in scope. So `Holder.directory` says whose a suspension is,
`Activate` refuses an operator's, an operator's `Disable` or `Enable` makes a
suspension theirs, and somebody the directory deleted is an operator's to bring
back ([operating.md](operating.md) § *Stopping somebody*).

## What its key may do, and why not more

| method | what for |
| --- | --- |
| `HolderService/Provision` | somebody new: the person, their identity, their address, in one write |
| `HolderService/Deactivate`, `Activate` | a suspension of the directory's, for people who sign in through the connection |
| `HolderService/Update`, `Get` | what it owns of a profile, and reading it back |
| `EmailService/Get`, `List`, `IdentityService/Get`, `List` | finding somebody by an address or an `externalId` |
| `MeService/Get`, `ConnectionService/List` | which tenant the key is, and its provisioning connection |

**Not `Identity.Add`, not an address written or attested, not `Holder.Add`, not
`Erase`.** Each of those reaches people who already exist, and `Core.mayReach`
answers yes for everybody who holds nothing -- which is most people. A key a
directory's cloud presents from the internet would then be a way into all of them.
`Provision` writes ways in only into the person it makes in the same breath, so a
key holding it reaches the people it makes and no one else. `Deactivate` reaches
the people who sign in through the provisioning connection -- not an app's holder,
not a front door's.

## Where each fact is

| | |
| --- | --- |
| `scim/` | the endpoint: auth, the routes, the mapping, both PATCH dialects |
| `server/core/directory.go` | `Provision`, `Deactivate`, `Activate`, and who a directory speaks for |
| `cli/scim.go` | `roster scim serve` and `roster scim provision` |
| `cmd/consumers.go` | the `scim:` block |

| the promise | pinned by |
| --- | --- |
| a person's whole life at the endpoint, in Entra's order and dialect | `TestADirectoryProvisionsSomebodyTheWayEntraDoes` |
| somebody already here is matched and not made again | `TestSomebodyAlreadyHereIsMatchedAndNotMadeAgain` |
| the directory lifts only its own suspensions | `TestADirectoryLiftsOnlyItsOwnSuspension` · `TestTheDirectoryDoesNotLiftAnOperatorsSuspension` |
| its key reaches only the people it makes | `TestADirectoryMakesPeopleAndOnlyNewOnes` · `TestTheEndpointTakesATenantKeyAndNothingElse` |
