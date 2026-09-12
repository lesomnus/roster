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
  there when the console is served.

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

What an overlay is expected to bring, and what it should leave alone:

| | |
| --- | --- |
| its own | hosts, secrets, storage class, image digests, the products it actually runs, and `config.yaml` -- by a `configMapGenerator` for `roster` with `behavior: replace` |
| the base's | what a client has to be registered with, which `URLS_*` Hydra needs, that the clients are applied and then **checked** -- the four things that were wrong when they were written twice |

The pieces of the rig that a deployment does not want are `product.yaml` (there
to have something to sign in *to*) and `secrets.yaml` above; both come out with a
`$patch: delete`.

**And the other shape of relying party is deliberately not here.** `oauth2-proxy`
in front of a page is half of what a deployment runs
(`docs/relying-party.md`), and it was moved into this directory and moved
straight back out: the issuer refuses a redirect URI over plain http unless the
host ends in `.localhost`, so that shape needs something terminating TLS in front
of it -- an **ingress**, which is the deployment's and not this base's.
`product.yaml` is here only because it can serve its own certificate.
So the proxy demo lives in whatever has a terminator: `scripts/cluster.sh`'s
overlay, or a deployment beside its own ingress. `scripts/cluster.sh`'s own overlay (`--behind`'s phases) is a
worked example of everything on this list except the secrets.

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
