# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /dday .
# Also build the Matrix bot — a separate long-lived process. Both binaries ship
# in the final image; compose picks which one runs per service via entrypoint.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bot ./cmd/bot
# Stage the DB directory here so it can be copied with nonroot ownership below;
# the distroless image has no shell to mkdir/chown at build time.
RUN mkdir -p /data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /dday /dday
COPY --from=build /bot /bot
# /data holds the SQLite DB. Owning it as nonroot (uid 65532) means a fresh
# named volume mounted here inherits that ownership, so the nonroot process can
# write dday.db (+ WAL/SHM) without any host-side chown.
COPY --from=build --chown=65532:65532 /data /data
EXPOSE 3329
USER nonroot:nonroot
# The distroless image has no shell/wget, so probe via the binary's own
# -healthcheck flag (it GETs /healthz on localhost and exits 0/1).
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD ["/dday", "-healthcheck"]
ENTRYPOINT ["/dday"]
