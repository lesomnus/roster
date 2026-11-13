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

It is for the people first, and for the groups a directory keeps them in: who is
in which is the other thing it knows and roster otherwise does not (§ *What it
does with a group*).

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

**The connection is the organisation's own door.** One at an issuer anybody can
make an account at -- GitHub's, Google's, Microsoft's `common` and
`organizations`, personal Microsoft accounts -- is refused `provisions`, by the
deployment as much as by the tenant, and so is moving the one that provisions to
such an issuer. `Provision` writes a way in, and what makes that safe on a key a
directory's cloud presents is that it lets in nobody the organisation had not: at
an open door, a key that leaked would name its holder's own account and sign them
in. A door of the organisation's own that lets anybody sign up is the same, and
roster cannot tell it apart -- do not mark one.

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
- **Provision Microsoft Entra ID Groups**: on, as it is by default, where the
  groups assigned to the application should be groups here; its mappings are
  left as they are -- `displayName`, `members`, and `externalId` from `objectId`.
  Off, an assigned group still brings its members in as users and makes no
  group.
- **Scope**: the users assigned to the application. A group assigned to it brings
  its direct members and not the members of groups inside it: Entra does not
  follow nesting.

*Provision on demand* with one person is the check before turning it on. Microsoft
publishes a SCIM validator that walks the same requests against a URL.

## What it does with a person

| SCIM | roster |
| --- | --- |
| `id` | `Holder.id` |
| `userName`, `emails` | an `Email` row, written **unverified** when they are made, and what the directory last said after that (`Holder.directory_address`); a lookup by `userName` finds a person by any address of theirs, or by the one it last said |
| `externalId` | their `Identity` at the provisioning connection when they are made; written onto somebody already here, kept as said and believed by nothing (`Holder.directory_subject`) |
| `displayName`, `name.formatted` | `Holder.name` when made, `profile.display_name` after |
| enterprise `department`, `employeeNumber` | `profile.department`, `profile.employee_no` |
| enterprise `manager` | a `Relation` from them to their manager, labelled `manager` |
| `preferredLanguage`, `locale` | `profile.locale` |
| `phoneNumbers` | a `Phone` row each, one number a kind (`type`) |
| `active` | a suspension of the directory's, and its lifting |

**The directory owns what it sends.** A value it sends is written over what was
there; a value it does not send is left, because a tenant that keeps names
somewhere else takes them out of the mapping and must not have them wiped every
cycle. Taking a value away is a `remove`. What it sends that is not kept here --
a title, a postal address -- is answered as accepted and written to the log.

**A number is written, and replaced when it changes.** Where somebody is reached
is no way into their account -- nothing here sends to a number or finds anybody
by one -- so it is the one thing about a person who exists that the directory's
word moves ([entity.md](entity.md) § *Phone*). A number said for a kind is that
kind's one number, a kind not mentioned is left, and a whole User that speaks of
numbers says every one of them. What a directory sends in that field that is not
a number is said in the log and refuses nothing.

**Who somebody reports to is a relation, and one word.** roster keeps who one
person is to another and reads none of it ([entity.md](entity.md) §
*Relation*), so `manager` is a `Relation` from the person to their manager that
says `manager` among its labels: the word this endpoint writes and the LDAP
front reads. One at a time, as a directory says it -- a new manager replaces the
old -- and the word is taken out of a row somebody else also wrote into, rather
than the row erased for it. A manager who is nobody here is said in the log and
refuses nothing.

**A changed address or `externalId` is kept, and not followed.** An address and
an identity are a person's ways in -- a link is mailed to one, a sign-in is the
other -- and they move by the person or an operator, never by a key a
directory's cloud presents. But a word that is dropped is a drift nobody sees.
So what the directory says is kept beside the person as it said it
(`HolderService.Tell`), where no rule reads it and nothing signs in by it, and
the directory is answered with it: the User reads back as written, and a lookup
by a `userName` it changed finds them. The admin console shows where it is none
of somebody's addresses. Once an operator gives them the address and it is the
first of theirs, nothing is kept beside it.

**Somebody already here is matched, not made again.** A directory looks a person
up before it creates them, and the people a tenant had -- signed in already, or
entered by an operator -- are found by their address. A create for an address or
an `externalId` somebody has is refused as `uniqueness`, which is what tells a
directory to match instead.

**And matched is not linked.** The directory then writes its own id onto the
person it found, and that is an identity written onto somebody who exists: the
write its key is not given. So it is kept and believed by nothing, and they are
somebody the directory does **not** speak for -- its `active: false` and its
`DELETE` are refused, `403`, on the day they leave. They are linked by signing
in through the connection, where `login.enrol` matches by address (`expected`,
`enrolling`), or by an operator at the box:

```sh
roster scim adopt --tenant contoso                     # who, their address, what the directory calls them
roster scim adopt --tenant contoso --confirm contoso   # and link them, the list believed
```

The list is where a pair that does not belong together is seen before it is
written -- believing it is a decision about a way into somebody's account, which
is why it is made there and not by the directory. The endpoint says so in its
log the first time a directory names somebody it matched, and the admin console
says it on the person. Run it after a directory's first cycle, and again when
the log says so.

**Leaving.** `active: false` suspends them -- the same suspension `Disable`
writes, refused everywhere -- and `active: true` lifts it. `DELETE` suspends them
and keeps the row: what becomes of it is an operator's decision, and the endpoint
speaks of them no more. A re-hire with the same address is refused as taken until
an operator frees it.

**An erase is not something the directory hears.** Somebody erased here -- by an
operator, or by themselves -- is somebody it no longer finds, and making them
again is refused as taken, since an erase leaves their identity and their address
where they were. It fails for them every cycle until they are forgotten, and
then makes them again, as somebody new. So an operator unassigns a person in the
directory before erasing them here, and the people a directory provisions do not
delete their own account, whatever their tenant offers (`MeService.Leave`).

**The directory lifts its own suspensions and no other.** It is one way: it never
hears that an operator suspended somebody, and restarting its provisioning says
`active` for everybody in scope. So `Holder.directory` says whose a suspension is,
`Activate` refuses an operator's, an operator's `Disable` or `Enable` makes a
suspension theirs, and somebody the directory deleted is an operator's to bring
back ([operating.md](operating.md) § *Stopping somebody*).

## What it does with a group

| SCIM | roster |
| --- | --- |
| `id` | `Group.id` |
| `displayName` | `Group.name`; the alias is made from it once, and stays |
| `externalId` | the label `roster.directory` on the group |
| `members` | a `GroupMembership` each, for the people the endpoint speaks of |

**The directory's own holder makes the group, and so manages it.** Whoever
makes a group manages it ([entity.md](entity.md) § *GroupMembership*), and that
is all that makes a group the directory's to change: its holder is in each group
it made, holding the manager's role there, and its key holds the writes that
change a group -- who is in it, what it is called -- through that role and
through no binding. So the key reaches the groups it made and no other, by the
rule every manager is held to and none of the directory's own.

**The label is how it finds them, and no rule reads it.** A group it made
carries `roster.directory`, holding what the directory calls it. The endpoint
speaks of the groups that carry it and of no other: one an operator made is not
found, not changed, and **not taken over** by a directory naming a group alike
-- that create is refused as `uniqueness`, since handing a group to a directory
is deciding who says what its bindings reach.

**A role may be bound to such a group, or a client shown it, as to any other.**
Nothing is refused a directory's group that is not refused every group. What
follows is the administrator's to know when they do it: the directory's holder
is a member, so it holds what is bound to the group, and the directory then says
who else does -- which is what binding a role to a directory's group is for.

**What it is called follows the directory, and the alias does not.** A rename
writes `Group.name` (`Group.Update`). The alias was made from the first name and
is what a token carries for a client the group is shown to (`GroupExposure`), so
a rename in the directory never changes what a relying party grants on.

**Who is in it, as far as the directory is told, is the plain members.** Its
own holder is in every group it made and is no member to it; somebody an
administrator appointed to manage one beside it is theirs to take out. A member
is a person the endpoint speaks of -- a group inside a group is not followed,
and roster has none.

**`DELETE` takes everybody out and keeps the row.** A manager changes who is in
their group and not whether there is one, and `Group.Erase` on the key would
reach every group of the tenant nothing is bound to. So the group is still
answered afterwards, with nobody in it, a directory that makes one of that name
again finds it, and what becomes of the row -- and of whatever was bound to it
-- is an administrator's.

## What its key may do, and why not more

| method | what for |
| --- | --- |
| `HolderService/Provision` | somebody new: the person, their identity, their address, in one write |
| `HolderService/Deactivate`, `Activate` | a suspension of the directory's, for people who sign in through the connection |
| `HolderService/Update`, `Get` | what it owns of a profile, and reading it back |
| `HolderService/Tell` | what it calls somebody, kept beside them: reaches anybody, and is believed by nothing |
| `PhoneService/Add`, `Erase`, `List` | where somebody is reached: reaches anybody, and is no way in |
| `RelationService/Add`, `Erase`, `List` | who somebody reports to: a record no rule reads |
| `EmailService/Get`, `List`, `IdentityService/Get`, `List` | finding somebody by an address or an `externalId` |
| `MeService/Get`, `ConnectionService/List` | which tenant the key is, and its provisioning connection |
| `GroupService/Add`, `Get`, `List`, `GroupMembershipService/List` | making a group, which makes its holder the group's manager, and reading the tenant's |
| `GroupService/Update`, `GroupMembershipService/Add`, `Erase` | what a group is called and who is in it -- on the key, and **not bound**: held in each group it made, and so for that group alone |

**Not `Identity.Add`, not an address written or attested, not `Holder.Add`, not
`Erase`.** Each of those reaches people who already exist, and `Core.mayReach`
answers yes for everybody who holds nothing -- which is most people. A key a
directory's cloud presents from the internet would then be a way into all of them.
`Provision` writes ways in only into the person it makes in the same breath, so a
key holding it reaches the people it makes and no one else -- and only at the
organisation's own door, where the account it names is one the organisation made
(§ *Setting a tenant up*). `Deactivate` reaches
the people who sign in through the provisioning connection -- not an app's holder,
not a front door's.

**Not `GroupMembership.Add` through a binding, and not `Group.Erase` at all.**
Bound, the first reaches every group of the tenant: putting somebody into a
group nothing is bound to is asked nothing else, and a group shown to a client
is a permission somewhere roster cannot see. So `roster scim provision` binds
the directory's holder everything in the table but the last row, and mints a
key that lists it all -- a key's own list narrows what its holder holds, and
would otherwise narrow those three away. A key minted before groups were
provisioned here lists none of this: `provision` again, which is a rotation.

## Where each fact is

| | |
| --- | --- |
| `scim/` | the endpoint: auth, the routes, the mapping, both PATCH dialects |
| `server/core/directory.go` | `Provision`, `Deactivate`, `Activate`, and who a directory speaks for; `Tell`, and what is kept of what it says |
| `cli/scim.go` | `roster scim serve`, `roster scim provision` and `roster scim adopt` |
| `scim/groups.go` | a directory's groups: which it speaks of, and who is in one as far as it says |
| `server/core/group.go` | why its holder manages the groups it makes, and what a manager may do |
| `cmd/consumers.go` | the `scim:` block |
| `deploy/scim/` | the endpoint in the reference deployment: a component an overlay turns on, and the Service its ingress routes to ([deploy/README.md](../deploy/README.md) § *A directory provisioning people*) |
| `compose.yaml` | the same endpoint on a desk, `scim`, at `:8092` |

| the promise | pinned by |
| --- | --- |
| a person's whole life at the endpoint, in Entra's order and dialect | `TestADirectoryProvisionsSomebodyTheWayEntraDoes` |
| somebody already here is matched and not made again | `TestSomebodyAlreadyHereIsMatchedAndNotMadeAgain` |
| and is not suspended by their directory until they are linked; what it calls them is kept and believed by nothing | `TestSomebodyMatchedIsNotSuspendedByTheirDirectoryUntilTheyAreLinked` · `TestWhatADirectorySaysIsKeptAndBelievedByNothing` |
| an operator links who the directory matched, from a list | `TestAnOperatorLinksWhoTheDirectoryMatched` |
| a changed `userName` is kept and answered, and the address stays | `TestAChangedUserNameIsKeptAndAnsweredAndTheAddressStays` |
| a number is written and replaced, one a kind, and is no way in | `TestADirectoryKeepsSomebodysNumbers` · `TestANumberIsWhereSomebodyIsReachedAndNoWayIn` |
| who somebody reports to is one relation, and one word in it | `TestADirectorySaysWhoSomebodyReportsTo` · `TestWhatSomebodyIsToSomebodyIsARecordNothingReads` |
| a key minted before a method was added goes on provisioning without it | `TestAKeyMintedBeforeNumbersWereKeptStillProvisions` |
| the directory lifts only its own suspensions | `TestADirectoryLiftsOnlyItsOwnSuspension` · `TestTheDirectoryDoesNotLiftAnOperatorsSuspension` |
| its key reaches only the people it makes | `TestADirectoryMakesPeopleAndOnlyNewOnes` · `TestTheEndpointTakesATenantKeyAndNothingElse` |
| and makes them only at the organisation's own door | `TestADirectoryProvisionsThroughItsOrganisationsOwnDoor` |
| somebody erased is neither found nor made again until forgotten, and its people do not leave on their own | `TestSomebodyErasedHereIsNeitherFoundNorMadeUntilForgotten` · `TestSomebodyTheirDirectoryProvisionsDoesNotLeaveOnTheirOwn` |
| a group's whole life at the endpoint, in Entra's order and dialect | `TestADirectoryProvisionsAGroupTheWayEntraDoes` · `TestAGroupsMembersAreSaidTheRFCsWayToo` |
| its key changes the groups it made and no other | `TestADirectoryChangesTheGroupsItMadeAndNoOther` · `TestADirectorysKeyIsMintedForOneTenantAndOneConnection` |
| where a deployment turns it on, the endpoint reaches roster | `scripts/cluster.sh` and `scripts/hydra.sh`, *the directory's endpoint, asked by a key nobody minted* |
