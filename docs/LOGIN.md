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
- Nothing today says custody may call `Vouch.Verify` and not `Holder.Erase`.
  roster installs no `gate.Policy`.

Whether the credential arrives as a certificate or an API key is the small half
of that question, and `auth.Seq` makes it swappable. The large half is what a
machine **is** in a schema whose central entity is a person.

That is the next piece of work, and the list above is what it has to answer —
which is why the wiring was built first.

## See also

- [PLAN.md](../PLAN.md) — D13, D14 and the open questions
- [`server/vouch`](../server/vouch) — the package comment is the detail
- payday's [guide/signing-in.md](https://github.com/lesomnus/payday/blob/main/docs/guide/signing-in.md)
  — how to put one of these in front of any payday app
