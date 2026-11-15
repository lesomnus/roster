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
| [`EmailService/Verify` with `Confirm`](#emailserviceverify-with-confirm) | *may confirm addresses* | any address, proved with no mailbox |
| [`NominationService/Add`](#nominationserviceadd) | *may install an app* | an app's key answered as somebody |
| [The same verbs at `admin.addr`](#the-same-verbs-at-adminaddr) | an operator's helpdesk role | any account in any tenant |
| [`BindingService/Add`](#bindingserviceadd) | *may assign roles* | everything its holder holds, to anybody |
| [A role bound to a group](#a-role-bound-to-a-group) | *this team gets this role* | its manager decides who holds it |
| [`GroupExposureService/Add`](#groupexposureserviceadd) | *may show a group to an app* | whatever that app grants on the name |
| [`SiteMembershipService/Add`](#sitemembershipserviceadd) | *may say who works where* | who is within a site's helpdesk's reach |
| [A pattern](#a-pattern) | `/roster.HolderService/*` | every method of it, and the next one added |
| [`HolderService/Disable`](#holderservicedisable) | *may suspend an account* | anybody's, the administrator's included |
| [`AuditService`](#auditservice) | *may read the audit log* | what every write said, of every row |
| [`HolderService/SignsIn`](#holderservicesignsin) | *may see how somebody signs in* | everybody's keys and sessions, by name |
| [A deployment key](#a-deployment-key) | a key for one app | its list, in every tenant |
| [A directory's key](#a-directorys-key) | a sync job | makes people, and suspends the last administrator |
| [The `self-service` role](#the-self-service-role) | a role nobody is bound | on for everybody the moment a method is in it |

## Becoming somebody

### A person's verb, across the tenant

`ApiKeyService/Issue`, `CredentialService/Issue`, `/Enrol`, `/Unlock`,
`IdentityService/Add`, `EmailService/Add`, `PhoneService/Add` -- each writes a
way into an account, and each takes a *whose*.

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

## Reading

### `AuditService`

**Reads as** *may read the audit log*. **Is** what every write said: a line
of the trail carries the row as the write left it, for rows long since erased
too. Not what the schema calls personal or secret -- names, addresses and
verifiers are on the trail as having been set, and never as what to.

It is held across the tenant. On a deployment key it is every tenant
(`TestADeploymentKeyReadsEveryTenantsTrail`), and `roster key add` says so
(`TestMintingAKeyThatReachesTheTrailSaysSo`).

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

### A directory's key

**Reads as** a sync job. **Is** a key on the internet that makes people in the
tenant, and suspends whoever signs in through its connection -- the tenant's
last administrator among them, whose word about who works there is not refused
(`TestADirectoryMaySuspendTheLastAdministrator`).

It makes only new people, writes no way into anybody already here, and lifts
only a suspension it made ([scim.md](../scim.md)). What it can do with those is
enough to matter: whoever holds it can lock a tenant out.

## What everybody holds

### The `self-service` role

**Reads as** a role nobody is bound. **Is** held by everybody of the tenant,
over what is their own, the moment a method is in it.

It names only what is somebody's own to do, and a write that would put
anything else there is refused. Of what it does name, one is worth knowing:
`IdentityService/Add` writes an account at a provider onto the caller's own
row with no proof that the account is theirs. A tenant whose people do not
link a second directory themselves takes it out:

```sh
roster --as admin role narrow @newco/self-service '{"methods":["/roster.IdentityService/Add"]}'
```

## What says so when one is granted

Today, three of these are said at the moment of granting, and only for a key:
`roster key add` prints a note when a key's methods reach the trail,
`VouchService/Accept` or `VouchService/Link`.

Nothing says it of a role or of a binding, at a terminal or on a page. That a
page should, before the write, is
[#310](https://github.com/lesomnus/roster/issues/310).
