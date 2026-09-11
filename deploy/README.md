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

## What is not here yet

Signing anybody in. That needs TLS in front -- Hydra outside `--dev` refuses an
`http://` issuer -- and a relying party, and then it would be `docker/`'s walks
pointed at this instead of at `compose.yaml`. Until then what this proves is
that the manifests stand up and that `roster login doctor` passes against what
they produce, which is the half every defect so far has been in.
