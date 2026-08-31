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
| **`rk_`** deployment key | a control-plane row; presents **as itself** | one of your own services | the auth interceptor, every request |
| **`rt_`** tenant key | a data-plane row; **resolves to its holder** | a customer's script, or their app | the same interceptor |
| **`rd_`** delegation | short-lived, bound to the key that asked for it | an app, *beside* its own key | the same interceptor |
| an **identity** | `(tenant, provider, subject)` — an account somewhere else | nobody; it is a fact, not a secret | whoever verified the provider |
| a **second factor** | TOTP seed, or a WebAuthn credential | a person, after the first factor | `VouchService.Verify` |

## A password

For a person at a browser. Two ways to write one:

```sh
roster vouch reset @newco/alice                        # generated here, printed once
printf '%s' "${PW}" | roster vouch set --password-stdin @newco/alice
```

`reset` generates thirty-two random bytes, because a secret the caller chose is
a secret the caller knows. `set` writes one somebody else chose — never as an
argument, since an argument is in the shell history and in the process list.

Neither can tell you what a password *was*: what is stored is a hash.

An account that wrong answers closed opens again by itself, and sooner if you
say so:

```sh
roster vouch unlock @newco/alice
```

The same three acts are on the console, on a person's own panel. Nothing is only
in one of them.

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
VouchService.Verify { who: {tenant: "newco", alias: "alice"}, secret: … }
  → { ok: true, holder: …, tenant: … }
```

and then mints whatever **it** uses for a session. roster does not mint that,
which is the rule at the top of this page.

If your front door would rather be handed something it can call roster with,
`VouchService.Delegate` is `Verify` plus an `rd_` — see § a delegation.

## A deployment key — `rk_`

For **your own services**: the login app, a product backend, a job that reads
the trail. They are holders of the control plane, and a key presents **as
itself** rather than as a person.

```sh
roster key add --service custody --allow '/roster.VouchService/Verify,/roster.MeService/Get'
```

```
rk_rCHP-AyXhX7cbNIjgLLL8udW6hOchSQKHzRkcpMWWwc
key 01a03322-… for @custody, allowing 2 method(s). This is the only time it is shown.
```

Printed to **stdout** and the sentence to stderr, so `$(roster key add …)` is
the key and nothing else. Shown once; what is stored is a hash.

Naming a service **creates** it. A service is not something anybody sets up on
purpose before they need it, and the control plane has one tenant so an alias
names one caller.

`--allow` is required in both directions: everything hands out more than anybody
asked for, and nothing mints a key that silently does not work.

Write it however reads best — the flag repeats, and each occurrence may itself
be a comma-separated list:

```sh
roster key add --service custody \
  --allow /roster.VouchService/Verify \
  --allow /roster.MeService/Get
```

**A deployment key is not walled by tenant.** It is the widest credential this
deployment issues. Some methods are wider than they look and the command says so
when you grant one:

```
NOTE: `/roster.AuditService/List` reaches the audit trail, which holds the contents of every
write in this deployment, in every tenant, for as long as the retention policy
keeps them.
```

`--expires 720h` bounds one. Empty is forever, which is what a service wants.

## A tenant key — `rt_`

For **one of a customer's people**, or their machine. It resolves to that
holder, so a call made with it is made *as them* — behind the wall, narrowed to
their tenant, and never wider than what they hold.

```sh
roster key add --tenant newco --holder alice --allow '/roster.HolderService/Get'
```

Naming a holder who is not there is a **refusal**, unlike a service. A
customer's people are the customer's, and a command that made one by mentioning
them would write rows into somebody else's tenant by typo.

Three other ways to get one, all the same row:

| | |
| --- | --- |
| `ApiKeyService.Issue` on `server.addr` | a customer's own admin, for somebody in their tenant |
| `ApiKeyService.Issue` on `admin.addr` | an operator, from the console |
| `MeService.IssueKey` | a person, for themselves — no subject in the request at all |

As themselves, from their own terminal, that last one is:

```sh
roster me get                                        # what you are, and your keys
roster me issue-key --name laptop --allow '/roster.MeService/Get'
roster me revoke-key 01a03382-5adb-8788-bf0e-c99210575d01
```

`roster me` is remote only. Every method answers from the caller and a local run
has none — it opens the database instead of calling a server.

Two rules hold whichever door was used, and they are why this is safe to offer
at all:

- **Nobody mints a key wider than themselves.** The methods on it must be ones
  the minter holds.
- **Nobody mints a key on an account they cannot reach.** A key *is* the person,
  so one written on the administrator's row is a credential for the
  administrator.

The local CLI is outside both, like every local command — see
[README.md](README.md) § a note on the CLI.

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

## An account somewhere else — an identity

Not a secret and not something roster checks. It is the fact *this person is
`entra:e1b2c3`*, so that a front door which has verified an Entra token can ask
roster who that is.

```sh
roster identity add '{"holder":{"slug":{"alias":"alice","tenant":{"alias":"newco"}}},
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
roster email add '{"holder":{"slug":{"alias":"alice","tenant":{"alias":"newco"}}},
                   "address":"alice@newco.example"}'
```

`date_verified` is **not** a field a caller writes. It is stamped by whatever
did the verifying, and `Add` refuses a request carrying one.

> Writing an identity or an address onto somebody's row is a way to sign in as
> them. Both are guarded by the same rule a password is: nobody writes a way
> into an account wider than their own.

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

## Stopping one

```sh
roster key list                             # both planes, with what each may call
roster key revoke --id 01a03322-…           # now, on whichever plane holds it
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
There is no registry of live sessions here and there will not be.

It **deliberately does not touch an API key**. A key is named, listed and
revoked one at a time; killing somebody's scripts silently under "sign out
everywhere" is an outage with nothing anywhere saying why. That is a second act
and it has a second name — `roster key revoke`, or `roster me revoke-key` for
your own.

Each is a verb of its own on the CLI, and they are ordinary RPCs — remote with
a role that names them, local through `Ungated` like everything else:

```sh
roster holder disable @newco/alice          # and what they hold stops working
roster holder enable @newco/alice           # the other direction
roster holder invalidate @newco/alice       # everything issued before now is void
```

The server stamps the time — there is none to give, so a stale clock cannot
un-suspend anybody, and a suspension cannot lose a race with somebody editing a
profile. `roster holder signs-in @newco/alice` is the read beside them: how
somebody signs in, keys included, with no verifier anywhere in the answer. And
`roster sync watch` is where a stop can be seen landing — one line per event,
the same stream every app holds.

## Next

[permissions.md](permissions.md) — what somebody may do once they are in.
