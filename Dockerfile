# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS build
WORKDIR /src
# Cache modules first (no deps yet, but keeps the layer stable).
COPY go.mod ./
RUN go mod download
COPY main.go index.html ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /dday .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /dday /dday
EXPOSE 3329
USER nonroot:nonroot
ENTRYPOINT ["/dday"]
