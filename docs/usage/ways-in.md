# Ways in

Creating somebody writes **no credential**. This page is the ones you can write
for them, what each is, and who checks it.

## The rule that decides which is which

> roster stores facts and verifies claims about them. It never issues anything a
> third party verifies.

Ask **who checks this?** If only roster can — a password, a TOTP code, a key
somebody introspects here — it is roster's to hold. If somebody else has to be
able to check it without asking — a signed token, a session cookie for another
app's browser — it is not roster's to make, and the answer is Hydra or the app's
own session.

So there is no "roster login token". What roster answers is *yes, that was
them*, and what an app does with the answer is the app's.

## At a glance

| | what it is | who presents it | where it is checked |
| --- | --- | --- | --- |
| a **password** | an argon2id verifier on a `Credential` row | a person, to your front door | `VouchService.Verify`, here |
| **`rk_`** deployment key | a control-plane row; presents **as itself**, or -- naming a tenant with `roster-at` -- as the holder that tenant nominated for it | an app you run across tenants | the auth interceptor, every request |
| **`rt_`** tenant key | a data-plane row; **resolves to its holder** | a customer's script, or their app | the same interceptor |
| **`rd_`** delegation | short-lived, bound to the key that asked for it | an app, *beside* its own key | the same interceptor |
| an **identity** | `(tenant, provider, subject)` — an account somewhere else | nobody; it is a fact, not a secret | whoever verified the provider |
| a **second factor** | TOTP seed, or a WebAuthn credential | a person, after the first factor | `VouchService.Verify` |

## A password

For a person at a browser. Two ways to write one:

```sh
roster --as admin vouch reset @newco/alice                        # generated here, printed once
printf '%s' "${PW}" | roster --as admin vouch set --password-stdin @newco/alice
```

`reset` generates thirty-two random bytes, because a secret the caller chose is
a secret the caller knows. `set` writes one somebody else chose — never as an
argument, since an argument is in the shell history and in the process list.

Neither can tell you what a password *was*: what is stored is a hash.

An account that wrong answers closed opens again by itself, and sooner if you
say so:

```sh
roster --as admin vouch unlock @newco/alice
```

The same three acts are on the admin console, on a person's own panel. Nothing is only
in one of them.

### If the one locked out is the operator

The operator `roster init` made lives on the **control plane**, a different
database from the people above, so the same three are under `roster control`:

```sh
roster --as admin control vouch reset @admin                      # generated here, printed once
roster --as admin control vouch unlock @admin
```

This is the way back for a deployment with **one** operator who has lost the
password `init` printed: every other door that writes a password needs a
credential, and theirs is the one gone. Use `reset` rather than `set` for it:
`reset` also signs out everything the old password opened, because a recovery
that leaves the old sessions alive is not one. `roster control vouch set` is
there too, and changes the password only.

The person is looked up and never created, so a typo is refused rather than
becoming a second operator. A second operator is the admin console's
*issue a password*, or `roster issue password`.

### If a deployment names a leaked-password corpus

```yaml
vouch:
  breached: /var/lib/roster/pwned-sha1.txt
```

Then every door refuses a password somebody has already lost, including these
commands and including a reset — which is a fact about the secret rather than
about the door it came through. The file is SHA-1, uppercase hex, one per line,
**sorted**; roster checks the order at startup, because an unsorted file answers
*no* to things that are in it.

### Using it

Your front door — a login app, an account portal, a product's session
endpoint — asks roster:

```
VouchService.Verify { who: {tenant: {alias: "newco"}, alias: "alice"}, secret: … }
  → { ok: true, holder: …, tenant: … }
```

and then mints whatever **it** uses for a session. roster does not mint that,
which is the rule at the top of this page.

The tenant and the alias are what somebody typed into a form, and roster folds
them to small letters before it looks: `Alice` at `Newco` is `alice` at
`newco`, as `Alice@Newco.Example` has always been `alice@newco.example`. It is
the same at `Delegate`, at a mailed link, and at roster's own sign-in. Nowhere
else: a reference in any other call is exact.

If your front door would rather be handed something it can call roster with,
`VouchService.Delegate` is `Verify` plus an `rd_` — see § a delegation.

## A deployment key — `rk_`

For an app **you** run -- you being a roster operator -- that has to reach more
than one tenant, or that a tenant cannot be asked to hold a key for: the Login
App, a product you host for your customers, a job that reads the trail. The key
hangs off a row of the control plane, which is why the command says `control`:
it writes to the other database.

It answers two ways. **Unnarrowed**, as itself, across every tenant, allowed
exactly its `--allow` -- the job reading the trail, or the Login App asking which
tenant a name is before it knows whose flow a challenge is. **Narrowed**, with `roster-at: <a name
a tenant answers at>`, as the holder that tenant nominated for this key, with
that holder's bindings: everything a product does for one of your customers.
[apps.md](../apps.md) is which of those an app needs, and the shapes it can take.

A key narrowed to a tenant needs that tenant to have nominated somebody for it,
which `roster app install --tenant T --allow … NAME` does: the holder, its role,
the binding and the nomination, once -- and with `--serves`, the app's first
role at itself and roster's verbs for it ([apps.md](../apps.md)).

Something a **tenant** runs -- their CI, their sync job, their own copy of an app
-- is a holder in their tenant with an `rt_` ([below](#a-tenant-key--rt_)).

```sh
roster --as admin control key add --allow '/roster.VouchService/Verify,/roster.MeService/Get' loft
```

```
rk_rCHP-AyXhX7cbNIjgLLL8udW6hOchSQKHzRkcpMWWwc
key 01a03322-… for @loft, allowing 2 method(s). This is the only time it is shown.
```

Printed to **stdout** and the sentence to stderr, so `$(roster control key add …)`
is the key and nothing else. Shown once; what is stored is a hash.

Naming a holder **creates** it. A caller of your own is not a row anybody sets
up on purpose before they need it, and the control plane has one tenant so an
alias names one of them.

`--allow` is required in both directions: everything hands out more than anybody
asked for, and nothing mints a key that silently does not work. The one key with
nothing on it that works is said so:

```sh
roster --as admin control key add --narrowed levyan
```

It is refused as itself and answered as whoever each tenant nominated -- the key
for a product that always knows which tenant a request is about, so a request
that forgets `roster-at` is refused rather than run across all of them.

Write it however reads best — the flag repeats, and each occurrence may itself
be a comma-separated list:

```sh
roster --as admin control key add \
  --allow /roster.VouchService/Verify \
  --allow /roster.MeService/Get \
  loft
```

Flags come before the name, as everywhere in this CLI.

**A deployment key is not walled by tenant when it is not narrowed.** It is the
widest credential this deployment issues. Some methods are wider than they look and the command says so
when you grant one:

```
NOTE: `/roster.AuditService/List` reaches the audit trail, which holds the contents of every
write in this deployment, in every tenant, for as long as the retention policy
keeps them.
```

`--expires 720h` bounds one. Empty is forever, which is what an unattended app wants.

**At the control listener it is whose it is.** A key's list names methods and no
plane, and `control.addr` answers the same services over other rows: there
`HolderService` is the people who run this deployment, and `ApiKeyService` the
keys of the apps they run. So there a key calls what its **holder** was bound on
the control plane, of what its own list names -- and the holder that
`roster control key add` made for `loft` above is bound nothing, so a key made
for an app calls nothing there:

```
/roster.HolderService/List: at this listener a key calls what its holder was bound on the control plane, and no role of theirs allows that
```

A key for a job that does manage the deployment's own rows hangs off somebody
who may: an operator -- `roster control key add --allow … admin` names one who
is there, and makes nobody -- or a holder of the control plane bound a role for
the job with `roster control role add` and `roster control binding add`, as an
operator is.

**And it is not the deployment.** A deployment key calls its list in every
tenant, and is held in calling it to what anybody at a port is: it does not take
a tenant's last administrator away, or the deployment's last operator
([permissions.md](permissions.md)), and does not write a row a file declares
([operating.md](../operating.md#declared-rows)). The first is a command's at
the box and an operator's at `admin.addr`; the second is the file's.

## A tenant key — `rt_`

For **one of a customer's people**, or their machine. It resolves to that
holder, so a call made with it is made *as them* — behind the wall, narrowed to
their tenant, and never wider than what they hold.

```sh
roster --as admin key add --tenant newco --holder alice --allow '/roster.HolderService/Get'
```

Naming a holder who is not there is a **refusal**, unlike `roster control key
add`. A customer's people are the customer's, and a command that made one by
mentioning them would write rows into somebody else's tenant by typo.

**It is told what it reaches, which is its tenant's.** A method wider than it
reads is said when the key is minted, as it is of a deployment key, and what
is said is this kind's: the trail a tenant key reads is its own tenant's, where
whoever it is for is allowed the read across the tenant.

```
NOTE: `/roster.AuditService/List` reaches the audit trail of its tenant, which holds the
contents of every write there, of rows long since erased too, for as long as
the retention policy keeps them.
```

Three other ways to get one, all the same row:

| | |
| --- | --- |
| `ApiKeyService.Issue` on `server.addr` | a customer's own admin, for somebody in their tenant |
| `ApiKeyService.Issue` on `admin.addr` | an operator, from the admin console |
| `ApiKeyService.Issue` with your **own** reference | a person, for themselves — the same verb, and the app or shell is what passes only their reference |

As themselves, from their own terminal, that last one is:

```sh
roster me get                                        # what you are, and your keys
roster me issue-key --name laptop --allow '/roster.MeService/Get'
roster me revoke-key 01a03382-5adb-8788-bf0e-c99210575d01
```

`roster me` is remote only. Every method answers from the caller and a local run
has none — it opens the database instead of calling a server.

**An app password is one of these.** A client that has a password field and
nothing else -- an LDAP client, a mail client -- cannot pass a second factor,
so what goes in that field is a key minted for that app alone, with `Me.Get`
as its only method: enough for `roster ldap serve` to read whose it is, and
nothing else if it leaks. The account page mints one from the app's name
(*mint an app password*); revoking it from the same list ends that one app.
[../ldap.md](../ldap.md) § The bind.

Two rules hold whichever door was used, and they are why this is safe to offer
at all:

- **Nobody mints a key wider than themselves.** The methods on it must be ones
  the minter holds.
- **Nobody mints a key on an account they cannot reach.** A key *is* the person,
  so one written on the administrator's row is a credential for the
  administrator.

The local CLI is outside both, like every local command -- see
[cli.md](cli.md) § "Two modes".

### A key for an app

A key says **the app it is spent at** (`ApiKey.app`, the holder that app is
answered as in the tenant), and none is a key of roster's own -- every key
above. A key for an app names that app's methods, and is held to what its
minter holds **at that app**: somebody who holds `/loft.BookingService/Cancel`
at Loft mints a key for Loft naming it, and is refused the same key for levyan,
or for roster.

```sh
roster --as admin key add --tenant newco --holder alice --app loft --allow '/loft.BookingService/Cancel'
```

`ApiKeyService.Issue` takes `app` the same way, on this plane alone: an `rk_`
is the deployment's own and is for no app. So does a person minting their own:
`roster me issue-key --app loft …`, and the account page's form to mint a key,
which takes the app's alias and leaves it empty for one of roster's. The app is somebody of the key's
tenant, and nothing more is asked of it.

**It is no credential at roster.** Presented here -- in `authorization`, or
beside `roster-as` -- a key for an app is refused as a key roster does not have
is, and nothing says whose it is: its methods are the app's, and a name they
share with one of roster's (`/roster.HolderService/Erase` written into a key for
Loft) opens nothing here. What somebody is told of their keys, by `MeService.Get`
and `HolderService.SignsIn`, says each one's `app`, and its alias in `app_alias`;
the account page draws it `@loft`, and `roster me get` and `roster key list`
print `app=@…`, each saying nothing of a key of roster's. What ends the app's
holder ends the keys that are for it, whoever's they are.

### A terminal, signed in from a browser

A machine with no browser -- a server over SSH, a container, somebody's laptop on
a fresh checkout -- gets its key without anybody copying one:

```sh
roster sign-in --at https://account.contoso.example \
  --name laptop --allow /roster.MeService/Get --out ~/.roster/key
```

```
  open      https://account.contoso.example/device
  and type  WDJB-MJHT

waiting, for up to 15m0s...
```

Somebody opens that page in a browser they are **already signed in at**, types the
eight characters, sees what the terminal asked to be allowed, and says yes; the
command writes the key and stops. That is RFC 8628, the device grant -- the same
flow every CLI with a *go here and type this* screen uses -- and it is polling
rather than anything cleverer, because a machine in that position has nothing that
can be called back. A poll is posted, with the terminal's own code in its body
and in no address; a wrong code typed costs whoever typed it, ten of them stop
any code of theirs being answered for a quarter of an hour; one code is one key,
whoever says yes how many times; and a front door holds a thousand terminals
waiting and tells the next to come back.

What it says to you is on standard error; without `--out`, the key is the one
line on standard output, so `roster sign-in … > key` keeps the key and the
terminal still shows what to type.

What comes out is an ordinary `rt_`: it belongs to the person who approved it, is
**never wider than they are**, appears in their keys beside the app passwords, and
is revoked from the same list. Two consequences of that worth knowing before the
first one:

- **A deployment has to turn this on**: `account.terminal`, off unless said,
  because it is a door into an account. Off, the command is told so rather than
  waiting (`docs/operating.md` § "One process, or five").
- **You can only sign a terminal in for methods your own role names.** A key acts
  as you, so `ApiKeyService/Issue` refuses one naming anything you do not hold --
  and a *waived* method is not a held one, so a role that names nothing cannot
  approve even `Me.Get`. What that means in practice is that the people who are to
  do this hold a role naming both:

  ```sh
  roster --as admin role add @contoso/terminals \
    '{"methods":["/roster.ApiKeyService/Issue","/roster.MeService/Get"]}'
  ```

  It is the same constraint an app password is under, and it is deliberately not
  special-cased: the rule that refuses it is the one that stops anybody handing out
  more than they hold, and a waiver is about *calling* rather than about holding.
- **It is the front door that does this, not roster.** `--at` is an account app,
  which is the thing that holds a key, knows which operator a host belongs to and
  can see who is signed in. `account/device.go` says why roster itself answers
  none of it.

> **The other device grant.** A deployment with Hydra in front has a second one,
> and it is not this: the Login App's `/device` screen, where a person types a code
> a **television** printed so that the device gets an **OAuth token** out of the
> issuer. Same RFC, different caller, and neither replaces the other -- this one
> hands out an `rt_` and that one hands out nothing roster issues. See
> [../login.md](../login.md) § *A device with no browser*.

## A delegation — `rd_`

For an app that is drawing a person their own record and should not do it with
its own reach.

It is minted by `VouchService.Delegate` (a password checked, and a credential
back in one call), `Accept` (a front door that verified an SSO token vouches for
who it saw), or `Redeem` (a magic link). It lives for minutes, is single-use in
the ways that matter, and is **bound to the key that asked for it**.

It rides beside that key rather than instead of it:

```
authorization: Bearer rk_…      # who is presenting
roster-as:     rd_…             # who the call is about
```

Both, and that is what makes it checkable — a delegation on its own is refused.
It cannot be presented with `auth.Plain` or mTLS at all, because those name a
caller without resolving to a row and there is nothing to compare.

An app asks for one at the moment it needs it, and a shell can too:
`roster vouch delegate` prints one for scripting the same flow, bound to the
key in `client.auth` exactly as an app's would be.

### What a key means, to the app that was handed one

A token somebody pastes into a script means something only here, so the app that
receives it has to ask:

```
payday.TokenService/Introspect { token: "rt_…" }
  → who it stands for, and what it was narrowed to
```

The app asks with **its own** key, which is the whole of the trust decision and is
per app. Two things about the answer: it is about the **holder** rather than the
key, so an app resolves it against its own rows; and it sees only **this plane's**
keys -- the `rk_`s `roster control key add` mints live in the other database, and there is
no query from one to the other, so they are not refused here, they are invisible.

## Somebody's own record

```
MeService.Get {}      # takes nothing, answers about the caller
```

Their identifiers, addresses, teams, every method they may call, and the three
ways in that resolve to them -- the credentials roster holds, the provider accounts
somebody else does, and the keys that act as them. The method list is the union the
server enforces, so what a page shows and what it is allowed to do cannot drift.

It needs **no role**: requiring one to learn that you hold none is a deployment
where a new account cannot be told what it is for. `MeService/SignOutEverywhere`
is waived for the same reason -- it cannot be pointed at anybody else, and only
a subject-less method can be waived at all. Taking one of your own accounts at
a provider away is `IdentityService/Erase` with its identifier, as an address
is `EmailService/Erase`: the tenant's `self-service` role, which everybody
holds over what is their own, names it.

### Changing your own password

```
CredentialService.Set { ref: <you>, current: …, secret: … }
```

It asks for the password you hold, and **signs you out everywhere**: every
session and delegation the old one opened, the one that made the change among
them. A password is changed because somebody else may have it, and whoever
that is was signed in already. So the next thing a page does after *changed*
is sign in again, with the new one. It is the stamp `HolderService/Invalidate`
leaves, so a key is not touched: a key is revoked by name. A link already out
for you goes with the sessions -- a recovery link, one that deletes the
account, one a front door handed a browser -- and so does a sign-in that was
waiting for its second factor. A link that confirms an address does not, since
it signs nobody in.

### Deleting your own account

```
MeService.Leave {confirm: "alice"}   # your alias, as it is
```

Where the tenant offers it (`config.leave`, off unless somebody turned it on),
a person deletes their own account: their holder is erased the way an operator's
`roster holder erase` erases one, after they are signed out everywhere -- both,
or neither: an erase that is refused, as the last administrator's is, leaves
them signed in. Nothing
is destroyed yet -- the operator can `roster restore` them while
`holder.forget_after` lasts, and they are forgotten from when it ends, by the first
pass after it -- and every app
holding a session for them hears it on the sync stream.

**Until they are forgotten, they cannot simply come back.** Their alias is free
at once, and their addresses and their accounts at providers are still theirs,
for a restore to give back. So signing in again through the provider they used,
or through one that vouches for an address they proved, makes them no new
account, whatever the front door's `enrol`: they are answered *this account was
deleted here; an administrator of your organisation can bring it back, or let it
go*. And `holder.forget_after` is **never** unless the deployment set it, which
makes that for good. A product that lets its people leave and come back sets it
([operating.md](../operating.md) § "Destroying somebody, which an erase does
not"); otherwise each of them waits for an operator to forget them.

Leaving is not for somebody their tenant's directory provisions, though: whoever
signs in through the connection it provisions through is the directory's to
remove ([scim.md](../scim.md)), and `MeService.Get` names that connection as
`provisioned_through`, so the account page offers them no such section.

It is waived like the two above, and asks for one more thing they do not: that
the person **proved who they are a few minutes ago** (`server/me.LeaveWithin`,
five), by the sign-in the credential the call came with carries -- its
`date_authenticated`, and never when the credential itself was minted. The
account page's is the sign-in made there, and a page open since the morning signs
in again first and comes back to the section; through a directory, it asks the
directory to authenticate them again (`max_age=0`, and `prompt=login` at
Microsoft's), and what counts is when the directory says it did (`auth_time`).

Not every directory can say. GitHub never does. Google takes no request to
authenticate anybody again, and says when it last did only to an app that asks
for it by name -- every round trip to it does -- and only where the app's Google
project lets it ([operating.md](../operating.md#google)): a sign-in through
Google counts when Google has just asked them, which it does in a browser it has
not signed in, and never where the project does not let it say. Somebody whose
ways in are those signs in with a password instead, where they have one -- and a
first password of their own asks for what deleting does, a sign-in a few minutes
old (`CredentialService/Set`), so the session that could not delete cannot get
them one either: a way in that outlives the session asking for it is worth more
to whoever holds that session than the deletion was.

Their road is a **link that deletes the account**, and does nothing else. The
account page offers it when it refuses them, and mails it to an address on
their account that somebody proved: `VouchService.LeaveLink`, which the front
door asks about the person signed in at it, for the address they pick. It lasts
a quarter of an hour (`server/vouch.LeaveFor`, the recovery link's window, for
the recovery link's reason), and following it opens a page that asks for their
alias, typed, before anything happens -- so a mail client previewing the link,
or a scanner following every link in a message, deletes nothing. The answer
spends it (`VouchService.Leave`), and they leave as `Leave` would have them
leave: signed out everywhere, erased as themselves, restorable by the operator
until they are forgotten.

It is held to every rule `Leave` asks but the age of a sign-in, which the
mailbox has just proved instead: the tenant offers leaving, its directory does
not provision them, no file declares them. Those are asked when the link is
minted, so that nothing is mailed that could not be spent, and again when it is
spent, since anything may have changed in between; a mistyped alias, or any
other refusal, leaves the link unspent. The address has to still be theirs and
still proved when it is spent, and nothing may have ended their sign-ins since
it was minted: a password changed or issued, or *sign out everywhere*, ends a
link already out as it ends a session. And it signs nobody in: it mints no
delegation, no session and no password, and the doors the other links are
spent at -- `Vouch.Redeem`, the user console, `Email.Confirm` -- each refuse
it. A caller allowed both methods can delete anybody it can see, which is why
they are a front door's grant and nobody's own; from a shell, as the front
door's key, they are `roster vouch leave-link` and `roster vouch leave`.

A **recovery link**, where the tenant offers passwords, is the longer way round:
it hands them a password, and a sign-in with it is a moment ago. It is a way in,
to somebody who wants to leave, which is why the page offers the other link
instead.

And that is where the cost lands. A link can be had only where the deployment
can send mail -- `account.mail`, the SMTP server the front door delivers through
([operating.md](../operating.md) § *The front door's mail*): roster mints the
link and sends nothing, and the front door sends it -- and only to an address that
was checked. Where the block is left out, as it ships, somebody whose only way in
is GitHub, or Google without the setting, cannot delete their own account, the
account page says so, and whoever runs the tenant does it for them (`roster holder
erase`).

From a terminal it is a key handed over in a browser, which carries **the
browser's** sign-in:

```sh
roster sign-in --at https://account.contoso.example --allow /roster.MeService/Leave   # approve it at a page you signed in to a moment ago
roster me leave --confirm alice                                                       # within five minutes of that sign-in
```

That road is narrower than `Leave` itself, and it is worth knowing why before
offering it. `roster sign-in` mints a key, and minting one is `ApiKeyService/Issue`,
which is not waived: a person whose role does not name it has no terminal road,
and deletes their account from the account page. And the key is only as recent as
the browser's sign-in, so approving it from a page signed in an hour ago is a key
an hour old in the only sense that counts.

A key issued from another key carries that one's sign-in however new it is, so a
key that has been in a config file for a month mints nothing fresher. A key an
administrator minted for somebody carries no sign-in of theirs at all, and neither
does a session at the admin console or the user console, nor a directory that does
not say when it last authenticated them -- GitHub, which has no such claim, and
Google where its project does not let it: all are refused with *sign in again*.

Everything else a person does about their own row is the **operator's verb with
their own reference**: `IdentityService/Erase` to take a provider account
back, `ApiKeyService/Issue` and `/Erase` for their own keys. Attaching a
provider account is a front door's word, `IdentityService/Attest`, below. There is no self-only twin
of a verb anywhere in roster. What lets somebody call one about themselves is
their tenant's `self-service` role, which everybody of it holds over what is
their own and nobody is bound ([permissions.md](permissions.md) § *What
somebody does to their own account*); somebody else's row is not one such a call
sees. `examples/sso` draws all of it.

## An account somewhere else — an identity

Not a secret and not something roster checks. It is the fact *this person is
`entra:e1b2c3`*, so that a front door which has verified an Entra token can ask
roster who that is.

```sh
roster --as admin identity add '{"holder":{"slug":{"alias":"alice","tenant":{"alias":"newco"}}},
                      "provider":"entra","subject":"e1b2c3"}'
```

Unique per `(tenant, provider, subject)`. roster is **not** the relying party:
it does not hold your OIDC client secret, does not validate the provider's
token, and has no opinion about which provider you use. `Connection` is where a
deployment keeps what a *login app* needs to talk to a provider, and even that
is configuration rather than proof.

`Email` is the same species — a fact used to find somebody, and to send a
recovery link to:

```sh
roster --as admin email add '{"holder":{"slug":{"alias":"alice","tenant":{"alias":"newco"}}},
                   "address":"alice@newco.example"}'
```

`date_verified` is **not** a field a caller writes. It is stamped by whatever
did the verifying, and `Add` refuses a request carrying one.

⚠️ **And an address nobody has checked names nobody.** Signing in by an address, and
asking for a recovery link at one, both read that stamp — so the row above is a
contact detail and not yet a way in. Two consequences worth knowing before you
write one:

- Somebody whose only address came from a directory that sent no `email_verified`
  cannot sign in by it. They sign in through the directory, which is how they got
  here; the road to the address resolving them is proving it from the account page,
  or `Email.Attest` stamping it the day that directory starts saying so.
- **A proof may take an address nobody proved.** An address is one person's within a
  tenant, so a row somebody wrote down without checking it would otherwise keep the
  rightful holder out for good — `Add` answers *somebody has it* and there is no
  second row to be had. So a claim is minted for the address on such a row, the link
  goes to the **mailbox**, and whoever reads it ends up holding it. A proved address
  does not move; see [../login.md](../login.md) § *Signing in by address*.
- **The row above, written by you about somebody else, is an invitation** -- for as
  long as they have no way in. Who wrote a row is stamped on it (`entered_by`, the
  caller and never the request), and `login.enrol: expected` admits somebody
  arriving verified at an address only when it is proved, or is an operator's word
  about a person who has not arrived. An address somebody wrote onto their **own**
  row invites nobody; see [../login.md](../login.md) § *Putting people in, and
  letting them arrive*.

> Writing an identity or an address onto somebody's row is a way to sign in as
> them. Both are guarded by the same rule a password is: nobody writes a way
> into an account wider than their own.

### A second directory, added by the person themselves

An operator with two `Connection` rows -- Entra and GitHub, say -- has people who
arrived through one of them and may want both. That is the account page's *signs
in with* section: a row per directory the operator offers, where the one they
arrived through says who they are there and offers to unlink, and the one they
have not used offers **connect**. Pressing it is a round trip to that directory
and back, and what lands on their row is `Identity.Attest` with their own
reference, **made as them** through the delegation their sign-in handed the
account app: the provider proves the account, which is the front door's part,
and roster takes its word for it only from an app that holds
`IdentityService/Attest` across the tenant, which `roster account provision`
binds it. And roster is asked what it is asked of any call of theirs -- that
their session still stands when the round trip ends, and that their tenant lets
them (`IdentityService/Attest` in its `self-service` role, or a role of their
own).

Two things it does not do, each for a reason:

- **It does not offer the directory they already use.** roster refuses a second
  identity at one provider -- *a second one is a link that found the wrong row*
  (`server/core/identity.go`) -- so a button for it would be a trip to a
  directory that ends in a refusal.
- **It does not link the directory their tenant provisions through.** An
  account there is who the tenant's directory means by somebody, so it is the
  directory's to say and not the person's ([scim.md](../scim.md)): *connect*
  for that connection ends in a refusal, since the write is made as them.
  Somebody is linked there by signing in through it where the front door
  matches them by address, or by an operator.
- **The operator's console has no such button.** A subject is issued at the
  directory and does not exist until somebody signs in there, so an operator
  cannot write one; what they can do instead is write the `Email` row the first
  sign-in matches (`../login.md` § *Putting people in, and letting them arrive*).
  The admin console lists what somebody signs in with and can take one away.

**Unlinking is refused where nothing would be left to sign in by**, whoever
asks. What is left has to work: an account at a directory the tenant still has
a `Connection` to, or a password in a tenant that takes passwords
(`config.password`). An account at a directory the operator has since removed
is a row and not a way in, and so is a password where passwords are off; a
second factor never was one. Somebody left with only those is given another way
in first.

**And the account a directory provisions them through is not theirs to
unlink.** It is what their tenant's directory finds them by, and what lets it
suspend them when they leave; unlinked, they would be out of its hands. Taking
one away there is an administrator's ([scim.md](../scim.md)).

## A second factor

```
CredentialService.Enrol { ref: …, kind: "totp" | "webauthn", name: "phone" }
VouchService.Verify     { who: …, kind: "totp", secret: "123456" }
```

A TOTP **seed** is the one secret roster has to be able to read back — computing
the code somebody is about to type means holding it — so it is wrapped with a
key the deployment keeps outside the database:

```yaml
vouch:
  keys: ["2026a:base64-of-32-bytes", "2025a:…"]   # current one first
```

A deployment with no `vouch.keys` is refused when somebody tries to enrol,
rather than storing a seed in the clear.

**A second factor is not a way in.** Six digits and a thirty-second window is
not a sign-in on its own, and somebody whose only credential is a seed cannot
sign in with it.

**Taking your own off takes having just signed in.** Within five minutes of
proving who you are, by the credential the call came with -- so at your account
page, a moment after signing in there with the factor itself. A key of yours
and a page left open are refused: what is left when a second factor goes is
the password alone, and a credential that merely acts as you must not be what
decides that. Somebody who has lost the phone asks whoever administers them,
who takes it off as they would reset a password.

**It is asked for wherever a password is taken**, roster's own sign-in
included. Somebody who has proved one -- a factor counts from the first code
that verifies -- is not signed in by their password at the user console or the
admin console: `AuthService.SignIn` answers what is left and a `continuation`
with no cookie, and a second call carries that and the code.

```
AuthService.SignIn { alias: …, secret: <the password> }                     -> { available: [{kind: "totp"}], continuation: "vc_…" }
AuthService.SignIn { continuation: "vc_…", kind: "totp", secret: <the code> } -> a cookie
```

`secret` is what is proved at each step, as bytes: the password beside the
alias, the code beside the continuation.

One attempt per password: a wrong code spends the continuation, and is counted
where a wrong password is. A link a front door hands a browser is the one
sign-in there that asks no second factor, since the directory that checked the
person is what the link stands for.

**And nowhere else.** Somebody who arrives through a provider is signed in on
the directory's word, at the account app and at the Login App alike:
`VouchService.Accept` answers at once, whatever factor of roster's they have
proved. What stood between them and that sign-in was the directory's to ask.

**Whoever runs the deployment has one too**, enrolled from a shell on the box,
since that is where an operator is managed and the admin console's work is
customers. An authenticator app; a security key is registered by a browser at
the name it will be asked for at.

```sh
roster --as admin control vouch enrol @admin        # an otpauth:// Uri, once, on stdout
roster --as admin control vouch prove --code-stdin @admin
```

It counts from the code that proves it, and the admin console asks for it from
then on. An operator who has lost the app is somebody at the box again:
`roster --as admin control credential list` names the row, and `control
credential erase` takes it away.

## Stopping one

```sh
roster --as admin key list                  # both planes, with what each may call
roster --as admin key revoke 01a03322-…           # now, on whichever plane holds it
```

A revoke is a delete and not a flag: the next call carrying that key finds
nothing. There is no window and nothing to expire. The trail keeps what it was.

For a person rather than a key, there are three acts — separate because a role
is a list of methods, and what you can grant is exactly what you can name:

| | |
| --- | --- |
| `HolderService/Disable` | they are not to sign in. Sessions, tenant keys and delegations they held stop working |
| `HolderService/Enable` | the other direction, and a separate grant on purpose |
| `HolderService/Invalidate` | everything issued **before now** is void. No undo, and no time to give — the server stamps it |

`Invalidate` is *sign out everywhere* as a **fact** rather than as a list: one
timestamp, which roster answers and each app compares its own sessions against.
There is no registry of live sessions here and there will not be. A link roster
minted for the person before it is void with them -- a recovery link, one that
deletes the account, one a front door handed a browser -- so a recovery
followed, which ends in a password issued, ends every link asked for before it.
So is a sign-in begun before it and waiting for its second factor. A link that
confirms an address is not, since it signs nobody in; what stops that one is
what stops every link -- the person suspended or erased, or their tenant closed.

**`Enable` brings back what `Disable` stopped, as it was.** A suspension refuses
a session and ends none: one still inside its life when the person is enabled
answers again, on whichever browser holds its cookie. So an account stopped
because somebody else was in it is `Invalidate`d before it is enabled, or the
session that was the reason comes back with the person.

**`roster restore` does not bring them back.** Somebody erased cannot be
`Invalidate`d first -- no verb reads them -- so a restore does it itself: it
stamps the same moment in the write that brings the person back, and nothing
issued before it answers again
([operating.md](../operating.md) § "Destroying somebody, which an erase does not").

It **deliberately does not touch an API key**. A key is named, listed and
revoked one at a time; killing somebody's scripts silently under "sign out
everywhere" is an outage with nothing anywhere saying why. That is a second act
and it has a second name — `roster key revoke`, or `roster me revoke-key` for
your own. A restore is no different: a key of somebody erased answers again
with them, unless it was revoked while they were gone, which `roster key list`
and `roster key revoke` do for somebody erased as for anybody.

Each is a verb of its own on the CLI, and they are ordinary RPCs — remote with
a role that names them, local through `Ungated` like everything else:

```sh
roster --as admin holder disable @newco/alice     # and what they hold stops working
roster --as admin holder enable @newco/alice      # the other direction
roster --as admin holder invalidate @newco/alice  # everything issued before now is void
```

The server stamps the time — there is none to give, so a stale clock cannot
un-suspend anybody, and a suspension cannot lose a race with somebody editing a
profile. `roster --as admin holder signs-in @newco/alice` is the read beside them: how
somebody signs in, every key of theirs included, with no verifier anywhere in the answer. And
`roster sync watch` is where a stop can be seen landing — one line per event,
the same stream every app holds.

The same shape holds for every other overlay verb, so there is nothing a
console can do that a terminal cannot: `roster holder reaches`, `holder search`, `tenant update`,
`host update`, `mail-domain update`, `connection update`, `email claim` and
`email confirm` are one line each in `cli/entity.go`, taking the request as
JSON the way the generated verbs do.

## Next

[permissions.md](permissions.md) — what somebody may do once they are in.
