# The CLI

`roster tenant add` and `TenantService.Add` are the same call. Every entity has
the same six verbs -- `get`, `ls`, `watch`, `add`, `patch`, `erase` -- and each
command is a client for exactly the RPC of that name, so what you learn typing is
what you write in code. `roster <entity> <verb> --help` prints the request shape.

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
    credential_file: ~/.roster/key      # an rt_, which resolves to them
```

```sh
roster holder ls -o table    # the people in their tenant, and no others
roster me get                # who this credential is, and every method it may call
roster tenant ls             # PermissionDenied, if their role does not say so
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

### Which commands have only one mode

| | |
| --- | --- |
| local only | `init`, `key add`, `vouch reset\|set\|unlock`, `trail`, `forget`, `restore`, `resources` -- what they write is not served, which is the whole reason they are commands |
| remote only | the rest of `vouch` (`verify`, `delegate`, `continue`, `link`, `redeem`, `revoke`, `enrol`, `accept`), `issue`, `me` -- those calls are a *caller's*, and a local run has none |

## How anything is named

```
roster <entity> <verb> [NAME] [REQ...] [options]
```

- `NAME` is a **reference**: an identifier, `@tenant`, or `@tenant/alias`.
- `REQ` is the rest of the request as JSON, merged over it. `-` reads stdin.
- **Flags come before arguments**: `roster tenant ls -o json`, not `... ls json -o`.

```sh
roster tenant ls -o table
roster holder get @newco/alice
roster holder ls -o wide
```

`-o` is `pretty` (the default), `json`, `protojson`, `prototext`, `name`, `table`,
`wide`, or `template=…`. `-o name` prints the identifier alone, which is what a
script wants.

An **alias** is unique within a tenant and among the living: two tenants may both
have an `admin`, and a tenant may reuse an alias after the person holding it is
erased.

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

## Every RPC that can have a command has one

Three of roster's own methods are left out on purpose, and each absence is a
decision rather than a gap:

| | |
| --- | --- |
| `Apply` | one of payday's two general writes, closed unless a deployment opts in, and roster does not |
| `AuthService` | it mints the console's session, and a session cookie is a browser's credential where a terminal's is a key |
| a *service's* key over the wire | minting is granting, the grant rule reads bindings, and a key holds none. The mints for a service are `roster key add` and a console |

payday's own framework services get no `roster` command either: `TokenService/Introspect`
is what an app calls to check a credential, and `BatchService` is the generic
multi-write an app composes. Neither is roster's to wrap.

## Next

[customers.md](customers.md) -- a tenant and the first person in it.
