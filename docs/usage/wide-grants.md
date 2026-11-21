# Grants that hand over more than they read as

A role is a list of method names, and most of them mean what they say. These
do not. Each is something a careful administrator has granted, or would, on
the strength of how it reads -- and each hands over an account, a tenant, or
everything a tenant ever wrote.

None of them is a defect. Each is in [rules.md](rules.md) as a rule that holds,
or as something roster leaves on purpose. What is here is what the rule
**lets through**: what somebody holding the grant can do that the name does
not say. Each example is something a served deployment did, or what a test
named beside it holds.

Read it before binding a role to somebody, before minting a key, and before
writing a role whose methods end in `*`.

| | reads as | is |
| --- | --- | --- |
| [A person's verb, across the tenant](#a-persons-verb-across-the-tenant) | *may manage keys*, *may add an address* | every colleague who holds no more |
| [`VouchService/Link` with `Redeem`](#vouchservicelink-with-redeem) | *may send recovery links* | every account of the tenant |
| [`VouchService/Accept`](#vouchserviceaccept) | *may sign people in* | a credential for anybody, on the caller's word |
| [`ConnectionService/Add` and `Update`](#connectionserviceadd-and-update) | *may set up sign-in through a directory* | everybody who signs in through it |
| [`EmailService/Verify` with `Confirm`](#emailserviceverify-with-confirm) | *may confirm addresses* | any address, proved with no mailbox |
| [`EmailService/Attest`](#emailserviceattest) | *may record a directory's address* | an address proved on the caller's word |
| [`NominationService/Add`](#nominationserviceadd) | *may install an app* | an app's key answered as somebody |
| [The same verbs at `admin.addr`](#the-same-verbs-at-adminaddr) | an operator's helpdesk role | any account in any tenant |
| [`BindingService/Add`](#bindingserviceadd) | *may assign roles* | everything its holder holds, to anybody |
| [A role bound to a group](#a-role-bound-to-a-group) | *this team gets this role* | its manager decides who holds it |
| [`GroupExposureService/Add`](#groupexposureserviceadd) | *may show a group to an app* | whatever that app grants on the name |
| [`SiteMembershipService/Add`](#sitemembershipserviceadd) | *may say who works where* | who is within a site's helpdesk's reach |
| [A pattern](#a-pattern) | `/roster.HolderService/*` | every method of it, and the next one added |
| [`HolderService/Disable`](#holderservicedisable) | *may suspend an account* | anybody's, the administrator's included |
| [`VouchService/LeaveLink` with `Leave`](#vouchserviceleavelink-with-leave) | *may mail a link that deletes an account* | anybody's account, deleted |
| [`AuditService`](#auditservice) | *may read the audit log* | what every write said, of every row |
| [`HolderService/SignsIn`](#holderservicesignsin) | *may see how somebody signs in* | everybody's keys and sessions, by name |
| [A deployment key](#a-deployment-key) | a key for one app | its list, in every tenant |
| [A key that mints keys](#a-key-that-mints-keys) | a key that lists one method | everything its holder holds |
| [A directory's key](#a-directorys-key) | a sync job | makes people, writes anybody's profile, reads everybody's addresses and accounts, and suspends the last administrator |
| [The `self-service` role](#the-self-service-role) | a role nobody is bound | on for everybody the moment a method is in it |

## Becoming somebody

### A person's verb, across the tenant

`ApiKeyService/Issue`, `CredentialService/Issue`, `/Enrol`, `/Unlock`,
`IdentityService/Add`, `/Resubject`, `EmailService/Add`, `PhoneService/Add` --
each writes a way into an account, and each takes a *whose*.

**Reads as** *may manage their keys*. **Is** *may write a way into anybody who
holds no more than they do* -- which, bound to two people, is each other.

> contoso wants its people to mint their own keys, and binds kim and park a
> role naming `ApiKeyService/Issue`.
>
> 1. kim calls `ApiKey.Issue {holder: park}`. A key.
> 2. `MeService/Get` with that key answers **park**. So does every product that
>    asks roster who is calling.

The rule that holds is that nobody writes a way into an account **wider** than
their own, and kim is not narrower than park. Somebody bound nothing at all is
reached the same way, by anybody who holds one of these.

**It is for** a helpdesk and an administrator: somebody whose work is other
people's accounts. **It is not what self-service is given by.** What somebody
does to their own account is their tenant's `self-service` role, which nobody
is bound and which reaches nobody else
([permissions.md](permissions.md) § *What somebody does to their own account*).
Bound in a site, one of these reaches the people of that site and no further.

### `VouchService/Link` with `Redeem`

**Reads as** *may send recovery links*. **Is** every account of the tenant.

> 1. Somebody whose role names both calls `Vouch.Link` for the administrator's
>    address. roster sends nothing: the link is answered **to the caller**,
>    who is expected to mail it.
> 2. They call `Vouch.Redeem` with it themselves, and are the administrator.

Neither is held to the rule about whose account a way in is written into, and
cannot be: a front door holds nothing, and has to mail a link to anybody who
asks at it. So the grant is the whole of the control.

**It is for** a front door -- the app a tenant's people sign in at -- and for
nothing and nobody else. `roster account provision` writes the role that names
them. `roster key add` says so when a key's methods name `Link`
(`TestALinkIsAWayInThatSomebodyElseDelivers`).

### `VouchService/Accept`

**Reads as** *may sign people in*. **Is** a credential for anybody, minted on
the caller's word that a provider signed them in. It checks nothing itself.

**It is for** a front door that ran the provider's flow, and `roster key add`
says so (`TestNobodyAcceptsWithoutBeingAllowedTo`). An app that checks a
password through `Vouch.Verify` does not need it.

### `ConnectionService/Add` and `Update`

**Reads as** *may set up sign-in through a directory*. **Is** the account of
everybody who signs in through that connection, an administrator's among them.

A connection says where a tenant's people are asked who they are, and roster
believes the answer. Somebody's account at a provider is kept by the
connection's **name**, so a connection moved to another issuer -- or erased and
added again under the same name -- is where those people are asked from the next
sign-in on, and whoever the new issuer says they are is who signs in.

> contoso binds its helpdesk lead a role naming `ConnectionService/*`, to set up
> a second directory. The connection the administrator signs in through keeps no
> secret. The lead moves its issuer to one they run, which says the
> administrator's subject, and signs in as the administrator.

What is kept: it is held across the tenant, so a role bound in a site writes no
connection; a tenant cannot move a connection that carries one of the
deployment's secrets, nor give one a secret of the deployment's
(`TestAConnectionsSecretGoesWhereTheDeploymentSays`); and a connection a file
declares is written from the file. What is not asked is who arrives through it,
and it cannot be: that is everybody, on purpose.

Where the front door links a first sign-in by address (`enrol: expected`), it
also reaches somebody who has never signed in through it: a new connection whose
issuer says a person's proved address is verified is linked to that person.

**It is for** whoever administers the tenant, and nobody narrower. `Erase`
alone signs nobody in.

### `EmailService/Verify` with `Confirm`

**Reads as** *may confirm addresses*. **Is** any address, proved, with no
mailbox read.

> 1. Somebody whose role names both writes `ceo@contoso.example` onto their own
>    account. Writing an address onto your own account is nobody's to refuse.
> 2. `Email.Verify` answers the link that proves it -- to the caller.
> 3. `Email.Confirm` spends it. The address is theirs, and proved.

A proved address names them to every relying party that reads one, and its
owner cannot take it back: a proved address does not change hands.

**It is for** a front door, which mints the link as itself and mails it. Neither
verb is somebody's own to call, and `Confirm` alone is one step from this the
day `Verify` is granted beside it.

### `EmailService/Attest`

**Reads as** *may record the address a directory handed over*. **Is** an
address proved on the caller's word, with no mailbox read and no link sent.

It is what a front door calls when somebody signs in through a provider that
says their address is verified: the address is written down as proved, on the
word of that sign-in. roster cannot see the sign-in. What it asks is that the
person has an account at a directory to be the word's source, and that they
hold no more than the caller.

> A key that lists `EmailService/Attest` names a colleague who signs in through
> the tenant's directory, and an address. The address is theirs and proved, and
> if somebody had written it onto their own account without proving it, it has
> been taken from them.

**It is for** a front door. Beside `Verify` with `Confirm` it is the shorter
road to the same place, and it is not somebody's own to call.

### `NominationService/Add`

**Reads as** *may install an app in the tenant*. **Is** naming who an app's
deployment key is answered **as** here. Whoever is named is who the app is,
with everything they hold.

It is held to the rule about a way in -- nobody nominates somebody wider than
themselves -- and to a binding across the tenant.

### The same verbs at `admin.addr`

`admin.addr` is the data plane with no wall, behind an operator's session. The
rule about whose account a way in is written into reads what the caller is
bound in the tenant, and an operator is bound nothing there, so **it is not
asked**.

**Reads as** an operator's helpdesk role. **Is** `CredentialService/Issue`,
`IdentityService/Add` or `EmailService/Add` on any account of any tenant, an
administrator's included. [operating.md](../operating.md) § *`admin.addr` is
root* says the same of the port.

## Handing out

### `BindingService/Add`

**Reads as** *may assign roles*. **Is** everything its holder holds, handed to
anybody -- no more than that, and all of it.

Nobody hands out what they do not hold, so this never makes more than there
was. What it does is spread: an administrator who gives one person
`BindingService/Add` beside `/roster.*/*` has let that person make
administrators. `RoleService/Add` and `/Widen` are the same act one step
earlier, since a role is written to be bound.

### A role bound to a group

**Reads as** *this group gets this role*. **Is** handing the role to whoever
manages the group, to give out.

> 1. kim may make groups, and makes `platform`. Whoever makes a group manages
>    it.
> 2. An administrator binds `deployer` to `platform`.
> 3. kim puts whoever kim likes into `platform`. Each of them is a deployer.

Putting somebody into a group asks whether the one doing it holds what the
group hands out, and a manager is in the group and holds it
(`TestAGroupBoundToARoleStillAsksWhoeverJoinsSomebody`). kim could not have
bound the role: that asks for `BindingService/Add` and for holding `deployer`.
But whoever binds it has chosen kim as who gives it out.

**Before binding a role to a group, read who manages it.**

### `GroupExposureService/Add`

**Reads as** *may show a group to an app*. **Is** whatever that app grants on
the group's name, which roster cannot see.

> contoso's cluster maps the group `cluster-admins` to its administrators.
> Whoever may write a `GroupExposure` naming that group and the cluster's
> client has decided who administers the cluster: everybody in the group, from
> their next token (`TestATokenCarriesTheGroupsShownToItsClient`).

It is held across the tenant for this reason
(`TestWhoAGroupIsShownToIsTheTenantsToSay`), and whoever manages such a group
holds the same decision from the other end.

### `SiteMembershipService/Add`

**Reads as** *may say who works where*. **Is** saying who is within reach of
every grant made in that site.

> Seoul's helpdesk resets the password of somebody of Seoul. Whoever puts
> lee, of Busan, into Seoul has given Seoul's helpdesk lee's password.

It is held across the tenant, since a site that could say who is in it would
say who is in its own reach.

### A pattern

**Reads as** `/roster.HolderService/*`. **Is** every method that service has,
and every one a later release adds to it, the day it is added.

`/roster.*/*` is the tenant: that is what `everything` holds, and the role a
tenant is made with. A pattern narrower than that is still a grant of methods
nobody had read when it was written.

## Stopping somebody

### `HolderService/Disable`

**Reads as** *may suspend an account*. **Is** anybody's they can see, wider
than themselves included.

> contoso's helpdesk is bound `HolderService/Disable` across the tenant. It
> suspends the administrator, whose keys and sessions stop at once.

It is not held to the rule about a way in, on purpose: it makes nobody anybody,
and somebody on call has to be able to stop a compromised account that is
wider than they are. What is kept is that a tenant is left with somebody
administering it. `HolderService/Invalidate` -- *sign them out everywhere* --
is the same.

So is `HolderService/Erase`. It reads as *may delete an account* and is
anybody's, an administrator's among them: whoever holds it across the tenant
erases somebody who holds more than they do, and what is kept is the last
administrator and a row a file declares. An erase destroys nothing and an
operator brings the person back (`roster restore`) until they are forgotten,
which is why it asks what suspending does and not what writing a way in does.

### `VouchService/LeaveLink` with `Leave`

**Reads as** *may mail a link that deletes an account*. **Is** anybody's
account the caller can see, deleted, where the tenant lets its people leave.

`LeaveLink` answers the link to its caller, for the caller to mail, as
`VouchService/Link` does; `Leave` spends one and asks for the person's alias,
which the caller knows.

> A key that lists both names the tenant's administrator and one of their
> proved addresses. It is answered the link, and spends it.

What is kept: the address has to be one of the person's that somebody proved, a
tenant has to offer leaving at all (`TenantConfig.leave`), a tenant is left with
somebody administering it, and an operator can bring the account back until it
is forgotten.

**It is for** a front door, which asks about whoever is signed in at it and
mails the link to them. Neither verb is somebody's own to call.

## Reading

### `AuditService`

**Reads as** *may read the audit log*. **Is** what every write said: a line
of the trail carries what the write set, for rows long since erased too. It
carries the row the write left as well only for a kind the deployment keeps at
`audit.content: value`; at the default that is empty, and a line is what was
set and by whom. Not what the schema calls personal or secret -- names,
addresses and verifiers are on the trail as having been set, and never as what
to.

It is held across the tenant. On a deployment key it is every tenant
(`TestADeploymentKeyReadsEveryTenantsTrail`), and minting a key says which it
is: `roster control key add` of every tenant's, `roster key add` of the key's
own (`TestAKeyIsToldWhatItsOwnKindReads`).

### `HolderService/SignsIn`

**Reads as** *may see how somebody signs in*. **Is** every person's keys by
name, what each key may call, and where they are signed in. Held across the
tenant, it is what turns `ApiKeyService/List` from *my keys* into
*everybody's* (`TestAnOperatorSeesHowSomebodyElseSignsIn`).

**It is for** an auditor and an administrator.

## Keys

### A deployment key

`roster control key add` mints an `rk_`. **Reads as** a key for one app. **Is**
its list of methods in **every tenant** of the deployment, unless it is
narrowed -- and narrowed, it is whoever each tenant nominated for it.

> A key minted with `/roster.HolderService/*` for an app that manages one
> customer's people lists, suspends and renames every customer's.

`--narrowed` is the key for an app that always knows which tenant a request is
about. A key that reads across tenants on purpose -- a job that exports the
trail -- is the other kind, and should be the only kind that is not narrowed
([ways-in.md](ways-in.md) § *A deployment key*).

### A key that mints keys

A key, or a delegation, that lists `ApiKeyService/Issue`. **Reads as** a
narrow credential: its list says one method. **Is** as wide as whoever it
is for. What a caller may put on a key is what its **holder** is bound, and
the list on the credential it called with is not asked -- so a key that lists
`Issue` and nothing else mints a second key, for its own holder, listing
anything they hold.

> A build job is given a key on an administrator's account listing
> `ApiKeyService/Issue` alone, to mint short-lived keys for its steps.
> Whoever reads that job's environment mints one listing `/roster.*/*`.

It is meant. A terminal is signed in this way: the account page's delegation
lists the dozen methods the page calls, and when somebody approves
`roster sign-in` it mints the key the terminal asked for, as wide as the
person approving and no wider
(`TestATerminalIsNeverAllowedMoreThanWhoeverApprovesIt`). So a key that has to
be narrow does not list `Issue`, and a key that has to mint is put on a holder
bound only what it should hand out -- which is the bound that is asked
(`TestAKeyThatMintsKeysMintsWhatItsHolderHolds`).

A **delegation** is the other way round: an app is handed one for the methods
it may itself call, by its key's list and by what it is bound, and for no other
([rules.md](rules.md)).

### A directory's key

**Reads as** a sync job. **Is** a key on the internet that makes people in the
tenant, and suspends whoever signs in through its connection -- the tenant's
last administrator among them, whose word about who works there is not refused
(`TestADirectoryMaySuspendTheLastAdministrator`).

It makes only new people, writes no way into anybody already here, and lifts
only a suspension it made ([scim.md](../scim.md)). It reaches more than the
people it makes and signs in, and that is decided:

- It writes the profile of anybody of the tenant no file declares, an
  administrator's among them, and who they report to
  (`TestADirectorysKeyWritesTheProfileOfAnybodyNoFileDeclares`).
- It makes groups, manages the ones its holder made, puts anybody of the tenant
  in them, and erases them with what was bound to them
  (`TestADirectorysKeyPutsAnybodyOfTheTenantInTheGroupsItMakes`).
- It reads every person's addresses, accounts at providers and numbers
  (`TestADirectorysKeyReadsEveryPersonsAddressesAccountsAndNumbers`).
- The directory is whoever holds these verbs: nothing ties them to the key
  `roster scim provision` minted, except that only the holder who made a group
  manages it (`TestTheDirectoryIsWhoeverHoldsItsVerbs`).

What it can do with those is enough to matter: whoever holds it can lock a
tenant out, and rename its administrator.

## What everybody holds

### The `self-service` role

**Reads as** a role nobody is bound. **Is** held by everybody of the tenant,
over what is their own, the moment a method is in it.

It names only what is somebody's own to do, and a write that would put
anything else there is refused. Of what it does name, one is worth knowing:
`IdentityService/Add` writes an account at a provider onto the caller's own
row with no proof that the account is theirs. Not at the connection the
tenant's directory provisions through: an account there is who the directory
means by somebody, so it is the directory's to say, and one's own write is
refused ([scim.md](../scim.md)). A tenant whose people do not link a second
directory themselves takes it out:

```sh
roster --as admin role narrow @newco/self-service '{"methods":["/roster.IdentityService/Add"]}'
```

## What says so when one is granted

**A list of methods can be asked.** `RoleService/HandsOver` answers this page
for one list: which of its methods are here, what each reads as, and what it
is where it would be held. It is what a page asks before a role is bound,
widened or put on a key, so that what it says beside the button is this page
and not a copy of it.

```sh
roster --as admin role hands-over '{"methods":["/roster.ApiKeyService/Issue","/roster.HolderService/Get"]}'
```

```
grants (1)
  grant      a-persons-verb-across-the-tenant
  methods    (1)
    /roster.ApiKeyService/Issue
  reads_as   may manage somebody's keys, password or addresses
  hands_over a way into the account of anybody of the tenant who holds no more than whoever is given it -- which, given to two people, is each other
```

`grant` is the heading here, as its link is written. `methods` is which of the
ones asked about reach it, as they were written, so a pattern is answered for
everything it covers. A list that means what it says is answered nothing.

Where it would be held is `held`, and it changes the answer, because a sentence
said of the wrong place is one nobody reads the next time:

| `held` | |
| --- | --- |
| `ROLE_HELD_ACROSS_THE_TENANT` | a binding that names no site. Not said is read as this, the widest |
| `ROLE_HELD_IN_A_SITE` | a binding that names a site, or a role held in a team. A person's verb reaches the people of that site; what is held across the tenant or not at all -- the trail, `SiteMembershipService/Add` -- hands over nothing there and is not answered |
| `ROLE_HELD_NOWHERE` | the tenant's `self-service` role, which reaches nobody else |

Asked of the control plane -- `roster control role hands-over` -- the answer is
a deployment's: every tenant, since that is where an operator's role and a
deployment key are exercised.

Three entries above are not about a method and are not answered: a role bound
to a group, a deployment key, and a directory's key. Whoever is making one of
those knows it is one.

**A key is told at the terminal.** `roster key add` and `roster control key
add` print a note when a key's methods reach the trail, `VouchService/Accept`
or `VouchService/Link`, and the note is the one for that kind of key -- a
tenant's, or every tenant's.

**Nothing refuses.** A page that asks may still not say, and `roster role add`
and `roster binding add` print nothing of their own. Whether a write should
wait until it is said to be meant is
[#310](https://github.com/lesomnus/roster/issues/310).
