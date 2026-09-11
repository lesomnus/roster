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

## What is deliberately fake

The secrets. `secrets.yaml` holds values anybody can read, because this is a
rig: a deployment overlays its own and never uses these. They are checked in
rather than generated because a generated secret is one the walks would have to
be told about, and then the rig has a moving part a deployment does not.

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

What that gives up, and is not covered anywhere yet, is **being behind a
proxy**: `X-Forwarded-Proto`, and an app that reads `r.TLS`. A deployment ends
TLS at its ingress and tells Hydra so with
`serve.public.tls.allow_termination_from` instead.

## The walk

`docker/itself.sh` -- the same script `scripts/hydra.sh` runs against compose --
as a Job **inside** the cluster, which is what makes the Services resolve and
the issuer's certificate trustable. A product goes round the whole loop: the
page nobody is signed in for, the sign-in, the page naming her, the sign-out
with the token it kept for it, and the form asked for again.

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

## What is still not here

**Being behind a proxy.** Hydra terminates TLS itself here, so
`X-Forwarded-Proto` and an app reading `r.TLS` are still covered nowhere. A
deployment ends TLS at its ingress and tells Hydra with
`serve.public.tls.allow_termination_from`.

**A registration changed under a running pod**, which is the one class a rig
that builds a cluster and walks it once cannot see: `golang.org/x/oauth2` probes
for the client authentication method and caches it, so an app keeps addressing a
changed client the old way with no second try. It needs a step that mutates and
tries again without restarting anything.
