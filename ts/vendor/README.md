# What is here, and why it should not be

One package as a tarball, built by hand from a commit its released version does
not have yet. npm cannot install a package that lives in a subdirectory of a
git repository, so it is built and packed in its checkout and copied here;
`package.json` points at it with `file:`, and the Dockerfile copies this
directory in before `npm ci`.

payday was here twice and is not now. It left at `0.0.4`, came back for one
commit because the `wasmExec` this console needs landed after that release, and
left again at `0.0.5`, which carries it. Only grpc-dgram is left, and it is the
harder one: its fix has been on `main` since `00e6a6a` with no release at all.

| tarball | from | why |
| --- | --- | --- |
| `lesomnus-grpc-dgram-0.0.1-00e6a6a.tgz` | `00e6a6a` of <https://github.com/lesomnus/grpc-dgram> | one wasm instance serving two entry points (`sock.dial({ entryPoint })`), which the sandbox needs and `0.0.1` does not have |

```sh
cd <grpc-dgram checkout>/ts && npm ci && npm run build && npm pack
```

`package.json` also carries an `overrides` entry that makes payday's own
dependency on `@lesomnus/grpc-dgram` resolve to the vendored one, so there is
one copy of it: the store is handed transports this app made, and two copies
of the library would be two definitions of the same class. payday's own
dependency is the released `0.0.1`, which is the one without
`dial({ entryPoint })`, so without the override npm installs both and the
customers screen dials a second entry point on a socket that has never heard
of one.

The Go module is pinned to the same commit in `go.mod`, where a commit is an
ordinary version.

When it is released, point `package.json` back at the registry, and delete the
`overrides` entry, this directory and this file with it.
