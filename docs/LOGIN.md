# Signing in

What happens when somebody types a password into a product app, end to end.
Every hop below is real: it is what `custody/cmd/login_test.go` runs, with roster
on a listener and custody dialling it.

## The path

```
  browser                custody                     roster
     │                      │                           │
     │ POST /session        │                           │
     │  {tenant, alias,     │                           │
     │   password}          │                           │
     ├─────────────────────>│                           │
     │                      │ VouchService.Verify       │
     │                      │  who{tenant, alias}       │
     │                      │  secret                   │
     │                      ├──────────────────────────>│
     │                      │                           │ Holder by @tenant/alias
     │                      │                           │ Credential by (holder,
     │                      │                           │   "password")
     │                      │                           │ argon2id, constant time
     │                      │                           │ failures, lockout
     │                      │  {ok, holder, tenant}     │
     │                      │<──────────────────────────┤
     │  204                 │                           │
     │  Set-Cookie: session │                           │
     │<─────────────────────┤                           │
     │                      │                           │
     │ POST /app.AssetService/List                      │
     │  Cookie: session     │                           │
     ├─────────────────────>│                           │
     │                      │ cookie → session → frame  │
     │                      │ anchor, if first time     │
     │                      │ wall narrows to tenant    │
     │  200 {items:[…]}     │                           │
     │<─────────────────────┤                           │
```

**roster never sees the browser.** It is called by machines, has no cookie
domain and no CSRF story, and that is why the session is custody's.

## What each side answers

| | |
| --- | --- |
| `POST /session` → **204**, no body | The cookie is the answer. What a page needs *about the person* is a request it should make, against the same server and behind the same wall |
| `POST /session` → **401** | Wrong password, unknown person, no such tenant — one answer for all of them |
| `Vouch.Verify` → `{ok:false}` | Same, and it takes the same time; see below |
| `Vouch.Verify` → `{locked_until}` | Ten wrong answers in a row closed the account for fifteen minutes |
| `DELETE /session` → **204** | The row is deleted, so the key is dead in every browser at once |
| any RPC → **401** | No cookie, a cookie naming nothing, an expired session, or a session naming somebody who has since been erased |

## Why every refusal looks the same

An unknown person, a person with no password and a wrong password are **one
answer**, and the first two burn an argon2 comparison so that they take as long
as the third. Otherwise the response time answers "does this account exist",
which is the question somebody working through a list of addresses is asking.

The one refusal that is distinguishable is a lockout, and that is a deliberate
trade: it says the person exists. The alternative is somebody locked out being
told nothing and trying forever. See PLAN.md, D14 — including what a lockout
does **not** fix.

## What custody keeps, and what it does not

custody keeps **no password**. There is no column for one in its schema, which
is the strongest form of that guarantee. The plaintext passes through its
process on the way to roster and is written nowhere.

What it does keep is an **anchor**: a `Holder` row carrying the identifier and
the tenant, made on the first call after signing in — not at sign-in, so that
`date_created` there means *first seen in custody* rather than *signed in once*.
The alias on it is seven characters payday made up. roster's name for the same
person is deliberately not copied: that copy would be wrong the first time
somebody marries.

Everything else — the name, the photo, the department — is read from roster when
there is a screen to draw.

## What custody sees that an IdP would hide

The plaintext, in memory, on its way past. That is the cost of custody serving
the sign-in form itself.

With Hydra and a Login App in front, the Login App sees it and custody never
does. Whether that is worth the extra moving parts is a deployment's decision,
and both shapes work: `custody/cmd/config.go` takes either a `roster` or an
`auth.issuer`.

## Do I need Hydra?

**One app: no.** The picture above is complete, and nothing is signed.

**Several apps, one sign-in: yes.** custody's cookie means nothing to the next
product, and the thing that fixes it — a credential with an issuer, a JWKS
endpoint, expiry, refresh and revocation — *is* OIDC. Writing it yourself is
writing Hydra.

So the question is never "password or OIDC". It is **one relying party or
many**. An air-gapped single app needs neither.

## A person who uses two operators' services

They have two accounts, and that is the whole answer.

`Identity` is unique on `(tenant, provider, subject)`. The same Google account
signs up to acme's service and to beta's, and those are two Holders with two
histories and two sets of permissions. Nothing here relates them, and nothing
should: a row that spanned tenants would have no owner, no answer to who may
erase it, and no tenant whose trail it belongs to. A tenant is the wall, and
something that crosses it is not a person any more.

The tenant is in the key rather than checked afterwards, which is the part worth
being exact about. It means a lookup **cannot** be made without naming a tenant,
so a front door that forgot to think about which one does not compile a wrong
answer -- it has nothing to look anybody up with.

Without it, one account at a provider would belong to exactly one tenant across
the whole deployment, and the second operator a person signed up to would be
told the identity was taken, by somebody they cannot see.

### What a front door has to know

Which tenant it is. That is what a tenant *is*: the same service under a
different operator's own domain, so the name the browser arrived at is the
operator whose service they are signing in to.

The email domain answers a different question -- where somebody
**authenticates**, often at another organisation entirely. One of acme's people
can perfectly well have a personal Google account.

### And what its own credential has to reach

A login app that fronts **several** operators cannot authenticate as a Holder.
A Holder belongs to one tenant and the wall narrows what it may read to that, so
it would resolve its own tenant and get NotFound for every other. What such a
deployment needs is an API key, whose actor is not inside a tenant.

`examples/sso` runs as one operator's front door for exactly this reason, and
says so where it wires the credential.

### What roster does offer

Within one tenant, several ways in for the same person. `Identity` is
one-to-many by design -- the same human arrives through the company's Entra
tenant on Monday and through GitHub on Saturday, and both land on one Holder
with one history and one set of permissions. That is the convenience it is for,
and putting the tenant in the key does not touch it: two Holders of one tenant
claiming the same subject at the same provider is still refused.

## This is not deployable yet

custody names itself to roster with `auth.Plain`, which is believed. Anybody who
can reach roster can claim to be custody and then guess passwords at every
tenant in the organisation.

Replacing it is not a longer string. roster answers nothing anonymously, so
custody needs a **row** here — and what that row is has not been decided:

- `Holder` is a person. custody is not one, and D1 makes `Holder.id` the `sub`
  of every token, so a service in that table has a `sub`.
- A `Holder` belongs to **one tenant** and is walled by it. custody acts across
  every tenant it has users in.
- `grpcx.Limit` counts per tenant, off the frame. All of custody's verifies
  would count against whichever tenant held it.
- What a service may call is now answerable: `cmd.Policy` is installed
  (`cmd/serve.go`), a `Role` names methods and a `Binding` grants it, and a
  holder with no binding may call nothing. `examples/sso` wires exactly that for
  its login app. What is still undecided is the row above -- what a **machine**
  is in this schema.

Whether the credential arrives as a certificate or an API key is the small half
of that question, and `auth.Seq` makes it swappable. The large half is what a
machine **is** in a schema whose central entity is a person.

That is the next piece of work, and the list above is what it has to answer —
which is why the wiring was built first.

## See also

- [PLAN.md](../PLAN.md) — D13, D14 and the open questions
- [`server/vouch`](../server/vouch) — the package comment is the detail
- [`examples/sso`](../examples/sso) — a relying party that signs somebody in
  with Google, Entra or GitHub and finds out who they are here. The package
  comment is the detail; the tests are the flow, run against a provider that
  answers over HTTP
- payday's [guide/signing-in.md](https://github.com/lesomnus/payday/blob/main/docs/guide/signing-in.md)
  — how to put one of these in front of any payday app
