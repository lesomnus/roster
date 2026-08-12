# The server, and the entrypoint that seeds it once.
#
# Not a production image: it is what `docker compose up` runs so that somebody
# working on the console has a roster to point at. What it is missing is a
# non-root user, a pinned base digest, and any answer about secrets beyond an
# environment variable -- see `docker/entrypoint.sh`.

FROM golang:1.26 AS build

WORKDIR /src

# The module graph first, so that editing a `.go` file does not re-download it.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/roster ./cmd/roster

FROM alpine:3.22

RUN apk add --no-cache ca-certificates

COPY --from=build /out/roster /usr/local/bin/roster
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["serve"]
