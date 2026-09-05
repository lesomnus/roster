# What is here, and why it should not be

Two packages as tarballs, built by hand from commits their released versions
do not have yet. npm cannot install a package that lives in a subdirectory of
a git repository, so each is built and packed in its checkout and copied here;
`package.json` points at them with `file:`, and the Dockerfile copies this
directory in before `npm ci`.

| tarball | from | why |
| --- | --- | --- |
| `lesomnus-grpc-dgram-0.0.1-00e6a6a.tgz` | `00e6a6a` of <https://github.com/lesomnus/grpc-dgram> | one wasm instance serving two entry points (`sock.dial({ entryPoint })`), which the sandbox needs and `0.0.1` does not have |
| `lesomnus-payday-0.0.3-332ed12.tgz` | `332ed12` of <https://github.com/lesomnus/payday> | `EntityDesc.service`, which `pd gen --ts` writes into `entities.ts` now, and `@lesomnus/payday/react/devtools`, which reads it |

```sh
cd <grpc-dgram checkout>/ts && npm ci && npm run build && npm pack
cd <payday checkout>/ts && npm ci && npm run build && npm pack
```

`package.json` also carries an `overrides` entry that makes payday's own
dependency on `@lesomnus/grpc-dgram` resolve to the vendored one, so there is
one copy of it: the store is handed transports this app made, and two copies
of the library would be two definitions of the same class.

The Go module is pinned to the same commits in `go.mod`, where a commit is an
ordinary version.

When a package is released, point `package.json` back at the registry for it,
delete its tarball, and -- when the last one goes -- this directory and file.
