# The CLI

`roster tenant add` and `TenantService.Add` are the same call. Every entity has
the same four verbs -- `get`, `list` (or `ls`), `add`, `erase` -- and
`watch` where its rows are watched (`holder`, `email`, `identity`, `credential`,
`team` and a few more; `--help` says). `access` is read and never written, so it
has `get` and `list` alone. `tenant` has no `erase`, because no tenant is erased:
one is closed with `roster tenant close` and a closed one destroyed with
`roster forget @tenant` ([operating.md](../operating.md) § *A tenant leaving*),
and `roster tenant erase` typed all the same says so and fails. Each command is a client for exactly the RPC of that
name, so what you learn typing is what you write in code. `roster <entity> <verb> --help` prints
the request shape.

**`patch` is a fifth, and it is the box's.** It is the general write: any field
of the row, under the version you read. No port serves one, whatever a
configuration says, so over the wire `roster team patch` answers
`Unimplemented`, *it is how the servers write, not how a caller asks*. Locally
it writes, like everything else there:

```sh
roster --as admin team get -o json 01a1e7ef-…    # `dateUpdated` is the version
roster --as admin team patch 01a1e7ef-… '{"name":"Operations","date_updated":"2026-11-17T05:16:48.623925973Z"}'
```

What changes a row over the wire is a verb of its own, where the entity has
one: `holder update`, `group update`, `tenant update`, `connection update`,
`role widen` and `role narrow`. A site and a team have none, nor does the role
on somebody's place in a team or a group: those are erased and added again
([permissions.md](permissions.md) says so where each is described).

## Two modes, and the configuration decides which

| | |
| --- | --- |
| **local** -- no `client.addr` | opens the database in `db:` and writes through `Ungated`: no wall, no gate, no rules. A shell on the box, doing what the deployment can do. It says so on stderr |
| **remote** -- `client.addr` set | an ordinary caller. The wall narrows what comes back, the gate decides what is allowed, and the credential says who is asking |

So a customer's own person runs the same binary. Their configuration has no `db:`
block at all -- there is nothing for them to open:

```yaml
client:
  addr: "roster.internal:50051"
  auth:
    scheme: bearer
    credential_file: /home/kim/.roster/key   # an rt_, which resolves to them; the path as it is, `~` is not expanded
  # insecure: true                           # only for a listener that has no `tls:`
```

```sh
roster holder list -o table    # the people in their tenant, and no others
roster me get                # who this credential is, and every method it may call
roster tenant list             # PermissionDenied, if their role does not say so
```

`--HAL` on the root forces the local mode whatever the file says, for somebody
with a shell who wants to look at the rows under a deployment configured for the
wire. Naming `auth` with no `addr` is refused: a credential with nowhere to send
it would otherwise read the database directly while you believed you were calling
a server.

**A command succeeding locally says the write is possible, not that a caller
could make it.** The local mode is outside every rule, which is why the first
role in a tenant can be written at all. If you are working out what a role needs,
test it remotely with a key.

### Locally, say who you are

```sh
roster --as admin holder list -o table
roster --as admin control holder list
roster --as admin key list
```

What a local command shows or writes of anybody is on the access record, and
the record names a person: `--as`, an operator of the control plane by alias --
who `init` made, or anybody the admin console signs in. The entity commands,
`key list` and `scim adopt` refuse to open anything without it on a deployment with a control
plane, and refuse a name that matches nobody or a disabled operator. A row is
written before the answer is printed, and a read whose row cannot be written is
refused rather than shown. It is a flag on the root, before the command, and
deliberately not a setting: the configuration and the environment are
everybody's who opens a shell on the box. It changes nothing a command may do --
local is still outside every rule -- and remote needs none, since the credential
says who is asking. See [operating.md](../operating.md#the-cli-on-the-box-is-the-deployment).

A local `watch` is refused where `watch.broker` is `memory`: that broker is
this process's own, and nothing the deployment does reaches it. Watch remotely,
or under a broker every process hears.

### Which commands have only one mode

| | |
| --- | --- |
| local only | `init`, `key add`, `key list`, `key revoke`, `vouch reset\|set\|unlock`, `trail`, `forget`, `restore`, `hold`, `resources`, `app install\|uninstall`, `login provision`, `account provision`, `scim provision\|adopt`, and all of `control` -- what they write is not served, which is the whole reason they are commands |
| remote only | the rest of `vouch` (`verify`, `delegate`, `continue`, `link`, `redeem`, `revoke`, `enrol`, `accept`, `leave-link`, `leave`), `issue`, `me`, `front`, `sync` -- those calls are a *caller's*, and a local run has none |

### The control plane is `roster control`

The operators and this deployment's own callers live on a second database,
`control.db`, and every command that writes there is under one word:

```sh
roster control key add --allow '/roster.VouchService/Verify' portal   # an rk_; naming it makes it
roster control vouch reset @admin                                     # the operator's way back in
roster --as admin control holder list                                   # the holders of the control plane, a page of them
```

The entity commands are all there except `tenant add`: the control plane has
one tenant, `owner`. `--as`, `control vouch` and `control key add` name an
operator by alias alone because of it; an entity command's reference is
written whole, `@owner/admin`. What answers
across both planes stays outside -- `roster key list` and `roster key revoke`,
because a key's identifier does not say which database it is in.

**An erase there is where it ends.** `roster restore`, `forget` and `hold` read the
customers' database and not `control.db`, so somebody of the control plane who is
erased (`roster --as admin control holder erase @owner/alice`) is not brought back,
and what was theirs is not destroyed either: their alias, name, verifier and second
factor stay in that database for good. The command says so on standard error before
it erases. For an operator who may come back, `control holder disable` is the one
that is undone.

The same rows are served at `control.addr`, to a key: one hanging off somebody
bound there, since at that listener a key calls what its holder holds on the
control plane (`ways-in.md`). A key made for an app is refused there.

`control key add --narrowed NAME` is the key for an app you host that always names
a tenant: it allows nothing until a request does (`ways-in.md`). `roster app
install --tenant T --role … NAME` puts that app into a tenant -- its holder there,
the role, the nomination -- and `roster app uninstall` takes it out
([apps.md](../apps.md)). Something a
**customer** runs for themselves is not this. It is a holder in their tenant with
an `rt_`: `roster key add --tenant … --holder …`.

## How anything is named

```
roster <entity> <verb> [options] [NAME] [REQ...]
```

- `NAME` is a **reference**: an identifier, `@tenant`, or `@tenant/alias`.
- `REQ` is the rest of the request as JSON, merged over it. `-` reads stdin.
- **Flags come before arguments**: `roster tenant list -o json`, not `... list json -o`.

```sh
roster --as admin tenant list -o table
roster --as admin holder get @newco/alice
roster --as admin holder list -o wide
```

`-o` is `pretty` (the default), `json`, `protojson`, `prototext`, `name`, `table`,
`wide`, or `template=…`. `-o name` prints the identifier alone, which is what a
script wants.

**A `list` is a page, and not the whole.** It answers twenty rows unless told,
in the order they were made. `-n` (`--size`) asks for more, up to a hundred,
and where there are more than it answered, the answer says where to carry on
from, which `--next` takes:

```sh
roster --as admin holder list -n 100                 # ends with `next: WyIyMDI2…` when there are more
roster --as admin holder list -n 100 --next WyIyMDI2…
```

`pretty` prints that as its last line, and `json`, `protojson` and `prototext`
print the answer whole, its `next` included. **`name`, `table`, `wide` and
`template` print the rows and nothing else**: twenty lines of `-o name` are all
there is or the first twenty, and nothing on either stream says which. A script
that wants every row reads `next` out of `-o json` and asks again until there
is none. Every `list` this manual shows is one page.

An **alias** is unique within a tenant and among the living: two tenants may both
have an `admin`, and a tenant may reuse an alias after the person holding it is
erased.

And it is **written as it is read**: small letters, digits and single hyphens,
beginning with a letter, 63 at most. A write that says one with a capital in it
or a space at either end is refused with the spelling it would have to be --
`alias: written as it is read, so "acme" rather than "ACME"` -- for a tenant,
a person, a site, a team, a group, a role and a key alike. It used to be
lowered and trimmed without a word, and the row was then not found by the name
it had just been given, since a reference is compared exactly.

A `NAME` typed at the command line is folded by the command line before
anything is sent, so `roster holder add @Newco/Alice` makes `alice` in `newco`
and `roster holder get @NEWCO/ALICE` finds them: a person types there. An
alias inside `REQ` is sent as it is written, and refused like any caller's.
The other place a person types a name is a sign-in, and there roster folds
it: [ways-in.md](ways-in.md).

### A reference in JSON is the oneof it is declared as

The outer key says *which way of naming*, and the inner one is the name:

```json
{"tenant": {"alias": "newco"}}
{"holder": {"slug": {"alias": "alice", "tenant": {"alias": "newco"}}}}
{"role":   {"slug": {"alias": "everything", "tenant": {"alias": "newco"}}}}
{"holder": {"id": "01a03322-4034-842a-8802-990533c39e6a"}}
```

`Tenant` carries the string directly, because its alias is unique on its own --
there is no parent to name it within. Everything else is
`{"slug": {"alias": …, "<parent>": …}}`, and the parent is whatever the alias is
unique inside: a tenant for most, a **site** for a team, a **holder** for a key.

## What a `watch` says

`roster holder watch @newco/alice` is `HolderService/Watch`: a stream that says
what is there, and then what changes. The comments generated onto every
`…WatchRequest` and `…WatchItem` say three things of it that are not so. This
is what is so:

- **A watch names its rows.** Its filters have the shape a `list`'s have, and
  only one that names a row by reference is taken. A filter by tenant, by site
  or by anything else a `list` takes is refused -- *a watch says which rows it
  is about by naming them* -- and so is no filter at all. To follow a set,
  list it and watch what it answered.
- **A row that is gone is said with the call that took it.** An item with no
  `value` is a row this stream carried and its caller no longer sees. It still
  carries `action`, the RPC that was called: `…/Erase` for a row that was
  erased, and whatever touched it for one that left the caller's reach. So the
  two are told apart, where the comment says there is deliberately no way to.
- **A row that leaves the caller's reach is not said until it is next
  written.** A stream hears the writes of its own entity. What takes a row out
  of somebody's reach is mostly a write of another -- their place in a site
  erased, a binding taken away -- and nothing is said then: the row's next
  write is answered as an item with no value. Until it is, the last value sent
  is one the caller was allowed when it was sent. A caller who may no longer
  call `Watch` at all has the stream ended, when it is next asked who opened
  it (`stream.recheck`, [operating.md](../operating.md)).

## Every RPC that can have a command has one

Three of roster's own methods are left out on purpose, and each absence is a
decision rather than a gap:

| | |
| --- | --- |
| `Apply` | one of payday's two general writes. No port serves either and no setting opens one (`cmd/serve.go`); `patch`, the other, is a command because the box reaches it |
| `AuthService` | it mints the admin console's session, and a session cookie is a browser's credential where a terminal's is a key |
| a *service's* key over the wire | minting is granting, the grant rule reads bindings, and a key holds none. The mints for a service are `roster control key add` and a console |

payday's own framework service gets no `roster` command either:
`TokenService/Introspect` is what an app calls to check a credential, and it
is not roster's to wrap.

## Next

[customers.md](customers.md) -- a tenant and the first person in it.
