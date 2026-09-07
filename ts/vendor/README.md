# What is here, and why it should not be

Two packages as tarballs, built by hand from commits their released versions do
not have yet. npm cannot install a package that lives in a subdirectory of a
git repository, so each is built and packed in its checkout and copied here;
`package.json` points at them with `file:`, and the Dockerfile copies this
directory in before `npm ci`.

payday left here for one commit and came back, which is worth a sentence.
`@lesomnus/payday@0.0.4` is on the registry and `package.json` named it the
ordinary way; then the one thing roster needed from payday landed **after** that
release. There is nothing else in it -- `git log ts/v0.0.4..main -- ts/` is that
one commit -- so this tarball is `0.0.4` plus three lines, and it goes when
`0.0.5` is published.

| tarball | from | why |
| --- | --- | --- |
| `lesomnus-grpc-dgram-0.0.1-00e6a6a.tgz` | `00e6a6a` of <https://github.com/lesomnus/grpc-dgram> | one wasm instance serving two entry points (`sock.dial({ entryPoint })`), which the sandbox needs and `0.0.1` does not have |
| `lesomnus-payday-0.0.4-b488985.tgz` | `b488985` of <https://github.com/lesomnus/payday> | `sandbox.Opts.wasmExec`, without which `@lesomnus/payday/sandbox` looks for `wasm_exec.js` at the origin's root -- and this console is served at `/console/`, so `0.0.4` cannot start the sandbox at all |

```sh
cd <grpc-dgram checkout>/ts && npm ci && npm run build && npm pack
cd <payday checkout>/ts && npm ci && npm run build && npm pack
```

`package.json` also carries an `overrides` entry that makes payday's own
dependency on `@lesomnus/grpc-dgram` resolve to the vendored one, so there is
one copy of it: the store is handed transports this app made, and two copies
of the library would be two definitions of the same class. payday's own
dependency is the released `0.0.1`, which is the one without
`dial({ entryPoint })`, so without the override npm installs both and the
customers screen dials a second entry point on a socket that has never heard
of one.

The Go modules are pinned to the same commits in `go.mod`, where a commit is an
ordinary version.

When a package is released, point `package.json` back at the registry for it and
delete its tarball -- and when the last one goes, the `overrides` entry, this
directory and this file.
