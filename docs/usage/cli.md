# The CLI

`roster tenant add` and `TenantService.Add` are the same call. An entity has
four verbs -- `get`, `list` (or `ls`), `add`, `erase` -- and
`watch` where its rows are watched (`holder`, `email`, `identity`, `credential`,
`team` and a few more; `--help` says), and some have verbs of their own
(`holder disable`, `role widen`, `tenant close`). `access` and `audit` are read
and never written by anybody asking, so they have `get` and `list` alone.
`tenant` has no `erase`, because no tenant is erased:
one is closed with `roster tenant close` and a closed one destroyed with
`roster forget @tenant` ([operating.md](../operating.md) § *A tenant leaving*),
and `roster tenant erase` typed all the same says so and fails. Each command is
the RPC of that name, so what you learn typing is what you write in code -- but
not every RPC is served on every port, and some of these commands answer only
at the box ([below](#which-commands-have-only-one-mode)). `roster <entity> <verb> --help` prints
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
one: `holder update` (a profile, whole, and `data`), `holder realias`,
`group update`, `tenant update`, `connection update`, `host update` and
`mail-domain update` (a note, and where a domain routes), `role widen` and
`role narrow`. A site and a team have none, nor does the role
on somebody's place in a team or a group: those are erased and added again
([permissions.md](permissions.md) says so where each is described).

And what a caller writes at `add` is not written again over the wire by any
verb, where the entity has no verb for it:

| | not written after `add` |
| --- | --- |
| a role | its alias, name, note and labels: `widen` and `narrow` write its methods alone |
| a person | their name, note and labels: `update` writes the profile and `data`, `realias` the alias |
| a group | its alias and labels |
| a connection, a host, a mail domain | their labels, and the name, which is so by design |
| a nomination | its name, its labels, and who it acts as |
| a number | the number and its kind |
| a binding, a relation | their labels |
| a key | its alias; and its note by nothing over the wire, since `ApiKeyService/Issue` does not carry it |
| a site, a team, the role on a place | everything, as above |
| a tenant | its alias; the last of its labels, which `update` cannot take away; and a hold, which is `roster hold` at the box |

Labels are the pattern: of the eleven entities that carry them, only a
tenant's are written after `add`. And **a person erased is brought back at the
box alone** (`roster restore`), where a tenant closed is brought back over the
wire (`tenant restore`).

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
    credential: ${file:/home/kim/.roster/key}   # an rt_, which resolves to them; the path as it is, `~` is not expanded
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
the record names a person: `--as`, a holder of the control plane by alias that
is not stopped -- who `init` made, anybody the admin console signs in, or, where
the operators are all disabled, a service, which is the way back.

These refuse to run without it on a deployment with a control plane: the
entity commands, `key list`, and the commands that write somebody's way in,
`key add|revoke`, `control key add`, `vouch reset|set|unlock`,
`control vouch reset|set|unlock|enrol|prove` and `restore`, of a person or a
tenant. Each refuses before it opens anything, and it is status `2` ([below](#exit-statuses)). `scim adopt` refuses without it once it
has opened both planes, before it reads anybody. Wherever it is given, every
local command looks it up and refuses a name that matches nobody, or somebody
disabled or erased, before it reads or writes anybody; the commands that write
somebody's way in and `trail accept` keep it, and the rest only
check it ([operating.md](../operating.md#the-cli-on-the-box-is-the-deployment)). A row is
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
| local only, of the entity commands | every `patch`; every verb of `access`, `session`, `link` and `continuation`; `credential get\|list\|watch\|add`; `api-key add`, `delegation add`; `consent add\|erase`; `tenant add`. No port a caller reaches serves them -- a verifier, a raw write of one, the record of who read what, a tenant put up from inside one -- and over the wire each is refused in the server's words |
| remote only | the rest of `vouch` (`verify`, `delegate`, `continue`, `link`, `redeem`, `revoke`, `enrol`, `accept`, `leave-link`, `leave`), `issue`, `me`, `front`, `sync`, `consent agree\|withdraw\|settled`, `email claim\|confirm` -- those calls are a *caller's*, and a local run has none |

### The control plane is `roster control`

The operators and this deployment's own callers live on a second database,
`control.db`, and every command that writes there is under one word:

```sh
roster --as admin control key add --allow '/roster.VouchService/Verify' portal   # an rk_; naming it makes it
roster --as admin control vouch reset @admin                                     # the operator's way back in
roster --as admin control holder list                                   # the holders of the control plane, a page of them
```

`control key add` names who runs it, and nobody can be named before `init` has
made the first operator, so it does not run before `roster init`: `init` first.

The entity commands are all there except `tenant add`, `tenant close` and
`tenant restore`: the control plane has one tenant, `owner`, and it is the
deployment. `--as`, `control vouch` and `control key add` name an
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
install --tenant T --allow … [--serves … --administer …] NAME` puts that app into
a tenant -- its holder there, its role of roster's, the nomination, and with
`--serves` a role of the app's own and roster's verbs for the app
(`app-administrator`), each held by it and by the people `--administer` names --
and `roster app uninstall` takes it out
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
script wants. An `-o` that is none of these, or an `--in` that is neither `json`
nor `protojson`, is refused before anything is opened, so a write is never made
and then reported as failed.

**A `list` is a page, and not the whole.** It answers twenty rows unless told,
in the order they were made -- but for `audit list` and `access list`, which
answer fifty, the newest first; `--reverse` turns either order round. `-n`
(`--size`) asks for more, up to a hundred however many it asks for, and up to
two hundred of the trail and the access record; and where there are more than
it answered, the answer says where to carry on from, which `--next` takes:

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

## What a read and a list answer

What a page's author meets of the generated `Get` and `List`, which no
comment of theirs says. Each was seen on a run.

**A `Get` answers what its `select` names.** With no `select` it answers what
`{"all": true}` answers: every field of the row, and each row it points at as
its identifier alone. The identifier is always answered. A row it points at
that is selected comes nested, to any depth -- a place in a site with its
person and that person's tenant, in one call -- each read by its own rule
([rules.md](rules.md)). A field not selected is absent or empty, **but for
the two stamps**: `date_created` and `date_updated` not selected are answered
as year one, `0001-01-01T00:00:00Z`. Year one is *not selected*, and not a
moment; `date_updated` is the version a write sends back,
and year one sent back is refused as a version that moved. Select it with
whatever you mean to write. `date_erased` is a field of twenty-six entities,
and of each one's `select`, and no read fills it: every read leaves the erased
out.

**A `List` takes no `select`.** A listed row carries each row it points at as
its identifier alone, so a list of places with their people's names is the
list, and then `HolderService/List` with a filter a person.

**A list is a page.** Twenty rows where the request says no `size`, and a
hundred at most however many it asks for; the trail's (`AuditService/List`)
and the access record's (`AccessService/List`, the box's) are fifty and two
hundred. A request carries thirty-two filters at most, and
more is refused rather than cut short. Rows come in the order they were made
(`reverse` turns it round), the trail's and the access record's newest first. Where there are more,
the answer's `next` is the next call's `after`. A list with nothing in it is
`{}` in JSON, with no `items` at all: read a missing `items` as none.

**What a read leaves on the access record** follows the fields an answer
carries that are declared somebody's. `HolderService/Reaches`,
`BindingService/List`, `DelegationService/List` and `RoleService/HandsOver` about somebody else leave
no row of it, where `HolderService/SignsIn` leaves one; `MeService/Get` leaves
none either, which #280 said. What somebody may call is declared nowhere.

## What a `watch` says

`roster holder watch @newco/alice` is `HolderService/Watch`: a stream that says
what is there, and then what changes. The comments generated onto every
`…WatchRequest` and `…WatchItem` say three things of it that are not so. This
is what is so:

- **A watch names its rows.** Its filters have the shape a `list`'s have, and
  only one that names a row by reference is taken. A filter by tenant, by site
  or by anything else a `list` takes is refused, `InvalidArgument` -- *a watch
  says which rows it is about by naming them* -- and so is no filter at all.
  So a watch's filter is not what a list's may be: to follow a set, list it
  and watch what it answered.
- **An erase is said by the identifier and the action.** An item with no
  `value` is a row this stream carried that was erased: it carries `id`, and
  `action`, the RPC that took it (`…/Erase`).
- **A row that leaves its hearer's reach is not said.** A stream hears the
  writes of its own entity, and answers its hearer only what they may read.
  What takes a row out of somebody's reach is mostly a write of another --
  their place in a site erased, a binding taken away -- and nothing is said
  then, nor when the row is next written, nor when it is erased. So **a hearer
  keeps a row it may no longer read, for as long as the stream is open**, as
  the last value it was sent, which it was allowed when it was sent. A
  stream opened again knows: a watch of a row out of sight is refused
  `NotFound` before it opens. A caller who may no longer call `Watch` at all
  has the stream ended, when it is next asked who opened it
  (`stream.recheck`, [operating.md](../operating.md)).

## Exit statuses

A command that did what it was asked exits `0`. One that did not exits with
the kind of failure it was, and says what on standard error, as it always has:

| | |
| --- | --- |
| `2` | the invocation is wrong: what is refused by reading the command line alone, before a file is read, a database opened or a call made. An unknown command or flag, an argument missing or one too many, a flag's value that does not parse or is not one of its words (`-o` among them), two flags that do not go together, a group typed with nothing after it, `--as` left out where a local command needs it. Typed again, it is refused again |
| `3` | a check ran to its end and found what it looks for |
| `1` | everything else: a refusal, the server's or the box's; nothing found; roster not reachable; a file that cannot be read; a configuration refused at start; a check that could not run |

These answer `3`, and no other command does:

- `roster trail verify`, with a finding in the archive
- `roster trail rows`, with a finding in the rows
- `roster login doctor`, with something that cannot sign anybody in
- `roster vouch verify`, answered anything but *yes*: *no*, *locked until …*, or a sign-in not finished
- `roster control vouch prove`, answered *no*

So a job running `roster trail verify` tells a finding, `3`, from a database
it could not open, `1`. A setting a flag stands for, given by neither
(`--hydra`, `login.hydra.admin`), is the configuration's, and `1`.

## Which RPCs have no command

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

And ten have none yet, and are reached only by a client of the RPC:
`HolderService` `Activate`, `Deactivate`, `Provision` and `Tell`;
`EmailService/Attest` and `IdentityService/Attest`;
`DelegationService/Exchange`; and over the wire
`CredentialService` `Set`, `Unlock` and `Issue` by reference, whose commands
(`vouch set`, `unlock`, `reset`) are the box's.

## Next

[customers.md](customers.md) -- a tenant and the first person in it.
