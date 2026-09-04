# What is here, and why it should not be

`@lesomnus/grpc-dgram` as a tarball, built from commit `00e6a6a` of
<https://github.com/lesomnus/grpc-dgram> -- the one that lets one wasm
instance serve two entry points (`sock.dial({ entryPoint })`), which the
sandbox needs and the released `0.0.1` does not have. npm cannot install a
package that lives in a subdirectory of a git repository, so it is built and
packed by hand:

```sh
cd <grpc-dgram checkout>/ts && npm ci && npm run build && npm pack
```

`package.json` points at it with `file:`, and the Dockerfile copies this
directory in before `npm ci`. The Go module is pinned to the same commit in
`go.mod`, where a commit is an ordinary version.

When the package is released, point `package.json` back at the registry,
delete this directory, and delete this file.
