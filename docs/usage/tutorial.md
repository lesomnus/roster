# Tutorial

One deployment, from nothing to a person signing in and a service calling. About
half an hour, all at a terminal, SQLite only — no Docker, no browser.

Every command and every answer here was run against the shipped `roster.yaml`.
Identifiers and secrets will differ; nothing else should.

## 0. What you are building

A deployment that serves one customer, **newco**:

- `ops` — you, the operator. Runs the deployment. Lives in the control plane.
- `newco/admin` — newco's administrator. Everything, inside newco.
- `newco/alice` — somebody who works there. A password and a laptop key.
- `portal` — your login app. A service of yours, not of newco's.

## 1. Two databases and a server

```sh
cd $(mktemp -d)
cp /path/to/roster.yaml .
roster init
```

```
control plane
  holder ops is 01a0332f-d4d6-8334-a802-98a7402c0dd8
  bound to role "everything" = /roster.*/* -- every RPC roster serves, now and after an upgrade
  password  EQwFb5X74bcKhtm9LbQ1erW23bOXNxxmP672FfKGvkY

sign in to the console as ops. that password is shown once and is not stored -- write it down now.

there are no customers yet, which is the right state to start in.
```

Two files appeared beside you: `roster.db` and `roster-control.db`. A key must
not live in the tables it protects, so who may call this deployment is a
different database from who its customers are.

Leave a server running in another terminal:

```sh
roster serve
```

## 2. A customer

Four writes. This is the whole of standing one up:

```sh
roster tenant add @newco '{"name":"Newco Ltd"}'
roster holder add @newco/admin '{"name":"Ada Admin"}'
roster role   add @newco/everything '{"methods":["/roster.*/*"]}'

echo '{"role":  {"slug":{"alias":"everything","tenant":{"alias":"newco"}}},
       "holder":{"slug": {"alias":"admin",     "tenant":{"alias":"newco"}}}}' \
  | roster binding add -
```

```sh
roster tenant ls -o table
```
```
ALIAS   NAME        AGE
newco   Newco Ltd   4s
```

Ada exists and holds everything **inside newco**. She still has no way to prove
she is Ada — that is step 4.

## 3. Somebody who works there

Alice, with a narrower role:

```sh
roster holder add @newco/alice '{"name":"Alice Nguyen"}'
roster role   add @newco/support \
  '{"methods":["/roster.HolderService/Get","/roster.HolderService/List","/roster.MeService/Get"]}'

echo '{"role":  {"slug":{"alias":"support","tenant":{"alias":"newco"}}},
       "holder":{"slug": {"alias":"alice",   "tenant":{"alias":"newco"}}}}' \
  | roster binding add -
```

## 4. Ways in

Three credentials, for three different things.

**A password, for Alice at a browser:**

```sh
roster vouch reset @newco/alice
```
```
TJCWlQLYd7NmXk1sHmfwUn0LZ0Bo7qUJmRTLwPvZgHo
Shown once. Everything they had signed in with stopped working: a reset that left
the old sessions alive would not be a reset.
```

The secret is on **stdout** and the sentence on stderr, so
`PW=$(roster vouch reset @newco/alice)` is the password and nothing else.

**A key for your login app**, which is a service of yours and not of newco's:

```sh
APP=$(roster key add --service portal \
        --allow '/roster.VouchService/Verify,/roster.MeService/Get')
```

Naming `portal` created it. `--allow` is required — everything hands out more
than anybody asked for, and nothing mints a key that silently does not work.

**A key for Alice's laptop**, which acts *as her*:

```sh
KEY=$(roster key add --tenant newco --holder alice \
        --name laptop --allow '/roster.MeService/Get')
```

```sh
roster key list
```
```
01a0332f-d634-87ba-860e-4c5bf8e59fc9	@owner/portal/default	used=never	/roster.VouchService/Verify,/roster.MeService/Get
01a0332f-d64d-8d70-880e-c31ade371232	@newco/alice/laptop	used=never	/roster.MeService/Get
```

Two planes in one listing. `rk_` for your service, `rt_` for a customer's
person, and the prefix follows from which it is rather than from anything you
typed.

## 5. Your login app checks Alice's password

This is the call your front door makes. `curl` stands in for it:

```sh
SEC=$(printf '%s' "${PW}" | base64 -w0)

curl -sS -X POST http://127.0.0.1:8080/roster.VouchService/Verify \
  -H 'content-type: application/json' -H 'connect-protocol-version: 1' \
  -H "authorization: Bearer ${APP}" \
  -d "{\"who\":{\"tenant\":\"newco\",\"alias\":\"alice\"},\"secret\":\"${SEC}\"}"
```
```json
{"ok":true, "holder":"AaAzL9WPhJmTAjnR775IOg==", "tenant":"AaAzL9UtgPCAAVfY/uAN+Q==",
 "lockedUntil":null, "satisfied":[], "available":[], "continuation":""}
```

And a wrong one:

```json
{"ok":false, "holder":"", "tenant":"", "lockedUntil":null, …}
```

`ok:false` and not an error — the request was fine, the answer is no. There is
no session, no token and no cookie in that response, and there will not be:
**roster never issues anything a third party verifies.** What your app does with
`ok:true` is your app's.

`connect-protocol-version: 1` is required by the transcoder; leave it off and
you get a 404 that looks like a wrong path.

## 6. Alice's own key

```sh
curl -sS -X POST http://127.0.0.1:8080/roster.MeService/Get \
  -H 'content-type: application/json' -H 'connect-protocol-version: 1' \
  -H "authorization: Bearer ${KEY}" -d '{}'
```
```json
{"id":"AaAzL9WPhJmTAjnR775IOg==", "tenant":"AaAzL9UtgPCAAVfY/uAN+Q==",
 "alias":"alice", "name":"Alice Nguyen",
 "methods":["/roster.MeService/Get"],
 "credentials":[{"kind":"password", …}],
 "keys":[{"alias":"laptop", "methods":["/roster.MeService/Get"], "dateUsed":"2026-08-24T09:53:19Z"}]}
```

Two things to notice.

**It answered as Alice**, not as the key. An `rt_` resolves to its holder, so a
call made with it is made as them — behind the wall, in their tenant.

**`methods` is the key's, not Alice's.** Her role has three; this key was minted
with one. A key is never wider than the person it hangs off and can be much
narrower:

```sh
curl -sS -X POST http://127.0.0.1:8080/roster.HolderService/List \
  -H 'content-type: application/json' -H 'connect-protocol-version: 1' \
  -H "authorization: Bearer ${KEY}" -d '{}'
```
```json
{"code":"permission_denied","message":"/roster.HolderService/List: this credential is not for that"}
```

Alice may call it. Her laptop may not.

## 7. Which customer is this request for?

If your product resolves a customer by hostname:

```sh
roster host add '{"tenant":{"alias":"newco"},"name":"newco.example.com"}'

FD=$(roster key add --service frontdoor \
       --allow '/roster.FrontService/WhoseHost,/roster.VouchService/Verify')

curl -sS -X POST http://127.0.0.1:8080/roster.FrontService/WhoseHost \
  -H 'content-type: application/json' -H 'connect-protocol-version: 1' \
  -H "authorization: Bearer ${FD}" -d '{"host":"newco.example.com"}'
```
```json
{"tenant":"AaAzL9UtgPCAAVfY/uAN+Q=="}
```

A tenant identifier and nothing else, which is what makes it safe to ask before
anybody has authenticated. A name nobody claimed is `not_found`.

Note the portal's key could not make that call — it was minted with two methods
and this is a third. That refusal is the system working, and it is what a role
is for.

## 8. Stopping something

```sh
roster key revoke --id 01a0332f-d64d-8d70-880e-c31ade371232
```

The next call carrying that key finds nothing. No window, nothing to expire, and
the trail keeps what it was. It works on either plane, and an identifier on
neither is a refusal rather than a shrug.

```sh
roster vouch unlock @newco/alice     # too many wrong answers closed it (ten, unless vouch.lockout says)
```

## What you have

```
control plane          data plane
  owner/ops              newco
  owner/portal             admin   → everything
  owner/frontdoor          alice   → support, a password, a laptop key
```

Nothing here needed a browser. The console does the same acts for an operator
who has no shell, over `admin.addr`, through the rules rather than around them —
see [`../operating.md`](../operating.md) § the console.

## Where to go next

| | |
| --- | --- |
| more about roles, sites, groups, teams | [permissions.md](permissions.md) |
| SSO instead of passwords | [ways-in.md](ways-in.md) § an account somewhere else, then [`../login.md`](../login.md) |
| what all twenty-three entities are | [`../entity.md`](../entity.md) |
| running this for real | [`../operating.md`](../operating.md) |
