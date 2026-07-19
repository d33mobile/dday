# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /dday .
# Stage the DB directory here so it can be copied with nonroot ownership below;
# the distroless image has no shell to mkdir/chown at build time.
RUN mkdir -p /data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /dday /dday
# /data holds the SQLite DB. Owning it as nonroot (uid 65532) means a fresh
# named volume mounted here inherits that ownership, so the nonroot process can
# write dday.db (+ WAL/SHM) without any host-side chown.
COPY --from=build --chown=65532:65532 /data /data
EXPOSE 3329
USER nonroot:nonroot
ENTRYPOINT ["/dday"]
