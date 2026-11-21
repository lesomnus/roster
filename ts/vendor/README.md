# Nox package

`lesomnus-nox-0.1.0-5965091.tgz` is the unmodified output of `npm pack` from
<https://github.com/lesomnus/nox/commit/5965091a027d125cb71b878d88d9dcf7f622e4fd>.
Nox is not published to npm yet. Its documented tarball installation supplies
compiled JS, CSS and declarations, rather than making roster compile a library
under roster's own TypeScript settings. `package-lock.json` records its integrity.
No dependency on another project's local checkout or Git credentials is needed.

To reproduce the package in a clean Nox checkout at that commit:

```sh
npm ci
npm pack
```

The scoped Monaco override in `package.json` keeps roster's existing Monaco
version. Nox declares an optional `^0.55.0` editor peer; this app consumes only
the core UI entry and does not use `@lesomnus/nox/editor`.

Once Nox is published, replace the tarball dependency with a chosen registry
version, remove the archive, and re-run the account UI checks.
