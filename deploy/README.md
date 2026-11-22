# The reference deployment

What roster looks like when it is deployed, as manifests rather than as prose.
`scripts/cluster.sh` stands it up in k3d with the image this checkout builds.

## Why it is here and not only in the deployment that uses it

Everything roster's apps need from Hydra -- which URLs it redirects to, how a
client has to be registered, which fields are not optional -- was written down
twice: once in `compose.yaml` for the walks, once in whichever repository
actually deploys it. Nothing compared them. Four defects came out of that gap in
one week, every one found by a person clicking something in a browser.

So the contract is here, upstream, as the thing that actually runs. A deployment
is a **kustomize overlay** on this: its own hosts, its own secrets, its own
storage, its own products. What it should not restate is what a client needs or
what Hydra has to be told, because those are the parts that were wrong.

## What it found on its first run

Writing it was the point, and each of these is a thing no other gate here could
see:

- **A deployment that could not come up at all.** `roster login provision`
  skips an operator whose tenant does not exist -- a fresh volume has no
  customers -- and then `serve` refused to start over the key file that
  operator would have had. The file is missing because the tenant is missing;
  the tenant is missing because the server has not started; the server will not
  start because the file is missing. Fixed in `cli/`: the operator is dropped
  and said out loud, and if that leaves none the Login App stays off while the
  rest serves.
- **Volumes that made it permanent.** Written with `emptyDir` this came up
  every time and fronted nobody ever: the tenant is made by `resources:` when
  the server starts and read by `provision` on the *next* start, and a volume
  that goes with the pod means every start is a first one.
- Three shapes of configuration that are only wrong at run time: the vouch
  keyring is `ROSTER_VOUCH_KEYS` and a **list**, the probe wants the control
  plane's **HTTP** listener rather than its gRPC one, and `/` only answers
  there when the admin console is served.

## There are no Secrets here

**This base ships none**, and that is the one thing about it that was learned the
expensive way. It had three -- `roster-hydra`, `roster-vouch`, `roster-product`,
with values anybody can read, because this is a rig -- and a deployment that
overlays it brings its own **under the same names**, which is the only sensible
naming. Both of those are true and together they do not work:

- kustomize accumulates **generators before patches**, so a `$patch: delete` of
  the base's Secret does not prevent the collision. What comes out is
  `id … Name:"roster-hydra" … exists; can not use behavior: 'unspecified'`, at
  build time, and the deployment cannot sync at all.
- and a generator plugin -- ksops, decrypting SOPS files -- cannot declare
  `behavior: replace` to get around it.

So the rig makes its own with `kubectl create secret`, beside the four it already
made that way, and what an overlay inherits from here is **no Secret at all**.
Which is also the safer default: a base that ships a token-signing key readable on
GitHub is one an overlay has to remember to remove.

Found the way these things are found -- by a real deployment failing to render,
one commit after the local render said it was fine. The local render had the
generator stubbed out, because the age key is not on this machine and should not
be.

## Using it as a base

```yaml
resources:
  - github.com/lesomnus/roster//deploy?ref=<a commit, never a branch>
```

Pin a **commit**. `?ref=main` is a deployment whose manifests change when
somebody else pushes, which is the thing GitOps exists to stop.

**And the image by its digest, which no tag names by commit.** A build is
tagged by the CI run that made it and its day -- `:r43640498656`, and
`:261114-r43640498656`, which is also what `roster version` says inside it --
and the commit is the image's label, `org.opencontainers.image.revision`, and
what `roster version --short` says inside it. So from a commit to a digest is
its `ci` run on `main`, whose id is the tag:

```sh
docker buildx imagetools inspect ghcr.io/lesomnus/roster:r<the run's id>   # Digest: sha256:…
```

What an overlay is expected to bring, and what it should leave alone:

| | |
| --- | --- |
| its own | hosts, secrets (`roster-checkpoints` among them, `key.pem`, with the `trail` component), storage class, image digests, the products it actually runs, whether it keeps the trail's archive (below), and `config.yaml` -- by a `configMapGenerator` for `roster` with `behavior: replace`; and everything § *Before this is safe* lists |
| the base's | what a client has to be registered with, which `URLS_*` Hydra needs, that the clients are applied and then **checked** -- the four things that were wrong when they were written twice |

**`config.yaml` is one file, and an overlay that replaces it restates all of
it.** A file in a ConfigMap is not patched in part, so an overlay's own
`config.yaml` is the base's with its values in -- the `login:` block too, which
that file asks not to be written twice. What keeps the two from drifting is
either of:

- **the environment instead of the file.** Every setting has a variable
  (`roster config env`), and a variable wins over the file, so an overlay that
  patches the Deployment's `env` leaves the base's file the one in use. It is
  how the `trail` and `scim` components say theirs.
- **reading what changed.** An overlay that does replace the file reads
  `git diff <the commit before>..<the commit after> -- deploy/config.yaml` at
  every commit it moves to, and takes what the base changed.

The piece of the rig that a deployment does not want is `product.yaml`, there to
have something to sign in *to*; it comes out with a `$patch: delete`. The base
ships no Secret at all -- an overlay's own collides with one that is already
there -- so there is nothing of that kind to remove.

**Nor is the account app**, the front door a customer's people sign in at
themselves; an overlay that runs it (`roster account serve`, or `account.addr` in
`config.yaml`) delivers the links it mails through `account.mail`
([operating.md](../docs/operating.md) § *The front door's mail*). The SMTP password
-- and the username, where a provider's is a credential too, as SES's is -- is a
Secret of the overlay's own, like every other here, named by reference -- mounted
and `password: file:/run/secrets/smtp/password`, or in the environment and
`password: env:SMTP_PASSWORD` -- and never written into `config.yaml`. Send from a
domain of your own through a transactional provider, with the SPF, DKIM and DMARC
records it gives you; a cluster that cannot reach port 587 or 465 outward has an
egress rule to write first. How much it mails is limited in each replica's memory,
so two replicas allow twice as much.

**And the other shape of relying party is deliberately not here.** `oauth2-proxy`
in front of a page is half of what a deployment runs
(`docs/relying-party.md`), and it was moved into this directory and moved
straight back out: the issuer refuses a redirect URI over plain http unless the
host ends in `.localhost`, so that shape needs something terminating TLS in front
of it -- an **ingress**, which is the deployment's and not this base's.
`product.yaml` is here only because it can serve its own certificate.
So the proxy demo lives in whatever has a terminator: `scripts/cluster.sh`'s
overlay, or a deployment beside its own ingress. `scripts/cluster.sh`'s own
overlay (its last phases) shows two things on this list: hosts, with TLS ending
in front and Hydra told its public name, and a product of its own beside the
base's. It names no storage class and no digest, and does not replace
`config.yaml`.

## Before this is safe

What the base leaves to an overlay and an overlay must not forget, in one
place. Each is a thing this base is right not to decide, and wrong to leave
unsaid.

**Argo CD runs the two Jobs.** `clients.yaml`'s Jobs are `PostSync` hooks, in
waves `0` and `1` with `hook-delete-policy: BeforeHookCreation`: Argo runs them
after every sync, writes the clients before it checks them, and deletes each
Job's last run before making it again. Applied by `kubectl apply -k` the
annotations mean nothing. Both Jobs start at once, so the check may read Hydra
before the write -- and pass on what it read. And an apply that changes a Job
within the hour its `ttlSecondsAfterFinished` keeps it is refused, *field is
immutable*, since a Job's template cannot change. Without Argo, delete both Jobs
and apply, wait for the clients' Job to finish, then delete the check and apply
again, which is what `scripts/cluster.sh` does (`resync`).

**The namespace is `roster`, in thirteen addresses.** The base names none, and
an overlay chooses it; but the full names a relying party and the issuer are
reached by say `roster.svc.cluster.local`: the six `URLS_` of `hydra.yaml`,
`login.base` in `config.yaml`, `--issuer` and `--base` in `product.yaml`,
`--public` in `clients.yaml`'s check, the `Host` row in `resources.yaml`, and
the two redirects of `clients/product.json`. An overlay in another namespace
restates each -- most of them are its own hosts anyway -- and one it misses is
a name that resolves nowhere.

**The Secrets, by name.** The base ships none (above). What it reads, and what
a deployment that forgets each is told:

| Secret | keys | read by | forgotten |
| --- | --- | --- | --- |
| `roster-vouch` | `keys` | roster, and its `provision` init container | roster's pod does not start; Kubernetes says the Secret is not found |
| `roster-hydra` | `system` | Hydra | Hydra's pod does not start, the same way |
| `roster-product` | `client-secret` | the product, and the Job that applies the clients | neither starts: the product as above, the Job waiting for its volume; and a sync waits on the Job |
| `roster-product-tls` | `tls.crt`, `tls.key` | the product | its pod does not start, waiting for the volume |
| `roster-hydra-tls` | `tls.crt`, `tls.key`, `ca.crt` | Hydra, the product and the check; optional in each | nothing waits for it. Hydra is told to serve TLS from it: an overlay brings it, or ends TLS in front and tells Hydra so (§ *TLS, and the `--dev` that is not here*) |
| `roster-checkpoints` | `key.pem` | roster, with the `trail` component | a refusal in roster's log, naming the key's path (§ *The trail, kept and heard*) |

**A storage class.** The claims -- `roster-data`, `roster-control` and
`roster-hydra`, and `roster-audit` and `roster-witness` with the `trail`
component -- name none, so the cluster's default is used, at 1 GiB and
`ReadWriteOnce` each. An overlay names its class and its sizes.

**Images by digest.** The base names roster's image `edge`, which is whatever CI
last pushed from `main`, and sets no `imagePullPolicy`, so Kubernetes' default
for a tag is `IfNotPresent`: a node keeps the `edge` it pulled first. Two nodes
are then two builds, and a pod that moves between them changes build without a
commit. An overlay names roster's image by digest in its `images:` -- from a
commit to a digest is above -- and Hydra's and curl's the same way.

**No `NetworkPolicy` is shipped.** Nothing here is a `NodePort`, a
`LoadBalancer` or an `Ingress`, so nothing is reached from outside the cluster
unless an overlay routes it. Inside it, **every pod of the cluster reaches every
port here unless the cluster says otherwise**: Hydra's admin port, where whoever
reaches it can sign anybody in as anybody, in the same Service as its public
one; the admin console, which is the deployment's root; and roster's other
listeners by the pod's address. On a cluster shared with anything else, an
overlay writes the policies: Hydra's admin port is called by roster's pod and by
the two Jobs of `clients.yaml`, and by nothing else here.

**No `resources` are set**, on any container: every pod is the first a node
under pressure evicts, and none is scheduled for what it uses. An overlay sets
requests and limits for what it runs; a process that renders pictures takes up
to about 150 MiB for them ([operating.md](../docs/operating.md) § *A picture
from the directory*).

**TLS, and the Login App's cookie.** The base's Login App is plain http --
`login.base` an `http://` name and `login.insecure_cookie: true` in
`config.yaml` -- because the rig has no browser. A deployment serves it over TLS,
since a browser types a password into it, and says both: `login.base` its
`https://` name and `login.insecure_cookie: false` (`ROSTER_LOGIN_BASE` and
`ROSTER_LOGIN_INSECURE_COOKIE`, or its own `config.yaml`). An `https` base
beside `insecure_cookie: true` is refused at start, so an overlay that says the
first through the environment and inherits the file's second does not come up.

No pod here mounts a token for the cluster's API: none calls it.

## The trail, kept and heard

The base keeps the trail in the database, for as long as the database is: no
window, so nothing leaves it and there is no archive to keep. Keeping it the way
`docs/operating.md` § "The audit trail" says to is the component `trail/`, which
a deployment turns on in its own overlay:

```yaml
resources:
  - github.com/lesomnus/roster//deploy?ref=<a commit>
components:
  - github.com/lesomnus/roster//deploy/trail?ref=<the same commit>
```

With it, past its window a row leaves the database for an archive on a claim of
its own (`roster-audit`), the archive's account is signed with the key in the
Secret `roster-checkpoints` (`key.pem`, which the overlay brings: `openssl
genpkey -algorithm ed25519`), and every checkpoint is heard outside the database
and the archive (payday#115) -- by the log, and by a store on a claim of its own,
`roster-witness`. A witness is worth what it is apart from, so the two claims are
separate from `roster-data` and from each other, and a deployment puts the
witness on storage that refuses deletion, mounted with credentials neither the
database's nor the archive's. The component says what it keeps through roster's
environment -- `ROSTER_AUDIT_PROFILE`, `ROSTER_AUDIT_ARCHIVE`,
`ROSTER_AUDIT_CHECKPOINTS_KEY` and `ROSTER_AUDIT_CHECKPOINTS_WITNESSES` -- which
wins over `config.yaml`, so another window or witness is a patch of those
variables. Forgetting the Secret is a refusal in
roster's log where the process comes up -- the key's path, and *no such file* --
rather than a pod that waits; how to make one is
[operating.md](../docs/operating.md) § *The audit trail*.

A component and not the base, because what it brings is storage a deployment
chooses: an overlay that inherited the two claims and did not keep an archive
carried two empty volumes for nothing.

`scripts/cluster.sh` includes it, and asks a verification inside the pod what
the store heard, because a witness that cannot hear stops nothing -- so whether
it is linked, whether the pod's user can write its claim, and whether the chain
is held to it are each something only a running deployment can say, and nothing
else in the rig would go red for them. `compose.yaml` keeps the trail the same
way, and `scripts/hydra.sh` asks it the same.

## A directory provisioning people

The base answers no SCIM. Where a tenant's directory provisions its people
(`docs/scim.md`), the endpoint is the component `scim/`, which a deployment
turns on in its own overlay:

```yaml
resources:
  - github.com/lesomnus/roster//deploy?ref=<a commit>
components:
  - github.com/lesomnus/roster//deploy/scim?ref=<the same commit>
```

With it roster opens a listener of its own in the same process -- `scim.addr`,
said through `ROSTER_SCIM_ADDR` -- that answers `/scim/v2` and nothing else, and
there is a Service for it, `roster-scim`. That Service is what the overlay's
ingress routes the directory's public name to: the path `/scim/v2`, over TLS
the ingress terminates, and from the directory's address ranges only where the
platform can say so. It is the one listener here a cloud outside the cluster
reaches, which is why it is not a path on `roster-data` -- that listener answers
every RPC -- and why it is a component: a deployment with no directory to hear
from has no such door.

It holds no Secret. A directory presents its tenant's own key, minted once in
the pod and pasted where the directory asks for a token:

```sh
kubectl exec deploy/roster -c roster -- roster --config /config/config.yaml \
  scim provision --tenant <alias> --connection <the one its people sign in through>
```

`ROSTER_SCIM_INSECURE` is the line that is easy to lose. The endpoint dials this
process's own data plane, which speaks plain gRPC on the pod network, and
without it nothing fails until a directory calls -- and then every call is
answered `503`, *roster did not answer*. So `scripts/cluster.sh` includes the
component and asks through the Service with a key nobody minted: roster
refusing that key is the endpoint having reached roster, and it is red on
anything else. `compose.yaml` runs the same endpoint as a process of its own,
`scim`, and `scripts/hydra.sh` asks it the same.

## TLS, and the `--dev` that is not here

Hydra outside `--dev` refuses an `http://` issuer -- *issuer URL scheme must be
HTTPS unless development mode is enabled*, said by not starting -- so it
terminates TLS itself here, on a certificate `scripts/cluster.sh` makes and a CA
every other pod is handed. `compose.yaml` still runs `--dev` and says so where
it does; this is the shape that does not have to, which is the point of it
existing.

The issuer is a **Service's own name in full**, which is two constraints at
once: it has to resolve wherever a relying party runs, and the only place all of
them do is inside the cluster; and it has to have a dot in it, because a cookie
jar will not answer to a single-label host and a sign-in is mostly cookies.

A deployment does it the other way -- an ingress holds the certificate and Hydra
is told so with `serve.public.tls.allow_termination_from` -- and
`scripts/cluster.sh` stands **that** up too, as an overlay on this, in its last
phases. What the two shapes cost each other is why both are run: an app behind a
terminator sees plain http while its public origin is https, and one that works
its own scheme out from `r.TLS` is wrong there. One did.

## The walks

The three scripts `scripts/hydra.sh` runs against compose, as Jobs **inside** the
cluster -- which is what makes the Services resolve and the issuer's certificate
trustable:

| | |
| --- | --- |
| `docker/itself.sh` | our own app, round the whole loop: the page nobody is signed in for, the sign-in, the page naming her, the sign-out with the token it kept for it, and the form asked for again |
| `docker/flow.sh` | the protocol with curl, which is where the claims in the token, a second flow the issuer skips the form for, and an `Invalidate` reaching the issuer are checked |
| `docker/behind.sh` | `oauth2-proxy` in front of a page: a **standard third party**, which does its own discovery and fetches the key set itself over TLS it has to be taught to trust -- the one thing our own code cannot check for us |

Somebody to sign in **as** is the rig's, not `deploy/`'s: `Holder`,
`Credential`, `Identity` and `Email` are the ways into an account, and a file
that made those would grant access to whoever can write it. `scripts/cluster.sh`
makes one person the way `docs/operating.md` says to, one `kubectl exec` per
write -- the image is distroless and has no shell to hand a script to.

The same goes for whoever **runs** the deployment. Nothing here runs `init`:
`serve` comes up first, and the operator is made in the pod after it,
`roster init --password-stdin` (`docs/operating.md`, § *The first operator*) --
which the rig does too, since a command at that shell asks who is at it.

## What `--dev` was hiding

Every one of these is a thing `compose.yaml` cannot find, and each cost a run:

- **A relying party's callback must be https.** *Redirect URL is using an
  insecure protocol, http is only allowed for hosts with suffix 'localhost'* --
  so `examples/product` grew `--tls-cert`/`--tls-key`, and the rig issues it a
  certificate from the same CA. A deployment ends TLS at its ingress and needs
  neither.
- **`serve.public.tls.enabled` is not implied** by setting a certificate path.
  Without it Hydra serves plain HTTP and says nothing; what fails is the relying
  party, with *server gave HTTP response to HTTPS client*.
- **The Login App serves no page unless told where it is.** `login.page.dir`
  missing is a 404 at `/login`, which from a browser looks like the issuer is
  broken.

And one about the walk rather than the deployment: the issuer's refusals come
back to the **app's own callback**, so matching the host is not enough to say a
flow worked. Checked that way, an `?error=` read as a success and what surfaced
was the app refusing a code the issuer had never issued.

## The class a fresh cluster cannot see

Everything above starts a fresh process, and a fresh process gets a client's
authentication method right whatever it is registered as: `golang.org/x/oauth2`
probes and caches what worked for the life of that process. So the hour a
deployment signed nobody in was invisible to every gate -- the declaration
changed, the sync applied it, and the pods **already running** kept addressing
Hydra the old way with no second try.

So the rig changes the declaration and syncs, and then asks again without
restarting anything: the check goes red naming the method, the sign-in fails at
the exchange, **a restart does not cure it**, and putting the declaration back
does -- with no restart. The fourth is the assertion, because before the method
was said in code a restart cured it and hid it.
