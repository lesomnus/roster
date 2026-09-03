# The server, the two pages, and the entrypoint that seeds it once.
#
# Not a production image: it is what `docker compose up` runs so that somebody
# working on the pages has a roster to point at, with a customer in it. What it is missing is a
# non-root user, a pinned base digest, and any answer about secrets beyond an
# environment variable -- see `docker/entrypoint.sh`.

# The two pages, built once here so the image serves them: the console under
# `/console/` on the control listener, and the account page for
# `roster account serve`.
FROM node:22 AS page

WORKDIR /src/ts

COPY ts/package.json ts/package-lock.json ./
RUN npm ci

COPY ts/ ./
RUN npm run build

FROM golang:1.27 AS build

WORKDIR /src

# The module graph first, so that editing a `.go` file does not re-download it.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/roster ./cmd/roster

FROM alpine:3.22

RUN apk add --no-cache ca-certificates

COPY --from=build /out/roster /usr/local/bin/roster
COPY --from=page /src/ts/dist/console /usr/share/roster/console
COPY --from=page /src/ts/dist/account /usr/share/roster/account
COPY docker/entrypoint.sh docker/customer.sh docker/account.sh /usr/local/bin/

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["serve"]
