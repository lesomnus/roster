# roster, as an image: the binary, the two pages, and nothing else.
#
# One image and not three. `serve`, `account serve` and `ldap serve` are three
# commands of one binary -- a deployment runs the same image three times with
# three arguments, which is also why the entrypoint is the binary rather than a
# script that picks one.
#
# Two final stages, and which one is built is the caller's:
#
#   app   what a deployment runs. distroless, nonroot, no shell.
#   dev   what `docker compose up` runs. alpine, and the seeding scripts in
#         `docker/`, so the quickstart in `docs/operating.md` is one command.
#
# `docker buildx bake` builds `app`; `compose.yaml` names `target: dev`. Both
# are copies out of one stage, `dist`, and are nothing else -- see there for why.

# The two pages, on the **builder's** architecture, because a page has none.
# Under `platforms` a naive stage would run node twice, the second time under
# emulation, to produce the same bytes.
FROM --platform=$BUILDPLATFORM node:22 AS page

WORKDIR /src/ts

COPY ts/package.json ts/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY ts/ ./
# `tsc` and both vite builds. The sandbox module is not here -- `ts/public/` is
# in `.dockerignore` -- and nothing in a served console asks for it.
RUN npm run build

# Also on the builder's, cross-compiling to the target: a Go toolchain does that
# natively, and emulating one to avoid it is the slow way to the same binary.
FROM --platform=$BUILDPLATFORM golang:1.27 AS base

WORKDIR /src

# The module graph first, so that editing a `.go` file does not re-download it.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Not part of any image, and not in `bake`'s default group either: the gate is
# `./scripts/test.sh`, which is gofmt, the build, the vet, the tests, `pd
# doctor`, `pd gen --check --ts`, the wasm build and the console -- and half of
# that needs node, which is not in this stage. This is the Go half, for
# `docker buildx bake test` on a machine that has neither toolchain.
FROM base AS test
RUN --mount=type=cache,target=/root/.cache/go-build \
	go vet ./... && go test ./...

FROM base AS build

ARG TARGETOS
ARG TARGETARCH

# Which architectures this compiles, one after the other in one stage. Left
# alone it is the one being built, so a desk's `docker compose up --build`
# compiles what it runs and nothing more. CI hands in every one it publishes
# (`docker-bake.hcl`, target `dist`), because there the compiling happens once,
# in one job, and every image after it is a copy.
ARG ARCHS="${TARGETARCH}"

# What `roster version` prints.
#
# The toolchain would stamp `vcs.revision` from the checkout, and cannot here:
# `.git/` is in `.dockerignore`, deliberately, because the build does not need
# it and it is the largest thing in the context. So the version is handed in and
# the revision goes on the image as a label instead (`docker-bake.hcl`), which
# is where something reading a registry can find it anyway.
ARG APP_VERSION="0.0.0-dev"

# roster, and beside it the example product app, which is not roster and is in
# this image anyway.
#
# It is the second half of a deployment's smoke test: `oauth2-proxy` in front of
# a static page proves the issuer is one a standard relying party accepts, and
# this proves the pieces an app written against payday actually uses -- the
# token verified in-process, `sub` read alone, a session of the app's own.
# Neither covers the other and a deployment has both shapes in it.
#
# A second image would be a second build, a second tag and a second thing to
# pin; a second binary is a few megabytes and no pipeline. It is named for what
# it is, nothing runs it unless a deployment says so, and deleting its `go
# build` below is the whole of removing it.
RUN --mount=type=cache,target=/root/.cache/go-build \
	set -e; for arch in ${ARCHS}; do \
		CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${arch} \
		go build -trimpath \
		-ldflags="-s -w -X github.com/lesomnus/payday/version.version=${APP_VERSION}" \
		-o /out/${arch}/roster ./cmd/roster; \
		CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${arch} \
		go build -trimpath -ldflags="-s -w" \
		-o /out/${arch}/example-product ./examples/product; \
	done

# Everything an image is made of, and nothing an image adds: a binary per
# architecture and the pages, which have none.
#
#   /amd64/roster  /amd64/example-product
#   /arm64/...
#   /pages/{console,user,account,login}
#
# The two images below copy out of this stage and out of nothing else, which is
# what lets CI build it **once**: `docker-bake.hcl`'s `dist` writes it to a
# directory, and every image after that is built with the directory standing in
# for the stage (a named context called `dist`). So the binary `scripts/cluster.sh`
# and `scripts/hydra.sh` test is the binary that is pushed, and nothing compiles
# twice. Built without that, as `docker compose up --build` does, it is an
# ordinary stage and the images are what they always were.
FROM scratch AS dist
COPY --from=build /out/ /
COPY --from=page /src/ts/dist/console /pages/console
COPY --from=page /src/ts/dist/user /pages/user
COPY --from=page /src/ts/dist/account /pages/account
COPY --from=page /src/ts/dist/login /pages/login

# Static rather than scratch: `roster account serve` makes outbound TLS calls to
# whatever providers a tenant wrote down as `Connection` rows, so it needs root
# certificates, and this is the smallest base that has them and a nonroot uid.
FROM gcr.io/distroless/static-debian12:nonroot AS app

ARG TARGETARCH

COPY --from=dist /${TARGETARCH}/roster /usr/local/bin/roster
COPY --from=dist /${TARGETARCH}/example-product /usr/local/bin/example-product

# Where the four pages land. A deployment points at each:
#
#   admin.console.dir              the admin console, at `/` on that listener
#   user_console.dir               the user console, at `/` on `server.http`
#   account.page.dir               the account page
#   login.page.dir                 the Login App's, for a deployment with Hydra
COPY --from=dist /pages/ /usr/share/roster/

USER nonroot:nonroot

# No EXPOSE and no default port. Every listener is named in the configuration
# and there are three of them with three answers about who may reach them --
# `docs/operating.md`, "The two planes". A port declared here would be a fourth
# answer that is not the deployment's.
ENTRYPOINT ["/usr/local/bin/roster"]
CMD ["serve"]

# The compose image: the same binary and pages, plus a shell and the scripts
# that seed a deployment once so `docker compose up` has a customer in it.
#
# Not a production image, and the difference is the point: it runs as root, the
# base is not pinned by digest, and its answer about secrets is an environment
# variable. See `docker/entrypoint.sh`.
FROM alpine:3.22 AS dev

# `curl` and `oathtool` beside the certificates because this stage is the one
# the scripts in `docker/` run in, and `scripts/hydra.sh` walks a whole OAuth
# flow with them -- from **inside** the compose network, so the walk needs
# nothing published and no second image. busybox `wget` cannot do the first: the
# flow is redirects and cookies. And the second form wants six digits from a
# seed, which is an authenticator app's whole job and `oathtool`'s.
RUN apk add --no-cache ca-certificates curl oath-toolkit-oathtool

ARG TARGETARCH

COPY --from=dist /${TARGETARCH}/roster /usr/local/bin/roster
COPY --from=dist /${TARGETARCH}/example-product /usr/local/bin/example-product
COPY --from=dist /pages/ /usr/share/roster/
COPY docker/entrypoint.sh docker/customer.sh docker/account.sh docker/ldap.sh docker/login.sh docker/dial.sh docker/flow.sh docker/behind.sh docker/itself.sh docker/device.sh /usr/local/bin/

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["serve"]
