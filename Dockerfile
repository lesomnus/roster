# roster, as an image: two binaries -- roster, and the example product beside
# it -- and the four pages roster serves, and nothing else.
#
# One image and not five. `serve`, `account serve`, `ldap serve`, `login serve`
# and `scim serve` are five commands of one binary -- a deployment runs the same
# image once per command with its own arguments, which is also why the
# entrypoint is the binary rather than a script that picks one.
#
# Two final stages:
#
#   app   what a deployment runs. distroless, uid 65532, no shell.
#   dev   what `docker compose up` runs. alpine, and the seeding scripts in
#         `docker/`, so the quickstart in `docs/operating.md` is one command.
#
# `app` is the last stage, so a `docker build .` that names no `--target` makes
# it, and `docker buildx bake` builds it too; `compose.yaml` names `target:
# dev`. `dev` is never what a build gets by default, since it seeds an operator
# whose password is `admin` where nothing else is said. Both are copies out of
# one stage, `dist`, and are nothing else -- see there for why.

# The four pages, on the **builder's** architecture, because a page has none.
# Under `platforms` a naive stage would run node twice, the second time under
# emulation, to produce the same bytes.
FROM --platform=$BUILDPLATFORM node:26 AS page

WORKDIR /src/ts

COPY ts/package.json ts/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY ts/ ./
# `tsc` and the four vite builds. The sandbox module is not here --
# `ts/public/` is in `.dockerignore` -- and nothing in a served console asks for
# it.
RUN npm run build

# Also on the builder's, cross-compiling to the target: a Go toolchain does that
# natively, and emulating one to avoid it is the slow way to the same binary.
#
# The Go of `go.mod`'s `toolchain`, to the patch: the binary pushed is compiled
# by the Go every test ran on, and not by whichever 1.27 was newest the day the
# image was built.
FROM --platform=$BUILDPLATFORM golang:1.27.4 AS base

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
# it and it is the largest thing in the context. So both are handed in: the
# version, and the commit, which `roster version --short` prints and the image
# carries as a label as well (`docker-bake.hcl`), where something reading a
# registry looks for it. A build that hands in no commit makes a binary that
# says none, as a build at a desk always has.
ARG APP_VERSION="0.0.0-dev"
ARG APP_REVISION=""

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
		-ldflags="-s -w -X github.com/lesomnus/payday/version.version=${APP_VERSION} -X github.com/lesomnus/roster/cli.revision=${APP_REVISION}" \
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

# The compose image: the same binary and pages, plus a shell and the scripts
# that seed a deployment once so `docker compose up` has a customer in it.
#
# Not a production image, and the difference is the point: it runs as root, its
# base is named by a tag alone where `app`'s is pinned by its digest, and its
# answer about secrets is an environment variable. See `docker/entrypoint.sh`.
# It is not the last stage, so a build gets it only by naming it.
FROM alpine:3.24 AS dev

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

# Static rather than scratch: `roster account serve` makes outbound TLS calls to
# whatever providers a tenant wrote down as `Connection` rows, so it needs root
# certificates, and this is the smallest base that has them and a nonroot uid.
#
# By its digest, beside the tag it had when it was written: `nonroot` names no
# version and moves when Google rebuilds it, so without the digest two builds
# of one commit could be two bases. Renovate's Dockerfile manager moves the
# digest when the tag moves, in a pull request the gates run on.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS app

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

# By number rather than by name: it is distroless's `nonroot`, and a kubelet
# told `runAsNonRoot` with no `runAsUser` checks the image's user and cannot
# check a name.
USER 65532:65532

# No EXPOSE and no default port. Every listener is named in the configuration
# and there are three of them with three answers about who may reach them --
# `docs/operating.md`, "The two planes". A port declared here would be a fourth
# answer that is not the deployment's.
ENTRYPOINT ["/usr/local/bin/roster"]
CMD ["serve"]
