# The words

This repository uses a small vocabulary in a specific way, and most of it is
defined only beside the thing it describes -- which is right for a *why* and
useless to somebody meeting the word for the first time in a commit message. So
this is the one page that says what each word means and where the thing it names
actually lives.

Three notes about the list itself:

- **Some of these words are overloaded**, and the collisions are real rather than
  sloppy -- `gate`, `overlay`, `claim` and `doctor` each name two different
  things in this repository. Those are marked ⚠️, because knowing there are two
  is most of the way to reading either.
- **Most of them are borrowed** -- from payday, from Kubernetes, from OIDC. Where
  a word is *this repository's own*, it says so.
- Counts are how often each appears outside generated code, which is a rough
  measure of how much you need it.

## Who

| | |
| --- | --- |
| **tenant** (3.3k) | one customer organisation. The unit the wall narrows to, and `Tenant.id` is on nearly every row |
| **holder** (2.6k) | a row for somebody or something that can be a caller. Not a user account -- credentials hang off it rather than in it, and nothing on the row says whether a person or a machine is behind it. `Holder.id` is the `sub` |
| **roster operator** (990, usually written *operator*) | whoever runs this **deployment**. A holder of the **control plane**, and the only one who makes and unmakes tenants. Never a tenant: *an operator's tenant* is a sentence with no meaning here, and where prose wants the customer organisation the word is **tenant** |
| **roster user** | a holder of the **data plane** -- somebody in a tenant. What they may do is their bindings, and the wall narrows every read to their own tenant |
| **tenant administrator** | a roster user whose role administers their tenant. **Not a kind of row**: the schema has no such thing, and it is a holder with a binding (#29) |
| **customer** | a tenant, from the roster operator's side of the table. `roster tenant add` makes one; the admin console's *customers* screen is about them |
| **admin console** | the page a **roster operator** opens. Today it is served by the control listener and reaches `admin.addr` for customers; #32 moves it whole onto the admin listener |
| **user console** | the page a **roster user** opens, showing their own tenant and nothing else, over the walled data plane. `ts/user/`, served by `server.http` at `/` where `user_console.dir` names a build (#34) |
| **a holder's credential** | what tells two holders apart, since the row does not: a `Credential` (a password, a second factor) or an `ApiKey`. One holder may have both, and roster refuses neither -- `roster control key add --allow … admin` mints a key on the operator's own row. Which plane it is in is the other difference, and the one the `rk_`/`rt_` prefix comes from |
| **custody** (69) | a caller that acts across **every** tenant -- the deployment's own machinery rather than a customer's. `docs/position.md` is where the line is |

## What protects what

| | |
| --- | --- |
| **the wall** (578) | payday's tenancy predicate: a read is narrowed to the tenants the caller can see, in the query, not after it. It is **not configurable** and it is why there is no `if tenant == …` anywhere. A read has one; `Add` has no row to narrow, which is the next entry |
| **walled** (227) | built with the wall on. `cmd/serve.go` builds `Walled` and `Ungated`; **`Ungated` is not a privilege**, it is an instance the wall was never installed on, for work that cannot be done from inside a tenant |
| ⚠️ **the gate** (377) | **two things.** (1) The generated authorization layer -- `pd.GateBuild`, `gate.Policy` -- which decides whether a caller may call a **method**, and knows nothing about rows. (2) Informally, "a gate" is any check that has to pass: `scripts/test.sh`, `hydra.sh`, the CI jobs. Sense (1) is a thing in the process; sense (2) is a thing in CI |
| **a grant** (374) | **any write that changes what the gate will answer for somebody.** Wider than the writes that name a role -- `GroupMembership.Add` grants as much as `Binding.Add` -- and `server/core/escalate.go` is where the answers live |
| **a layer** (224) | a server that wraps another and calls `Next()` with a different request, so a field can mean one thing where a caller reaches it and another by the time a row is written. `server/core` is roster's. CLAUDE.md § *Writing a layer* is the mechanics |
| **a plane** (753) | one of the two (now three) servers roster runs in one process on separate databases: the **data plane** (customers and their people), the **control plane** (who may call this deployment, and the admin console), and the **admin** listener -- the data plane with no wall, for work that happens before there is a tenant to be narrowed to |

## What a caller carries

| | |
| --- | --- |
| **a key** | 32 bytes of `crypto/rand` behind a prefix, presented as `authorization: Bearer` on every call and stored as a SHA-256 hash. `rk_` is the control plane's (a service of the operator), `rt_` is a customer's (a person or their service). `server/keys` |
| **a delegation** (652) | `rd_`: minted per sign-in, expiring in minutes, and **bound to the caller it was issued to**. It does not travel in `authorization` -- it rides in `roster-as` beside the caller's own key, which is what makes the binding checkable. An app calling *as* the person it just signed in |
| **a host proof** | `HostProof`: a hostname a tenant is claiming, and the value roster asked them to publish at `_roster-challenge.<name>` as a `TXT` record. The one token in this schema that is **not** a secret and is stored as it is compared -- it goes in public DNS, and anybody who can put a record under a name owns the name. Nothing to do with Hydra's `login_challenge`, which the label unfortunately rhymes with. `server/prove`, and #42 |
| **a continuation** (378) | what a half-finished sign-in is: roster's, short-lived, single-use. The app holds no half-signed-in state; `POST /session/continue` hands the continuation back |
| **a session** | a cookie an **app** holds for a browser, never roster's to mint for somebody else's app. `payday/auth/authsession`; roster mints one only for its own console (`AuthService`) |
| **`sub`** | the `Holder.id` in a token. Globally unique, so a relying party keys on it **alone** -- `authoidc.Subject` is the whole of that decision |

## The apps

| | |
| --- | --- |
| **the admin console** | roster's own UI for a **roster operator**, served at `/` on the admin listener. `ts/console/`. Write *admin console* rather than *the admin console*: a **user console** is the other one, and *a console* on its own is a terminal somebody is sitting at |
| **the user console** | roster's own UI for a **roster user**: their own tenant, over the walled data plane. `ts/user/`. The two pages draw the same screens from `ts/lib/tenant/` and differ in who is calling -- which is the whole of the difference, and why there is one copy of them |
| **the account app** | roster's front door for a tenant's own people -- their own record, their own ways in. Its own process, holding one tenant key per **tenant** it fronts. `account/`, `ts/account/` |
| **the Login App** | what Hydra hands a `login_challenge` to, and what answers with a `Holder.id`. `login/`, `ts/login/`. **Self-hosted** is a roster user running it for their own tenant with one `rt_`; **roster-hosted** is a roster operator running one instance for many tenants with one `rk_`, narrowed per request to the holder each tenant's `Host` row nominates (#36) |
| **the front door** (227) | the shared browser-facing half both of those are built on: `POST /session` and after. `frontdoor/`, and `frontdoor/web/frontdoor.js` is its browser side |
| **the sandbox** (211) | two different kinds, and both are *the real thing with something faked* -- in opposite directions. The Login App's is the real pages with a **made-up server**: `npm --prefix ts run dev:login`, over `ts/vite.login.ts`. The consoles' is the real **server** compiled into the page: `npm --prefix ts run dev:sandbox` and `dev:user:sandbox`, over `wasm/`, one module publishing an entry point per listener. Each fakes the half that is not what it exists to show -- and the consoles' fakes exactly two things, the cookie and the name the page arrived at, because a message port carries neither |
| **a relying party** | an app that trusts the issuer's tokens. Two shapes, and a deployment has both: `docs/relying-party.md` |

## How it is checked

| | |
| --- | --- |
| **a walk** (135) | a script that drives the real thing end to end over HTTP and **asserts what came back** -- `docker/flow.sh`, `behind.sh`, `itself.sh`. The repository's own word, from 2026-09-03. A `step` that prints a URL is not a walk; a walk fails |
| **a rig** (62) | an apparatus assembled to stand the real thing up and exercise it: `compose.yaml` is the fast one, `scripts/cluster.sh` is the one over the real manifests. **Not production and not a unit test.** From `test rig` in engineering English, and that from *rigging* a ship -- fitting it out with what it needs to sail. ⚠️ Nothing to do with *rigged* meaning fixed or fraudulent. **This one is not the repository's own word: it arrived on 2026-09-11**, with `deploy/`, and is written down here because a term introduced in one commit and used sixty times deserves a definition somewhere |
| ⚠️ **doctor** | **two commands.** `pd doctor` is payday's -- it reads the app's schema and wiring and says what would go wrong at run time (a layer missing `WithDriver`, for one). `roster login doctor` is roster's -- it asks Hydra whether the OAuth clients are registered in a way this stack can sign anybody in with |
| **a stray** | `roster login doctor`'s word for a client Hydra will raise challenges for that no operator here claims. Broken rather than untidy: every flow raised for it reaches a page saying the login is not working |
| **the baseline** | `docs/baseline.md`: the promises a normal user relies on, each pinned to the test that holds it. **A baseline test is never weakened to let a change pass** |

## The records

| | |
| --- | --- |
| **the trail** (437) | the audit table: who wrote what, when, and what it was before. payday's `Audit` entity, `server/trail` for retention. A deployment key reads every tenant's, which `roster control key add` says out loud |
| **the corpus** (73) | the breached-password list a new password is checked against, in the one place that holds the row |
| **an epoch** | a counter on a `Holder` that invalidates everything issued before it. `Holder.Invalidate` moves it, which is how *sign this person out of everything* reaches sessions and delegations that are already open |
| **a seam** (29) | payday's word for a place it deliberately leaves for an app to fill -- `auth` reads a credential and does not issue one, and `AuthService` is roster filling that seam |

## The two other overloads

⚠️ **overlay** (136) is a **protobuf overlay** in `proto/ext/**` -- a file that redefines a generated message to add a field or a method to an entity's own service -- and a **kustomize overlay** in `deploy/`, which is a deployment layering its own hosts and secrets over the reference manifests. Same word, unrelated mechanisms, and both are used a lot. Which one is meant is always obvious from the directory and never from the sentence.

⚠️ **claim** is an **OIDC claim** (`preferred_username`, `email`) and a
**PersistentVolumeClaim**. Only the manifests mean the second.

## See also

- [position.md](position.md) — what roster is and where it stops, which is where
  most of these words get their force
- [entity.md](entity.md) — the twenty-four tables the nouns above are rows in
- [../CLAUDE.md](../CLAUDE.md) — the rules that use this vocabulary as given
