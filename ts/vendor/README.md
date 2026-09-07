# What is here, and why it should not be

One package as a tarball, built by hand from a commit its released version does
not have yet. npm cannot install a package that lives in a subdirectory of a
git repository, so it is built and packed in its checkout and copied here;
`package.json` points at it with `file:`, and the Dockerfile copies this
directory in before `npm ci`.

payday used to be here too and is not any more: `@lesomnus/payday@0.0.4` is on
the registry, and `package.json` names it the ordinary way.

| tarball | from | why |
| --- | --- | --- |
| `lesomnus-grpc-dgram-0.0.1-00e6a6a.tgz` | `00e6a6a` of <https://github.com/lesomnus/grpc-dgram> | one wasm instance serving two entry points (`sock.dial({ entryPoint })`), which the sandbox needs and `0.0.1` does not have |

```sh
cd <grpc-dgram checkout>/ts && npm ci && npm run build && npm pack
```

`package.json` also carries an `overrides` entry that makes payday's own
dependency on `@lesomnus/grpc-dgram` resolve to the vendored one, so there is
one copy of it: the store is handed transports this app made, and two copies
of the library would be two definitions of the same class. That entry matters
more now than it did -- payday comes from the registry, where its dependency
is the released `0.0.1`, and without the override npm would install both.

The Go module is pinned to the same commit in `go.mod`, where a commit is an
ordinary version.

When it is released, point `package.json` back at the registry, drop the
`overrides` entry, and delete this directory and the tarball in it.
