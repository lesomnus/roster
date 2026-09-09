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
# `docker buildx bake` builds `app`; `compose.yaml` names `target: dev`.

# The two pages, on the **builder's** architecture, because a page has none.
# Under `platforms` a naive stage would run node twice, the second time under
# emulation, to produce the same bytes.
FROM --platform=$BUILDPLATFORM node:22 AS page

WORKDIR /src/ts

# `vendor/` beside the manifests: `@lesomnus/grpc-dgram` is a tarball built from
# a commit until it is released, and `npm ci` reads it. See `ts/vendor/README.md`.
COPY ts/package.json ts/package-lock.json ./
COPY ts/vendor ./vendor
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

# What `roster version` prints.
#
# The toolchain would stamp `vcs.revision` from the checkout, and cannot here:
# `.git/` is in `.dockerignore`, deliberately, because the build does not need
# it and it is the largest thing in the context. So the version is handed in and
# the revision goes on the image as a label instead (`docker-bake.hcl`), which
# is where something reading a registry can find it anyway.
ARG APP_VERSION="0.0.0-dev"

RUN --mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath \
	-ldflags="-s -w -X github.com/lesomnus/payday/version.version=${APP_VERSION}" \
	-o /out/roster ./cmd/roster

# Static rather than scratch: `roster account serve` makes outbound TLS calls to
# whatever providers a tenant wrote down as `Connection` rows, so it needs root
# certificates, and this is the smallest base that has them and a nonroot uid.
FROM gcr.io/distroless/static-debian12:nonroot AS app

COPY --from=build /out/roster /usr/local/bin/roster

# Where the pages land. A deployment points at them:
#
#   control.console.dir            the console, served under `/console/`
#   roster account serve --static  the account page
COPY --from=page /src/ts/dist/console /usr/share/roster/console
COPY --from=page /src/ts/dist/account /usr/share/roster/account
COPY --from=page /src/ts/dist/login /usr/share/roster/login

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

COPY --from=build /out/roster /usr/local/bin/roster
COPY --from=page /src/ts/dist/console /usr/share/roster/console
COPY --from=page /src/ts/dist/account /usr/share/roster/account
COPY --from=page /src/ts/dist/login /usr/share/roster/login
COPY docker/entrypoint.sh docker/customer.sh docker/account.sh docker/ldap.sh docker/login.sh docker/flow.sh /usr/local/bin/

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["serve"]
